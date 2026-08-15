package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// EntraUser is a Microsoft Entra directory user, provisioned via the standard
// Microsoft Graph surface (POST /v1.0/users). Tokens the sim mints carry the
// user's oid/sub, and group claims resolve through the membership store. Props
// holds every other member of the Graph user the client wrote — accountEnabled,
// mailNickname, jobTitle, usageLocation and the rest — so a read answers with
// what was written rather than a fixed subset.
type EntraUser struct {
	OID               string `json:"oid"`
	Sub               string `json:"sub"`
	PreferredUsername string `json:"preferredUsername"`
	Name              string `json:"name"`
	Email             string `json:"email,omitempty"`

	Props map[string]json.RawMessage `json:"props,omitempty"`
}

// EntraGraphGroup is a standalone group created via POST /v1.0/groups.
type EntraGraphGroup struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	MailNickname    string `json:"mailNickname"`
	SecurityEnabled bool   `json:"securityEnabled"`
	MailEnabled     bool   `json:"mailEnabled"`

	Props map[string]json.RawMessage `json:"props,omitempty"`
}

// entraGroupMembership records one directory object being a member of one
// group. Graph's group `members` collection admits users, service principals,
// groups and devices alike, so the member is held by object ID rather than by
// a user-specific reference.
type entraGroupMembership struct {
	GroupID  string
	MemberID string
}

// EntraApplication is an Entra (Azure AD) application registration created via
// POST /v1.0/applications. The application's appId is the client identifier a
// service principal references. PasswordCredentials are the client secrets the
// v2.0 token endpoint validates for the client_credentials grant — exactly the
// role app-registration secrets play on real Microsoft Entra. Props holds every
// other member of the Graph application the client wrote — api, web, spa,
// requiredResourceAccess, identifierUris and the rest.
type EntraApplication struct {
	ID                  string                    `json:"id"`
	AppID               string                    `json:"appId"`
	DisplayName         string                    `json:"displayName"`
	SignInAudience      string                    `json:"signInAudience,omitempty"`
	PasswordCredentials []EntraPasswordCredential `json:"passwordCredentials,omitempty"`

	Props map[string]json.RawMessage `json:"props,omitempty"`
}

// EntraServicePrincipal is the directory object that materializes an
// application (or managed identity) into a tenant. Its id is the principal's
// object ID (OID) — the value RBAC role assignments reference.
type EntraServicePrincipal struct {
	ID                   string                    `json:"id"`
	AppID                string                    `json:"appId"`
	DisplayName          string                    `json:"displayName"`
	ServicePrincipalType string                    `json:"servicePrincipalType"`
	PasswordCredentials  []EntraPasswordCredential `json:"passwordCredentials,omitempty"`

	Props map[string]json.RawMessage `json:"props,omitempty"`
}

// EntraPasswordCredential is one client secret minted via addPassword. Real
// Graph returns the secretText only on creation; subsequent reads carry the
// metadata plus a three-character hint. The stored SecretHash (SHA-256 of the
// secretText) is what the v2.0 token endpoint validates — real Microsoft
// Entra likewise stores only a derived verifier. Graph wire responses are
// hand-built by entraPasswordCredentialJSON and never include the hash; the
// json tag exists only so the nested hash persists inside the stored
// application / service-principal records (the persistence sidecar covers
// top-level json:"-" fields only).
type EntraPasswordCredential struct {
	KeyID         string `json:"keyId"`
	DisplayName   string `json:"displayName,omitempty"`
	SecretText    string `json:"secretText,omitempty"`
	Hint          string `json:"hint,omitempty"`
	StartDateTime string `json:"startDateTime,omitempty"`
	EndDateTime   string `json:"endDateTime,omitempty"`
	SecretHash    string `json:"secretHash,omitempty"`
}

// The Entra directory stores are bound to the server's database in
// registerEntra (and its application / service-principal sub-registrations),
// so directory state — users, groups, memberships, app registrations, and
// service principals with their credential hashes — survives a SIM_PERSIST
// restart. registerEntra runs before any other service registration reads or
// writes the directory (managed identities materialize service principals at
// register time) and before the server accepts requests, so auth.go token
// issuance always sees initialized stores.
var (
	entraUsersStore sim.Store[EntraUser]

	entraGraphGroupStore      sim.Store[EntraGraphGroup]
	entraGroupMembershipStore sim.Store[entraGroupMembership]

	entraApplicationStore      sim.Store[EntraApplication]
	entraServicePrincipalStore sim.Store[EntraServicePrincipal]
)

// The members the simulator models on its own typed records, plus the members
// Microsoft Graph owns (the object identifiers it assigns) and the write-only
// members it never echoes back (passwordProfile). A write never puts these in
// the generic property bag: they are either applied to the typed record or
// dropped.
var (
	entraApplicationServiceOwned = map[string]bool{
		"id": true, "appId": true, "displayName": true, "signInAudience": true,
		"passwordCredentials": true, "keyCredentials": true,
		"createdDateTime": true, "deletedDateTime": true, "publisherDomain": true,
	}
	entraServicePrincipalServiceOwned = map[string]bool{
		"id": true, "appId": true, "displayName": true, "servicePrincipalType": true,
		"passwordCredentials": true, "keyCredentials": true, "deletedDateTime": true,
	}
	entraGroupServiceOwned = map[string]bool{
		"id": true, "displayName": true, "description": true, "mailNickname": true,
		"securityEnabled": true, "mailEnabled": true,
		"createdDateTime": true, "deletedDateTime": true,
	}
	entraUserServiceOwned = map[string]bool{
		"id": true, "displayName": true, "userPrincipalName": true, "mail": true,
		"passwordProfile": true, "createdDateTime": true, "deletedDateTime": true,
	}
)

// entraRegisterServicePrincipal records a service principal in the directory.
// It is the single registration point for both application-backed principals
// (POST /v1.0/servicePrincipals) and managed-identity-backed principals
// (managedidentity.go calls this when a user-assigned identity is created), so
// GET /v1.0/servicePrincipals/{id} resolves either by the principal's object ID.
func entraRegisterServicePrincipal(id, appID, displayName, spType string) {
	entraServicePrincipalStore.Put(id, EntraServicePrincipal{
		ID:                   id,
		AppID:                appID,
		DisplayName:          displayName,
		ServicePrincipalType: spType,
	})
}

// entraUnregisterServicePrincipal removes a service principal — used when a
// managed identity backing one is deleted.
func entraUnregisterServicePrincipal(id string) {
	entraServicePrincipalStore.Delete(id)
}

// entraDefaultUser is the directory's built-in identity. The sim has no
// interactive sign-in page, so browser grants that carry no login_hint (real
// Azure AD would resolve the browser session's signed-in user there) mint
// tokens for this fixed identity instead.
var entraDefaultUser = EntraUser{
	OID:               "test-oid",
	Sub:               "test-sub",
	PreferredUsername: "sockerless-test@example.com",
	Name:              "Sockerless Test User",
}

// entraBootstrapClientID and entraBootstrapClientSecret are the well-known
// application registration seeded into every directory the simulator serves —
// the Microsoft Entra equivalent of the AWS simulator's seeded "test"/"test"
// credential. Real Microsoft Entra requires a registered application, a
// service principal materializing it in the tenant, and a client secret
// before the v2.0 client_credentials grant issues a token; every SDK, CLI, and
// Terraform harness authenticates against this seeded registration exactly as
// a real confidential client authenticates against one an administrator
// provisioned.
const (
	entraBootstrapClientID     = "test-client-id"
	entraBootstrapClientSecret = "test-client-secret"
	entraBootstrapAppObjectID  = "00000000-0000-0000-0000-0000000000b1"
	entraBootstrapSPObjectID   = "00000000-0000-0000-0000-0000000000b2"
)

// entraSeedBootstrap seeds the bootstrap application registration, its service
// principal, and its client secret so the client_credentials grant is
// immediately usable for entraBootstrapClientID — the same directory state an
// administrator would provision once via the Certificates & secrets blade and
// the Enterprise applications blade before handing the credential to
// automation. Seeding is idempotent: a directory that already holds the
// bootstrap rows (a persisted database on restart) keeps them untouched, so
// credentials added to the bootstrap registration survive.
func entraSeedBootstrap() {
	if _, ok := entraApplicationStore.Get(entraBootstrapAppObjectID); !ok {
		hash := sha256.Sum256([]byte(entraBootstrapClientSecret))
		now := time.Now().UTC()
		entraApplicationStore.Put(entraBootstrapAppObjectID, EntraApplication{
			ID:             entraBootstrapAppObjectID,
			AppID:          entraBootstrapClientID,
			DisplayName:    "Sockerless Bootstrap",
			SignInAudience: "AzureADMyOrg",
			PasswordCredentials: []EntraPasswordCredential{{
				KeyID:         "00000000-0000-0000-0000-0000000000b3",
				DisplayName:   "bootstrap",
				StartDateTime: now.Format(time.RFC3339),
				EndDateTime:   now.AddDate(10, 0, 0).Format(time.RFC3339),
				SecretHash:    base64.RawStdEncoding.EncodeToString(hash[:]),
			}},
		})
	}
	if _, ok := entraServicePrincipalStore.Get(entraBootstrapSPObjectID); !ok {
		entraRegisterServicePrincipal(entraBootstrapSPObjectID, entraBootstrapClientID, "Sockerless Bootstrap", "Application")
	}
}

// getEntraSimUser looks up a directory user by oid, falling back to the
// built-in default identity.
func getEntraSimUser(oid string) EntraUser {
	u, ok := entraUsersStore.Get(oid)
	if ok {
		return u
	}
	return entraDefaultUser
}

// entraLookupUser resolves a directory user by object ID, including the
// built-in default identity, without substituting one for the other.
func entraLookupUser(oid string) (EntraUser, bool) {
	if u, ok := entraUsersStore.Get(oid); ok {
		return u, true
	}
	if oid == entraDefaultUser.OID {
		return entraDefaultUser, true
	}
	return EntraUser{}, false
}

func findEntraUserByUPN(upn string) (EntraUser, bool) {
	upn = strings.TrimSpace(upn)
	if upn == "" {
		return EntraUser{}, false
	}
	if strings.EqualFold(entraDefaultUser.PreferredUsername, upn) {
		return entraDefaultUser, true
	}
	users := entraUsersStore.Filter(func(u EntraUser) bool {
		return strings.EqualFold(u.PreferredUsername, upn)
	})
	if len(users) == 0 {
		return EntraUser{}, false
	}
	return users[0], true
}

// newGraphID returns a random UUID-shaped object ID for Graph resources.
func newGraphID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// parseOIDFromBearer decodes the oid claim from a Bearer JWT without signature
// verification — the sim trusts its own tokens internally.
func parseOIDFromBearer(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		OID string `json:"oid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.OID == "" {
		return "", false
	}
	return claims.OID, true
}

func registerEntra(srv *sim.Server) {
	entraUsersStore = sim.MakeStore[EntraUser](srv.DB(), "entra_users")
	entraGraphGroupStore = sim.MakeStore[EntraGraphGroup](srv.DB(), "entra_groups")
	entraGroupMembershipStore = sim.MakeStore[entraGroupMembership](srv.DB(), "entra_group_memberships")

	// Token-issuance state (RS256 signing key, refresh tokens) is directory
	// state too — a restart must neither rotate the JWKS out from under
	// live bearers nor invalidate issued refresh tokens.
	registerAzureAuthState(srv)

	registerEntraGroups(srv)
	registerEntraUsers(srv)
	registerEntraApplications(srv)
	registerEntraServicePrincipals(srv)
	registerEntraDirectory(srv)
	entraSeedBootstrap()

	// Microsoft Graph delegated read endpoints.
	// Real URL: https://graph.microsoft.com/v1.0/me/memberOf
	// The sim is configured as the graph endpoint in metadata.go, so requests
	// hit this process. The oid from the bearer token identifies the user whose
	// group memberships are read from the membership store.
	srv.HandleFunc("GET /v1.0/me/memberOf", handleGraphMemberOf)
	srv.HandleFunc("GET /v1.0/me/transitiveMemberOf", handleGraphMemberOf)
	srv.HandleFunc("GET /beta/me/memberOf", handleGraphMemberOf)
	srv.HandleFunc("GET /beta/me/transitiveMemberOf", handleGraphMemberOf)
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

// registerEntraGroups mounts the Microsoft Graph group surface — the group
// itself, its members and owners reference collections, and the groups it
// belongs to. Real URL base: https://graph.microsoft.com/v1.0/groups.
// terraform-provider-azuread drives the whole family through the beta
// endpoint, so both versions are mounted over the same store.
func registerEntraGroups(srv *sim.Server) {
	srv.HandleFunc("POST /v1.0/groups", handleGraphCreateGroup)
	srv.HandleFunc("POST /beta/groups", handleGraphCreateGroup)
	srv.HandleFunc("GET /v1.0/groups", handleGraphListGroups)
	srv.HandleFunc("GET /beta/groups", handleGraphListGroups)
	srv.HandleFunc("GET /v1.0/groups/{groupId}", handleGraphGetGroup)
	srv.HandleFunc("GET /beta/groups/{groupId}", handleGraphGetGroup)
	srv.HandleFunc("PATCH /v1.0/groups/{groupId}", handleGraphUpdateGroup)
	srv.HandleFunc("PATCH /beta/groups/{groupId}", handleGraphUpdateGroup)
	srv.HandleFunc("DELETE /v1.0/groups/{groupId}", handleGraphDeleteGroup)
	srv.HandleFunc("DELETE /beta/groups/{groupId}", handleGraphDeleteGroup)

	srv.HandleFunc("GET /v1.0/groups/{groupId}/members", handleGraphListGroupMembers)
	srv.HandleFunc("GET /beta/groups/{groupId}/members", handleGraphListGroupMembers)
	srv.HandleFunc("POST /v1.0/groups/{groupId}/members/$ref", handleGraphAddGroupMemberRef)
	srv.HandleFunc("POST /beta/groups/{groupId}/members/$ref", handleGraphAddGroupMemberRef)
	srv.HandleFunc("DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref", handleGraphRemoveGroupMemberRef)
	srv.HandleFunc("DELETE /beta/groups/{groupId}/members/{memberId}/$ref", handleGraphRemoveGroupMemberRef)

	srv.HandleFunc("GET /v1.0/groups/{groupId}/owners", handleGraphListGroupOwners)
	srv.HandleFunc("GET /beta/groups/{groupId}/owners", handleGraphListGroupOwners)
	srv.HandleFunc("POST /v1.0/groups/{groupId}/owners/$ref", handleGraphAddGroupOwnerRef)
	srv.HandleFunc("POST /beta/groups/{groupId}/owners/$ref", handleGraphAddGroupOwnerRef)
	srv.HandleFunc("DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref", handleGraphRemoveGroupOwnerRef)
	srv.HandleFunc("DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref", handleGraphRemoveGroupOwnerRef)

	srv.HandleFunc("GET /v1.0/groups/{groupId}/memberOf", handleGraphGroupMemberOf)
	srv.HandleFunc("GET /beta/groups/{groupId}/memberOf", handleGraphGroupMemberOf)
}

// entraGroupDoc renders a group in Graph's wire shape.
func entraGroupDoc(r *http.Request, grp EntraGraphGroup) map[string]any {
	return graphDoc(r, "groups", map[string]any{
		"id":              grp.ID,
		"displayName":     grp.DisplayName,
		"description":     grp.Description,
		"mailNickname":    grp.MailNickname,
		"securityEnabled": grp.SecurityEnabled,
		"mailEnabled":     grp.MailEnabled,
	}, grp.Props)
}

func handleGraphCreateGroup(w http.ResponseWriter, r *http.Request) {
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		DisplayName     string `json:"displayName"`
		MailNickname    string `json:"mailNickname"`
		Description     string `json:"description,omitempty"`
		SecurityEnabled bool   `json:"securityEnabled"`
		MailEnabled     bool   `json:"mailEnabled"`
	}
	if err := graphDecodeProps(body.Props, &req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayName == "" {
		sim.AzureError(w, "Request_BadRequest", "displayName is required", http.StatusBadRequest)
		return
	}
	grp := EntraGraphGroup{
		ID:              newGraphID(),
		DisplayName:     req.DisplayName,
		Description:     req.Description,
		MailNickname:    req.MailNickname,
		SecurityEnabled: req.SecurityEnabled,
		MailEnabled:     req.MailEnabled,
		Props:           graphMergeProps(nil, body.Props, entraGroupServiceOwned),
	}
	// Graph resolves the owners and members navigation-property bindings the
	// create request carries before it answers, so the reference collections
	// are populated by the time the client reads them back.
	for _, ownerID := range body.Binds["owners"] {
		if _, ok := entraDirectoryObjectDoc(r, ownerID); !ok {
			entraGraphNotFound(w, ownerID)
			return
		}
	}
	for _, memberID := range body.Binds["members"] {
		if _, ok := entraDirectoryObjectDoc(r, memberID); !ok {
			entraGraphNotFound(w, memberID)
			return
		}
	}
	entraGraphGroupStore.Put(grp.ID, grp)
	entraAddOwners(grp.ID, body.Binds["owners"])
	for _, memberID := range body.Binds["members"] {
		entraAddGroupMember(grp.ID, memberID)
	}
	doc := entraGroupDoc(r, grp)
	doc["@odata.context"] = "$metadata#groups/$entity"
	sim.WriteJSON(w, http.StatusCreated, doc)
}

func handleGraphListGroups(w http.ResponseWriter, r *http.Request) {
	groups := entraGraphGroupStore.List()
	docs := make([]map[string]any, 0, len(groups))
	for _, grp := range groups {
		docs = append(docs, entraGroupDoc(r, grp))
	}
	docs, ok := graphFilterDocs(w, r, docs)
	if !ok {
		return
	}
	graphCollection(w, r, "$metadata#groups", docs)
}

func handleGraphGetGroup(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	grp, ok := entraGraphGroupStore.Get(groupID)
	if !ok {
		entraGraphNotFound(w, groupID)
		return
	}
	doc := entraGroupDoc(r, grp)
	doc["@odata.context"] = "$metadata#groups/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

func handleGraphUpdateGroup(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	updated := entraGraphGroupStore.Update(groupID, func(g *EntraGraphGroup) {
		graphSetString(body.Props, "displayName", &g.DisplayName)
		graphSetString(body.Props, "description", &g.Description)
		graphSetString(body.Props, "mailNickname", &g.MailNickname)
		graphSetBool(body.Props, "securityEnabled", &g.SecurityEnabled)
		graphSetBool(body.Props, "mailEnabled", &g.MailEnabled)
		g.Props = graphMergeProps(g.Props, body.Props, entraGroupServiceOwned)
	})
	if !updated {
		entraGraphNotFound(w, groupID)
		return
	}
	entraAddOwners(groupID, body.Binds["owners"])
	for _, memberID := range body.Binds["members"] {
		entraAddGroupMember(groupID, memberID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphDeleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	if !entraGraphGroupStore.Delete(groupID) {
		entraGraphNotFound(w, groupID)
		return
	}
	entraDropOwners(groupID)
	for _, m := range entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.GroupID == groupID || m.MemberID == groupID
	}) {
		entraGroupMembershipStore.Delete(entraGroupMemberKey(m.GroupID, m.MemberID))
	}
	w.WriteHeader(http.StatusNoContent)
}

func entraGroupMemberKey(groupID, memberID string) string { return groupID + "/" + memberID }

func entraAddGroupMember(groupID, memberID string) {
	if memberID == "" {
		return
	}
	entraGroupMembershipStore.Put(entraGroupMemberKey(groupID, memberID),
		entraGroupMembership{GroupID: groupID, MemberID: memberID})
}

func handleGraphAddGroupMemberRef(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	if _, ok := entraGraphGroupStore.Get(groupID); !ok {
		entraGraphNotFound(w, groupID)
		return
	}
	var req struct {
		ODataID string `json:"@odata.id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	memberID := graphRefObjectID(req.ODataID)
	if memberID == "" {
		sim.AzureError(w, "Request_BadRequest", "@odata.id is required", http.StatusBadRequest)
		return
	}
	entraAddGroupMember(groupID, memberID)
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphListGroupMembers(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	if _, ok := entraGraphGroupStore.Get(groupID); !ok {
		entraGraphNotFound(w, groupID)
		return
	}
	memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.GroupID == groupID
	})
	baseURL := azureAuthBaseURL(r)
	version := graphVersionPrefix(r)
	docs := make([]map[string]any, 0, len(memberships))
	for _, m := range memberships {
		doc, ok := entraDirectoryObjectDoc(r, m.MemberID)
		if !ok {
			// A membership recorded against an object the directory no longer
			// holds still names the object, exactly as Graph's reference
			// collection does before the reference is cleaned up.
			doc = map[string]any{
				"@odata.type": "#microsoft.graph.directoryObject",
				"id":          m.MemberID,
			}
		}
		doc["@odata.id"] = fmt.Sprintf("%s%s/directoryObjects/%s", baseURL, version, m.MemberID)
		docs = append(docs, doc)
	}
	graphCollection(w, r, "$metadata#directoryObjects", docs)
}

func handleGraphRemoveGroupMemberRef(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	memberID := sim.PathParam(r, "memberId")
	if !entraGroupMembershipStore.Delete(entraGroupMemberKey(groupID, memberID)) {
		entraGraphNotFound(w, memberID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphListGroupOwners(w http.ResponseWriter, r *http.Request) {
	handleGraphListOwners(w, r, sim.PathParam(r, "groupId"))
}

func handleGraphAddGroupOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphAddOwnerRef(w, r, sim.PathParam(r, "groupId"))
}

func handleGraphRemoveGroupOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphRemoveOwnerRef(w, r, sim.PathParam(r, "groupId"), sim.PathParam(r, "ownerId"))
}

// handleGraphGroupMemberOf answers the groups a group is itself a member of.
func handleGraphGroupMemberOf(w http.ResponseWriter, r *http.Request) {
	groupID := sim.PathParam(r, "groupId")
	if _, ok := entraGraphGroupStore.Get(groupID); !ok {
		entraGraphNotFound(w, groupID)
		return
	}
	memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.MemberID == groupID
	})
	docs := make([]map[string]any, 0, len(memberships))
	for _, m := range memberships {
		grp, ok := entraGraphGroupStore.Get(m.GroupID)
		if !ok {
			continue
		}
		doc := entraGroupDoc(r, grp)
		doc["@odata.type"] = "#microsoft.graph.group"
		docs = append(docs, doc)
	}
	graphCollection(w, r, "$metadata#directoryObjects", docs)
}

// ---------------------------------------------------------------------------
// users
// ---------------------------------------------------------------------------

// registerEntraUsers mounts the Microsoft Graph user surface. Real URL base:
// https://graph.microsoft.com/v1.0/users.
func registerEntraUsers(srv *sim.Server) {
	srv.HandleFunc("POST /v1.0/users", handleGraphCreateUser)
	srv.HandleFunc("POST /beta/users", handleGraphCreateUser)
	srv.HandleFunc("GET /v1.0/users", handleGraphListUsers)
	srv.HandleFunc("GET /beta/users", handleGraphListUsers)
	srv.HandleFunc("GET /v1.0/users/{userId}", handleGraphGetUser)
	srv.HandleFunc("GET /beta/users/{userId}", handleGraphGetUser)
	srv.HandleFunc("PATCH /v1.0/users/{userId}", handleGraphUpdateUser)
	srv.HandleFunc("PATCH /beta/users/{userId}", handleGraphUpdateUser)
	srv.HandleFunc("DELETE /v1.0/users/{userId}", handleGraphDeleteUser)
	srv.HandleFunc("DELETE /beta/users/{userId}", handleGraphDeleteUser)
	srv.HandleFunc("GET /v1.0/users/{userId}/memberOf", handleGraphUserMemberOf)
	srv.HandleFunc("GET /beta/users/{userId}/memberOf", handleGraphUserMemberOf)
}

// entraUserDoc renders a user in Graph's wire shape.
func entraUserDoc(r *http.Request, u EntraUser) map[string]any {
	return graphDoc(r, "users", map[string]any{
		"id":                u.OID,
		"displayName":       u.Name,
		"userPrincipalName": u.PreferredUsername,
		"mail":              u.Email,
	}, u.Props)
}

func handleGraphCreateUser(w http.ResponseWriter, r *http.Request) {
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
		Mail              string `json:"mail,omitempty"`
	}
	if err := graphDecodeProps(body.Props, &req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayName == "" || req.UserPrincipalName == "" {
		sim.AzureError(w, "Request_BadRequest", "displayName and userPrincipalName are required", http.StatusBadRequest)
		return
	}
	if _, exists := findEntraUserByUPN(req.UserPrincipalName); exists {
		sim.AzureError(w, "Request_BadRequest", fmt.Sprintf("Another object with the same value for property userPrincipalName already exists: %s", req.UserPrincipalName), http.StatusBadRequest)
		return
	}
	oid := newGraphID()
	user := EntraUser{
		OID:               oid,
		Sub:               oid,
		PreferredUsername: req.UserPrincipalName,
		Name:              req.DisplayName,
		Email:             req.Mail,
		Props:             graphMergeProps(nil, body.Props, entraUserServiceOwned),
	}
	entraUsersStore.Put(oid, user)
	doc := entraUserDoc(r, user)
	doc["@odata.context"] = "$metadata#users/$entity"
	sim.WriteJSON(w, http.StatusCreated, doc)
}

func handleGraphListUsers(w http.ResponseWriter, r *http.Request) {
	users := entraUsersStore.List()
	docs := make([]map[string]any, 0, len(users))
	for _, u := range users {
		docs = append(docs, entraUserDoc(r, u))
	}
	docs, ok := graphFilterDocs(w, r, docs)
	if !ok {
		return
	}
	graphCollection(w, r, "$metadata#users", docs)
}

func handleGraphGetUser(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	u, ok := entraLookupUser(userID)
	if !ok {
		entraGraphNotFound(w, userID)
		return
	}
	doc := entraUserDoc(r, u)
	doc["@odata.context"] = "$metadata#users/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

// handleGraphUpdateUser applies an incremental update. Real Graph PATCH only
// touches the members present in the body and returns 204 No Content.
func handleGraphUpdateUser(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	updated := entraUsersStore.Update(userID, func(u *EntraUser) {
		graphSetString(body.Props, "displayName", &u.Name)
		graphSetString(body.Props, "mail", &u.Email)
		graphSetString(body.Props, "userPrincipalName", &u.PreferredUsername)
		u.Props = graphMergeProps(u.Props, body.Props, entraUserServiceOwned)
	})
	if !updated {
		entraGraphNotFound(w, userID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	if !entraUsersStore.Delete(userID) {
		entraGraphNotFound(w, userID)
		return
	}
	entraUserManagerStore.Delete(userID)
	for _, m := range entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.MemberID == userID
	}) {
		entraGroupMembershipStore.Delete(entraGroupMemberKey(m.GroupID, m.MemberID))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGraphUserMemberOf answers the groups a named user belongs to. The /me
// spelling reads the same collection for the bearer's own object ID.
func handleGraphUserMemberOf(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	if _, ok := entraLookupUser(userID); !ok {
		entraGraphNotFound(w, userID)
		return
	}
	graphCollection(w, r, "$metadata#directoryObjects", entraMemberOfDocs(r, userID))
}

// ---------------------------------------------------------------------------
// applications
// ---------------------------------------------------------------------------

// registerEntraApplications mounts the Microsoft Graph application-registration
// surface. Real URL base: https://graph.microsoft.com/v1.0/applications
func registerEntraApplications(srv *sim.Server) {
	entraApplicationStore = sim.MakeStore[EntraApplication](srv.DB(), "entra_applications")

	srv.HandleFunc("POST /v1.0/applications", handleGraphCreateApplication)
	srv.HandleFunc("POST /beta/applications", handleGraphCreateApplication)
	srv.HandleFunc("GET /v1.0/applications", handleGraphListApplications)
	srv.HandleFunc("GET /beta/applications", handleGraphListApplications)
	srv.HandleFunc("GET /v1.0/applications/{appObjectId}", handleGraphGetApplication)
	srv.HandleFunc("GET /beta/applications/{appObjectId}", handleGraphGetApplication)
	srv.HandleFunc("PATCH /v1.0/applications/{appObjectId}", handleGraphUpdateApplication)
	srv.HandleFunc("PATCH /beta/applications/{appObjectId}", handleGraphUpdateApplication)
	srv.HandleFunc("DELETE /v1.0/applications/{appObjectId}", handleGraphDeleteApplication)
	srv.HandleFunc("DELETE /beta/applications/{appObjectId}", handleGraphDeleteApplication)

	// Client secrets live on the application object — the Certificates &
	// secrets blade of a real app registration calls exactly this route, and
	// the minted secret is what the v2.0 client_credentials grant validates.
	srv.HandleFunc("POST /v1.0/applications/{appObjectId}/addPassword", handleGraphApplicationAddPassword)
	srv.HandleFunc("POST /beta/applications/{appObjectId}/addPassword", handleGraphApplicationAddPassword)
	srv.HandleFunc("POST /v1.0/applications/{appObjectId}/removePassword", handleGraphApplicationRemovePassword)
	srv.HandleFunc("POST /beta/applications/{appObjectId}/removePassword", handleGraphApplicationRemovePassword)

	srv.HandleFunc("GET /v1.0/applications/{appObjectId}/owners", handleGraphListApplicationOwners)
	srv.HandleFunc("GET /beta/applications/{appObjectId}/owners", handleGraphListApplicationOwners)
	srv.HandleFunc("POST /v1.0/applications/{appObjectId}/owners/$ref", handleGraphAddApplicationOwnerRef)
	srv.HandleFunc("POST /beta/applications/{appObjectId}/owners/$ref", handleGraphAddApplicationOwnerRef)
	srv.HandleFunc("DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref", handleGraphRemoveApplicationOwnerRef)
	srv.HandleFunc("DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref", handleGraphRemoveApplicationOwnerRef)
}

// entraApplicationDoc renders an application registration in Graph's wire shape.
func entraApplicationDoc(r *http.Request, a EntraApplication) map[string]any {
	return graphDoc(r, "applications", map[string]any{
		"id":                  a.ID,
		"appId":               a.AppID,
		"displayName":         a.DisplayName,
		"signInAudience":      a.SignInAudience,
		"passwordCredentials": entraPasswordCredentialsJSON(a.PasswordCredentials),
	}, a.Props)
}

func handleGraphCreateApplication(w http.ResponseWriter, r *http.Request) {
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		DisplayName    string `json:"displayName"`
		SignInAudience string `json:"signInAudience"`
	}
	if err := graphDecodeProps(body.Props, &req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayName == "" {
		sim.AzureError(w, "Request_BadRequest", "displayName is required", http.StatusBadRequest)
		return
	}
	for _, ownerID := range body.Binds["owners"] {
		if _, ok := entraDirectoryObjectDoc(r, ownerID); !ok {
			entraGraphNotFound(w, ownerID)
			return
		}
	}
	app := EntraApplication{
		ID:             newGraphID(),
		AppID:          newGraphID(),
		DisplayName:    req.DisplayName,
		SignInAudience: req.SignInAudience,
		Props:          graphMergeProps(nil, body.Props, entraApplicationServiceOwned),
	}
	entraApplicationStore.Put(app.ID, app)
	entraAddOwners(app.ID, body.Binds["owners"])
	doc := entraApplicationDoc(r, app)
	doc["@odata.context"] = "$metadata#applications/$entity"
	sim.WriteJSON(w, http.StatusCreated, doc)
}

func handleGraphListApplications(w http.ResponseWriter, r *http.Request) {
	apps := entraApplicationStore.List()
	docs := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		docs = append(docs, entraApplicationDoc(r, a))
	}
	docs, ok := graphFilterDocs(w, r, docs)
	if !ok {
		return
	}
	graphCollection(w, r, "$metadata#applications", docs)
}

func handleGraphGetApplication(w http.ResponseWriter, r *http.Request) {
	app, ok := entraApplicationStore.Get(sim.PathParam(r, "appObjectId"))
	if !ok {
		entraGraphNotFound(w, sim.PathParam(r, "appObjectId"))
		return
	}
	doc := entraApplicationDoc(r, app)
	doc["@odata.context"] = "$metadata#applications/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

func handleGraphUpdateApplication(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "appObjectId")
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	updated := entraApplicationStore.Update(id, func(a *EntraApplication) {
		graphSetString(body.Props, "displayName", &a.DisplayName)
		graphSetString(body.Props, "signInAudience", &a.SignInAudience)
		a.Props = graphMergeProps(a.Props, body.Props, entraApplicationServiceOwned)
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	entraAddOwners(id, body.Binds["owners"])
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphDeleteApplication(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "appObjectId")
	if !entraApplicationStore.Delete(id) {
		entraGraphNotFound(w, id)
		return
	}
	entraDropOwners(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphApplicationAddPassword(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "appObjectId")
	cred, ok := entraParseAddPassword(w, r)
	if !ok {
		return
	}
	updated := entraApplicationStore.Update(id, func(a *EntraApplication) {
		a.PasswordCredentials = append(a.PasswordCredentials, entraStoredCredential(cred))
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, entraPasswordCredentialJSON(cred, true))
}

func handleGraphApplicationRemovePassword(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "appObjectId")
	keyID, ok := entraParseRemovePassword(w, r)
	if !ok {
		return
	}
	removed := false
	updated := entraApplicationStore.Update(id, func(a *EntraApplication) {
		a.PasswordCredentials, removed = entraDropCredential(a.PasswordCredentials, keyID)
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	if !removed {
		sim.AzureError(w, "Request_ResourceNotFound",
			fmt.Sprintf("No password credential found with keyId %s", keyID), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphListApplicationOwners(w http.ResponseWriter, r *http.Request) {
	handleGraphListOwners(w, r, sim.PathParam(r, "appObjectId"))
}

func handleGraphAddApplicationOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphAddOwnerRef(w, r, sim.PathParam(r, "appObjectId"))
}

func handleGraphRemoveApplicationOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphRemoveOwnerRef(w, r, sim.PathParam(r, "appObjectId"), sim.PathParam(r, "ownerId"))
}

// ---------------------------------------------------------------------------
// service principals
// ---------------------------------------------------------------------------

// registerEntraServicePrincipals mounts the Microsoft Graph service-principal
// surface. Real URL base: https://graph.microsoft.com/v1.0/servicePrincipals
func registerEntraServicePrincipals(srv *sim.Server) {
	entraServicePrincipalStore = sim.MakeStore[EntraServicePrincipal](srv.DB(), "entra_service_principals")

	srv.HandleFunc("POST /v1.0/servicePrincipals", handleGraphCreateServicePrincipal)
	srv.HandleFunc("POST /beta/servicePrincipals", handleGraphCreateServicePrincipal)
	srv.HandleFunc("GET /v1.0/servicePrincipals", handleGraphListServicePrincipals)
	srv.HandleFunc("GET /beta/servicePrincipals", handleGraphListServicePrincipals)
	srv.HandleFunc("GET /v1.0/servicePrincipals/{spId}", handleGraphGetServicePrincipal)
	srv.HandleFunc("GET /beta/servicePrincipals/{spId}", handleGraphGetServicePrincipal)
	srv.HandleFunc("PATCH /v1.0/servicePrincipals/{spId}", handleGraphUpdateServicePrincipal)
	srv.HandleFunc("PATCH /beta/servicePrincipals/{spId}", handleGraphUpdateServicePrincipal)
	srv.HandleFunc("DELETE /v1.0/servicePrincipals/{spId}", handleGraphDeleteServicePrincipal)
	srv.HandleFunc("DELETE /beta/servicePrincipals/{spId}", handleGraphDeleteServicePrincipal)

	srv.HandleFunc("POST /v1.0/servicePrincipals/{spId}/addPassword", handleGraphServicePrincipalAddPassword)
	srv.HandleFunc("POST /beta/servicePrincipals/{spId}/addPassword", handleGraphServicePrincipalAddPassword)
	srv.HandleFunc("POST /v1.0/servicePrincipals/{spId}/removePassword", handleGraphServicePrincipalRemovePassword)
	srv.HandleFunc("POST /beta/servicePrincipals/{spId}/removePassword", handleGraphServicePrincipalRemovePassword)

	srv.HandleFunc("GET /v1.0/servicePrincipals/{spId}/owners", handleGraphListServicePrincipalOwners)
	srv.HandleFunc("GET /beta/servicePrincipals/{spId}/owners", handleGraphListServicePrincipalOwners)
	srv.HandleFunc("POST /v1.0/servicePrincipals/{spId}/owners/$ref", handleGraphAddServicePrincipalOwnerRef)
	srv.HandleFunc("POST /beta/servicePrincipals/{spId}/owners/$ref", handleGraphAddServicePrincipalOwnerRef)
	srv.HandleFunc("DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref", handleGraphRemoveServicePrincipalOwnerRef)
	srv.HandleFunc("DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref", handleGraphRemoveServicePrincipalOwnerRef)
}

// entraServicePrincipalDoc renders a service principal in Graph's wire shape.
func entraServicePrincipalDoc(r *http.Request, sp EntraServicePrincipal) map[string]any {
	return graphDoc(r, "servicePrincipals", map[string]any{
		"id":                   sp.ID,
		"appId":                sp.AppID,
		"displayName":          sp.DisplayName,
		"servicePrincipalType": sp.ServicePrincipalType,
		"passwordCredentials":  entraPasswordCredentialsJSON(sp.PasswordCredentials),
	}, sp.Props)
}

func handleGraphCreateServicePrincipal(w http.ResponseWriter, r *http.Request) {
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		AppID       string `json:"appId"`
		DisplayName string `json:"displayName"`
	}
	if err := graphDecodeProps(body.Props, &req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.AppID == "" {
		sim.AzureError(w, "Request_BadRequest", "appId is required", http.StatusBadRequest)
		return
	}
	// Graph refuses a service principal whose appId names no application in
	// the tenant, and terraform-provider-azuread retries on exactly that
	// message while the application replicates.
	app, ok := entraFindApplicationByAppID(req.AppID)
	if !ok {
		sim.AzureError(w, "Request_BadRequest",
			fmt.Sprintf("The appId '%s' of the service principal does not reference a valid application object.", req.AppID),
			http.StatusBadRequest)
		return
	}
	displayName := req.DisplayName
	// Graph resolves displayName from the backing application when omitted.
	if displayName == "" {
		displayName = app.DisplayName
	}
	for _, ownerID := range body.Binds["owners"] {
		if _, ok := entraDirectoryObjectDoc(r, ownerID); !ok {
			entraGraphNotFound(w, ownerID)
			return
		}
	}
	sp := EntraServicePrincipal{
		ID:                   newGraphID(),
		AppID:                req.AppID,
		DisplayName:          displayName,
		ServicePrincipalType: "Application",
		Props:                graphMergeProps(nil, body.Props, entraServicePrincipalServiceOwned),
	}
	entraServicePrincipalStore.Put(sp.ID, sp)
	entraAddOwners(sp.ID, body.Binds["owners"])
	doc := entraServicePrincipalDoc(r, sp)
	doc["@odata.context"] = "$metadata#servicePrincipals/$entity"
	sim.WriteJSON(w, http.StatusCreated, doc)
}

func handleGraphListServicePrincipals(w http.ResponseWriter, r *http.Request) {
	sps := entraServicePrincipalStore.List()
	docs := make([]map[string]any, 0, len(sps))
	for _, sp := range sps {
		docs = append(docs, entraServicePrincipalDoc(r, sp))
	}
	docs, ok := graphFilterDocs(w, r, docs)
	if !ok {
		return
	}
	graphCollection(w, r, "$metadata#servicePrincipals", docs)
}

func handleGraphGetServicePrincipal(w http.ResponseWriter, r *http.Request) {
	sp, ok := entraServicePrincipalStore.Get(sim.PathParam(r, "spId"))
	if !ok {
		entraGraphNotFound(w, sim.PathParam(r, "spId"))
		return
	}
	doc := entraServicePrincipalDoc(r, sp)
	doc["@odata.context"] = "$metadata#servicePrincipals/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

func handleGraphUpdateServicePrincipal(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "spId")
	body, err := graphDecodeBody(r)
	if err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	updated := entraServicePrincipalStore.Update(id, func(sp *EntraServicePrincipal) {
		graphSetString(body.Props, "displayName", &sp.DisplayName)
		sp.Props = graphMergeProps(sp.Props, body.Props, entraServicePrincipalServiceOwned)
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	entraAddOwners(id, body.Binds["owners"])
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphDeleteServicePrincipal(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "spId")
	if !entraServicePrincipalStore.Delete(id) {
		entraGraphNotFound(w, id)
		return
	}
	entraDropOwners(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphServicePrincipalAddPassword(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "spId")
	cred, ok := entraParseAddPassword(w, r)
	if !ok {
		return
	}
	// Persist the credential without its secretText — Graph only returns
	// secretText on the addPassword response, never on later reads.
	updated := entraServicePrincipalStore.Update(id, func(sp *EntraServicePrincipal) {
		sp.PasswordCredentials = append(sp.PasswordCredentials, entraStoredCredential(cred))
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, entraPasswordCredentialJSON(cred, true))
}

func handleGraphServicePrincipalRemovePassword(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "spId")
	keyID, ok := entraParseRemovePassword(w, r)
	if !ok {
		return
	}
	removed := false
	updated := entraServicePrincipalStore.Update(id, func(sp *EntraServicePrincipal) {
		sp.PasswordCredentials, removed = entraDropCredential(sp.PasswordCredentials, keyID)
	})
	if !updated {
		entraGraphNotFound(w, id)
		return
	}
	if !removed {
		sim.AzureError(w, "Request_ResourceNotFound",
			fmt.Sprintf("No password credential found with keyId %s", keyID), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphListServicePrincipalOwners(w http.ResponseWriter, r *http.Request) {
	handleGraphListOwners(w, r, sim.PathParam(r, "spId"))
}

func handleGraphAddServicePrincipalOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphAddOwnerRef(w, r, sim.PathParam(r, "spId"))
}

func handleGraphRemoveServicePrincipalOwnerRef(w http.ResponseWriter, r *http.Request) {
	handleGraphRemoveOwnerRef(w, r, sim.PathParam(r, "spId"), sim.PathParam(r, "ownerId"))
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// graphDecodeProps re-encodes the entity's own properties and decodes them into
// the typed shape a handler models, so a handler reads the members it owns
// without a second pass over the request body.
func graphDecodeProps(props map[string]json.RawMessage, out any) error {
	encoded, err := json.Marshal(props)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

// graphSetString applies a PATCH member to a typed string field, leaving it
// untouched when the member is absent from the body.
func graphSetString(props map[string]json.RawMessage, key string, dst *string) {
	raw, ok := props[key]
	if !ok {
		return
	}
	var v string
	if json.Unmarshal(raw, &v) == nil {
		*dst = v
	}
}

// graphSetBool applies a PATCH member to a typed bool field.
func graphSetBool(props map[string]json.RawMessage, key string, dst *bool) {
	raw, ok := props[key]
	if !ok {
		return
	}
	var v bool
	if json.Unmarshal(raw, &v) == nil {
		*dst = v
	}
}

// entraParseAddPassword reads an addPassword request body and mints the new
// credential. addPassword accepts an empty body (EOF); a non-empty malformed
// body is a 400, not silently ignored. Real Graph honors a caller-supplied
// validity window and defaults the end date to two years out.
func entraParseAddPassword(w http.ResponseWriter, r *http.Request) (EntraPasswordCredential, bool) {
	var req struct {
		PasswordCredential struct {
			DisplayName   string `json:"displayName"`
			StartDateTime string `json:"startDateTime"`
			EndDateTime   string `json:"endDateTime"`
		} `json:"passwordCredential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		sim.AzureError(w, "Request_BadRequest", "invalid request body", http.StatusBadRequest)
		return EntraPasswordCredential{}, false
	}
	now := time.Now().UTC()
	start := req.PasswordCredential.StartDateTime
	if start == "" {
		start = now.Format(time.RFC3339)
	}
	end := req.PasswordCredential.EndDateTime
	if end == "" {
		end = now.AddDate(2, 0, 0).Format(time.RFC3339)
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	secretText := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(secretText))
	return EntraPasswordCredential{
		KeyID:         newGraphID(),
		DisplayName:   req.PasswordCredential.DisplayName,
		SecretText:    secretText,
		Hint:          secretText[:3],
		StartDateTime: start,
		EndDateTime:   end,
		SecretHash:    base64.RawStdEncoding.EncodeToString(hash[:]),
	}, true
}

// entraParseRemovePassword reads a removePassword request body ({"keyId": …}).
func entraParseRemovePassword(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		KeyID string `json:"keyId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.KeyID == "" {
		sim.AzureError(w, "Request_BadRequest", "keyId is required", http.StatusBadRequest)
		return "", false
	}
	return req.KeyID, true
}

// entraStoredCredential strips the secretText for persistence — the plaintext
// exists only in the addPassword response; validation uses the stored hash.
func entraStoredCredential(cred EntraPasswordCredential) EntraPasswordCredential {
	cred.SecretText = ""
	return cred
}

// entraDropCredential removes the credential with the given keyId, reporting
// whether it was present.
func entraDropCredential(creds []EntraPasswordCredential, keyID string) ([]EntraPasswordCredential, bool) {
	for i, c := range creds {
		if c.KeyID == keyID {
			return append(creds[:i:i], creds[i+1:]...), true
		}
	}
	return creds, false
}

// entraFindApplicationByAppID resolves an application registration by its
// appId (the OAuth2 client_id).
func entraFindApplicationByAppID(appID string) (EntraApplication, bool) {
	apps := entraApplicationStore.Filter(func(a EntraApplication) bool {
		return strings.EqualFold(a.AppID, appID)
	})
	if len(apps) == 0 {
		return EntraApplication{}, false
	}
	return apps[0], true
}

// entraFindServicePrincipalByAppID resolves the service principal that
// materializes an application in the tenant.
func entraFindServicePrincipalByAppID(appID string) (EntraServicePrincipal, bool) {
	sps := entraServicePrincipalStore.Filter(func(sp EntraServicePrincipal) bool {
		return strings.EqualFold(sp.AppID, appID)
	})
	if len(sps) == 0 {
		return EntraServicePrincipal{}, false
	}
	return sps[0], true
}

// entraClientSecretMatches reports whether the presented client_secret matches
// an unexpired password credential registered for the application — on the
// application object itself (the app-registration Certificates & secrets
// blade) or directly on one of its service principals, the same two credential
// sets real Microsoft Entra validates for the client_credentials grant.
func entraClientSecretMatches(app EntraApplication, secret string) bool {
	hash := sha256.Sum256([]byte(secret))
	presented := base64.RawStdEncoding.EncodeToString(hash[:])
	now := time.Now().UTC()
	valid := func(creds []EntraPasswordCredential) bool {
		for _, c := range creds {
			if c.SecretHash == "" || c.SecretHash != presented {
				continue
			}
			if end, err := time.Parse(time.RFC3339, c.EndDateTime); err == nil && now.After(end) {
				continue
			}
			return true
		}
		return false
	}
	if valid(app.PasswordCredentials) {
		return true
	}
	sps := entraServicePrincipalStore.Filter(func(sp EntraServicePrincipal) bool {
		return strings.EqualFold(sp.AppID, app.AppID)
	})
	for _, sp := range sps {
		if valid(sp.PasswordCredentials) {
			return true
		}
	}
	return false
}

// entraPasswordCredentialJSON emits a password credential in Graph's wire
// shape. secretText is included only on the addPassword response
// (withSecret=true); every later read serves it as null, exactly as real
// Microsoft Graph does.
func entraPasswordCredentialJSON(c EntraPasswordCredential, withSecret bool) map[string]any {
	var secret any
	if withSecret {
		secret = c.SecretText
	}
	return map[string]any{
		"keyId":               c.KeyID,
		"customKeyIdentifier": nil,
		"displayName":         c.DisplayName,
		"hint":                c.Hint,
		"secretText":          secret,
		"startDateTime":       c.StartDateTime,
		"endDateTime":         c.EndDateTime,
	}
}

func entraPasswordCredentialsJSON(creds []EntraPasswordCredential) []map[string]any {
	out := make([]map[string]any, 0, len(creds))
	for _, c := range creds {
		out = append(out, entraPasswordCredentialJSON(c, false))
	}
	return out
}

// entraMemberOfDocs renders the groups one directory object belongs to.
func entraMemberOfDocs(r *http.Request, objectID string) []map[string]any {
	memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.MemberID == objectID
	})
	baseURL := azureAuthBaseURL(r)
	version := graphVersionPrefix(r)
	docs := make([]map[string]any, 0, len(memberships))
	seen := map[string]bool{}
	for _, m := range memberships {
		if seen[m.GroupID] {
			continue
		}
		seen[m.GroupID] = true
		grp, ok := entraGraphGroupStore.Get(m.GroupID)
		if !ok {
			continue
		}
		doc := entraGroupDoc(r, grp)
		doc["@odata.type"] = "#microsoft.graph.group"
		doc["@odata.id"] = fmt.Sprintf("%s%s/directoryObjects/%s", baseURL, version, grp.ID)
		docs = append(docs, doc)
	}
	return docs
}

func handleGraphMemberOf(w http.ResponseWriter, r *http.Request) {
	// Real Microsoft Graph resolves /me from the bearer token's oid claim and
	// answers a tokenless request with 401 InvalidAuthenticationToken — there
	// is no anonymous /me.
	oid, ok := parseOIDFromBearer(r)
	if !ok {
		sim.AzureError(w, "InvalidAuthenticationToken",
			"Access token is empty or carries no oid claim.", http.StatusUnauthorized)
		return
	}
	graphCollection(w, r, "$metadata#directoryObjects", entraMemberOfDocs(r, oid))
}
