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
// user's oid/sub, and group claims resolve through the membership store.
type EntraUser struct {
	OID               string `json:"oid"`
	Sub               string `json:"sub"`
	PreferredUsername string `json:"preferredUsername"`
	Name              string `json:"name"`
	Email             string `json:"email,omitempty"`
}

// EntraGraphGroup is a standalone group created via POST /v1.0/groups.
type EntraGraphGroup struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	MailNickname    string `json:"mailNickname"`
	SecurityEnabled bool   `json:"securityEnabled"`
	MailEnabled     bool   `json:"mailEnabled"`
}

// entraGroupMembership records one user being a member of one group.
type entraGroupMembership struct {
	GroupID string
	UserID  string
}

// EntraApplication is an Entra (Azure AD) application registration created via
// POST /v1.0/applications. The application's appId is the client identifier a
// service principal references. PasswordCredentials are the client secrets the
// v2.0 token endpoint validates for the client_credentials grant — exactly the
// role app-registration secrets play on real Microsoft Entra.
type EntraApplication struct {
	ID                  string                    `json:"id"`
	AppID               string                    `json:"appId"`
	DisplayName         string                    `json:"displayName"`
	SignInAudience      string                    `json:"signInAudience,omitempty"`
	PasswordCredentials []EntraPasswordCredential `json:"passwordCredentials,omitempty"`
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

	// Microsoft Graph group management — standard provisioning surface.
	// Real URL base: https://graph.microsoft.com/v1.0
	srv.HandleFunc("POST /v1.0/groups", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DisplayName     string `json:"displayName"`
			MailNickname    string `json:"mailNickname"`
			Description     string `json:"description,omitempty"`
			SecurityEnabled bool   `json:"securityEnabled"`
			MailEnabled     bool   `json:"mailEnabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		}
		entraGraphGroupStore.Put(grp.ID, grp)
		sim.WriteJSON(w, http.StatusCreated, entraGraphGroupJSON(grp))
	})

	srv.HandleFunc("GET /v1.0/groups/{groupId}", func(w http.ResponseWriter, r *http.Request) {
		groupID := sim.PathParam(r, "groupId")
		grp, ok := entraGraphGroupStore.Get(groupID)
		if !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "group not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraGraphGroupJSON(grp))
	})

	srv.HandleFunc("DELETE /v1.0/groups/{groupId}", func(w http.ResponseWriter, r *http.Request) {
		groupID := sim.PathParam(r, "groupId")
		if !entraGraphGroupStore.Delete(groupID) {
			sim.AzureError(w, "Request_ResourceNotFound", "group not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Group membership management.
	srv.HandleFunc("POST /v1.0/groups/{groupId}/members/$ref", func(w http.ResponseWriter, r *http.Request) {
		groupID := sim.PathParam(r, "groupId")
		if _, ok := entraGraphGroupStore.Get(groupID); !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "group not found", http.StatusNotFound)
			return
		}
		var req struct {
			ODataID string `json:"@odata.id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		// Extract user ID from the @odata.id URL (final path segment).
		parts := strings.Split(strings.TrimRight(req.ODataID, "/"), "/")
		userID := parts[len(parts)-1]
		key := groupID + "/" + userID
		entraGroupMembershipStore.Put(key, entraGroupMembership{GroupID: groupID, UserID: userID})
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET /v1.0/groups/{groupId}/members", func(w http.ResponseWriter, r *http.Request) {
		groupID := sim.PathParam(r, "groupId")
		if _, ok := entraGraphGroupStore.Get(groupID); !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "group not found", http.StatusNotFound)
			return
		}
		memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
			return m.GroupID == groupID
		})
		baseURL := azureAuthBaseURL(r)
		values := make([]map[string]any, 0, len(memberships))
		for _, m := range memberships {
			u := getEntraSimUser(m.UserID)
			values = append(values, map[string]any{
				"@odata.type":       "#microsoft.graph.user",
				"@odata.id":         fmt.Sprintf("%s/v1.0/directoryObjects/%s", baseURL, m.UserID),
				"id":                m.UserID,
				"displayName":       u.Name,
				"userPrincipalName": u.PreferredUsername,
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"@odata.context": "$metadata#directoryObjects",
			"value":          values,
		})
	})

	srv.HandleFunc("DELETE /v1.0/groups/{groupId}/members/{userId}/$ref", func(w http.ResponseWriter, r *http.Request) {
		groupID := sim.PathParam(r, "groupId")
		userID := sim.PathParam(r, "userId")
		key := groupID + "/" + userID
		if !entraGroupMembershipStore.Delete(key) {
			sim.AzureError(w, "Request_ResourceNotFound", "membership not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Microsoft Graph user management.
	srv.HandleFunc("POST /v1.0/users", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DisplayName       string `json:"displayName"`
			UserPrincipalName string `json:"userPrincipalName"`
			MailNickname      string `json:"mailNickname"`
			Mail              string `json:"mail,omitempty"`
			AccountEnabled    bool   `json:"accountEnabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		}
		entraUsersStore.Put(oid, user)
		sim.WriteJSON(w, http.StatusCreated, entraGraphUserJSON(oid, req.DisplayName, req.UserPrincipalName, req.Mail, req.AccountEnabled))
	})

	srv.HandleFunc("GET /v1.0/users/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := sim.PathParam(r, "userId")
		u, ok := entraUsersStore.Get(userID)
		if !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "user not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraGraphUserJSON(u.OID, u.Name, u.PreferredUsername, u.Email, true))
	})

	// PATCH applies an incremental update to a user. Real Graph PATCH only
	// touches the fields present in the body and returns 204 No Content.
	srv.HandleFunc("PATCH /v1.0/users/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := sim.PathParam(r, "userId")
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		updated := entraUsersStore.Update(userID, func(u *EntraUser) {
			if raw, ok := req["displayName"]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					u.Name = v
				}
			}
			if raw, ok := req["mail"]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					u.Email = v
				}
			}
		})
		if !updated {
			sim.AzureError(w, "Request_ResourceNotFound", "user not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("DELETE /v1.0/users/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := sim.PathParam(r, "userId")
		if !entraUsersStore.Delete(userID) {
			sim.AzureError(w, "Request_ResourceNotFound", "user not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	registerEntraApplications(srv)
	registerEntraServicePrincipals(srv)
	entraSeedBootstrap()

	// Microsoft Graph delegated read endpoints.
	// Real URL: https://graph.microsoft.com/v1.0/me/memberOf
	// The sim is configured as the graph endpoint in metadata.go, so requests
	// hit this process. The oid from the bearer token identifies the user whose
	// group memberships are read from the membership store.
	srv.HandleFunc("GET /v1.0/me/memberOf", handleGraphMemberOf)
	srv.HandleFunc("GET /v1.0/me/transitiveMemberOf", handleGraphMemberOf)
}

// registerEntraApplications mounts the Microsoft Graph application-registration
// CRUD surface. Real URL base: https://graph.microsoft.com/v1.0/applications
func registerEntraApplications(srv *sim.Server) {
	entraApplicationStore = sim.MakeStore[EntraApplication](srv.DB(), "entra_applications")

	srv.HandleFunc("POST /v1.0/applications", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DisplayName    string `json:"displayName"`
			SignInAudience string `json:"signInAudience"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		if req.DisplayName == "" {
			sim.AzureError(w, "Request_BadRequest", "displayName is required", http.StatusBadRequest)
			return
		}
		app := EntraApplication{
			ID:             newGraphID(),
			AppID:          newGraphID(),
			DisplayName:    req.DisplayName,
			SignInAudience: req.SignInAudience,
		}
		entraApplicationStore.Put(app.ID, app)
		sim.WriteJSON(w, http.StatusCreated, entraApplicationJSON(app))
	})

	srv.HandleFunc("GET /v1.0/applications", func(w http.ResponseWriter, r *http.Request) {
		apps := entraApplicationStore.List()
		values := make([]map[string]any, 0, len(apps))
		for _, a := range apps {
			values = append(values, entraApplicationJSON(a))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"@odata.context": "$metadata#applications",
			"value":          values,
		})
	})

	srv.HandleFunc("GET /v1.0/applications/{appObjectId}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := entraApplicationStore.Get(sim.PathParam(r, "appObjectId"))
		if !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "application not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraApplicationJSON(app))
	})

	srv.HandleFunc("PATCH /v1.0/applications/{appObjectId}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "appObjectId")
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		updated := entraApplicationStore.Update(id, func(a *EntraApplication) {
			if raw, ok := req["displayName"]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					a.DisplayName = v
				}
			}
			if raw, ok := req["signInAudience"]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					a.SignInAudience = v
				}
			}
		})
		if !updated {
			sim.AzureError(w, "Request_ResourceNotFound", "application not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("DELETE /v1.0/applications/{appObjectId}", func(w http.ResponseWriter, r *http.Request) {
		if !entraApplicationStore.Delete(sim.PathParam(r, "appObjectId")) {
			sim.AzureError(w, "Request_ResourceNotFound", "application not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Client secrets live on the application object — the Certificates &
	// secrets blade of a real app registration calls exactly this route, and
	// the minted secret is what the v2.0 client_credentials grant validates.
	srv.HandleFunc("POST /v1.0/applications/{appObjectId}/addPassword", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "appObjectId")
		cred, ok := entraParseAddPassword(w, r)
		if !ok {
			return
		}
		updated := entraApplicationStore.Update(id, func(a *EntraApplication) {
			a.PasswordCredentials = append(a.PasswordCredentials, entraStoredCredential(cred))
		})
		if !updated {
			sim.AzureError(w, "Request_ResourceNotFound", "application not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraPasswordCredentialJSON(cred, true))
	})

	srv.HandleFunc("POST /v1.0/applications/{appObjectId}/removePassword", func(w http.ResponseWriter, r *http.Request) {
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
			sim.AzureError(w, "Request_ResourceNotFound", "application not found", http.StatusNotFound)
			return
		}
		if !removed {
			sim.AzureError(w, "Request_ResourceNotFound",
				fmt.Sprintf("No password credential found with keyId %s", keyID), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// registerEntraServicePrincipals mounts the Microsoft Graph service-principal
// CRUD + addPassword surface. Real URL base:
// https://graph.microsoft.com/v1.0/servicePrincipals
func registerEntraServicePrincipals(srv *sim.Server) {
	entraServicePrincipalStore = sim.MakeStore[EntraServicePrincipal](srv.DB(), "entra_service_principals")

	srv.HandleFunc("POST /v1.0/servicePrincipals", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AppID       string `json:"appId"`
			DisplayName string `json:"displayName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		if req.AppID == "" {
			sim.AzureError(w, "Request_BadRequest", "appId is required", http.StatusBadRequest)
			return
		}
		displayName := req.DisplayName
		// Graph resolves displayName from the backing application when omitted.
		if displayName == "" {
			apps := entraApplicationStore.Filter(func(a EntraApplication) bool {
				return strings.EqualFold(a.AppID, req.AppID)
			})
			if len(apps) > 0 {
				displayName = apps[0].DisplayName
			}
		}
		sp := EntraServicePrincipal{
			ID:                   newGraphID(),
			AppID:                req.AppID,
			DisplayName:          displayName,
			ServicePrincipalType: "Application",
		}
		entraServicePrincipalStore.Put(sp.ID, sp)
		sim.WriteJSON(w, http.StatusCreated, entraServicePrincipalJSON(sp))
	})

	srv.HandleFunc("GET /v1.0/servicePrincipals", func(w http.ResponseWriter, r *http.Request) {
		appIDFilter := parseGraphEqFilter(r.URL.Query().Get("$filter"), "appId")
		var sps []EntraServicePrincipal
		if appIDFilter != "" {
			sps = entraServicePrincipalStore.Filter(func(sp EntraServicePrincipal) bool {
				return strings.EqualFold(sp.AppID, appIDFilter)
			})
		} else {
			sps = entraServicePrincipalStore.List()
		}
		values := make([]map[string]any, 0, len(sps))
		for _, sp := range sps {
			values = append(values, entraServicePrincipalJSON(sp))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"@odata.context": "$metadata#servicePrincipals",
			"value":          values,
		})
	})

	srv.HandleFunc("GET /v1.0/servicePrincipals/{spId}", func(w http.ResponseWriter, r *http.Request) {
		sp, ok := entraServicePrincipalStore.Get(sim.PathParam(r, "spId"))
		if !ok {
			sim.AzureError(w, "Request_ResourceNotFound", "service principal not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraServicePrincipalJSON(sp))
	})

	srv.HandleFunc("PATCH /v1.0/servicePrincipals/{spId}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "spId")
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		updated := entraServicePrincipalStore.Update(id, func(sp *EntraServicePrincipal) {
			if raw, ok := req["displayName"]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					sp.DisplayName = v
				}
			}
		})
		if !updated {
			sim.AzureError(w, "Request_ResourceNotFound", "service principal not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("DELETE /v1.0/servicePrincipals/{spId}", func(w http.ResponseWriter, r *http.Request) {
		if !entraServicePrincipalStore.Delete(sim.PathParam(r, "spId")) {
			sim.AzureError(w, "Request_ResourceNotFound", "service principal not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("POST /v1.0/servicePrincipals/{spId}/addPassword", func(w http.ResponseWriter, r *http.Request) {
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
			sim.AzureError(w, "Request_ResourceNotFound", "service principal not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, entraPasswordCredentialJSON(cred, true))
	})

	srv.HandleFunc("POST /v1.0/servicePrincipals/{spId}/removePassword", func(w http.ResponseWriter, r *http.Request) {
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
			sim.AzureError(w, "Request_ResourceNotFound", "service principal not found", http.StatusNotFound)
			return
		}
		if !removed {
			sim.AzureError(w, "Request_ResourceNotFound",
				fmt.Sprintf("No password credential found with keyId %s", keyID), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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

// parseGraphEqFilter extracts the value from a Graph OData filter of the form
// "<field> eq 'value'". Returns "" when the filter doesn't target field.
func parseGraphEqFilter(filter, field string) string {
	if !strings.Contains(strings.ToLower(filter), strings.ToLower(field)) {
		return ""
	}
	if idx := strings.Index(filter, "'"); idx >= 0 {
		if end := strings.IndexByte(filter[idx+1:], '\''); end >= 0 {
			return filter[idx+1 : idx+1+end]
		}
	}
	return ""
}

func entraApplicationJSON(a EntraApplication) map[string]any {
	return map[string]any{
		"@odata.context":      "$metadata#applications/$entity",
		"id":                  a.ID,
		"appId":               a.AppID,
		"displayName":         a.DisplayName,
		"signInAudience":      a.SignInAudience,
		"passwordCredentials": entraPasswordCredentialsJSON(a.PasswordCredentials),
	}
}

func entraServicePrincipalJSON(sp EntraServicePrincipal) map[string]any {
	return map[string]any{
		"@odata.context":       "$metadata#servicePrincipals/$entity",
		"id":                   sp.ID,
		"appId":                sp.AppID,
		"displayName":          sp.DisplayName,
		"servicePrincipalType": sp.ServicePrincipalType,
		"passwordCredentials":  entraPasswordCredentialsJSON(sp.PasswordCredentials),
	}
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

	baseURL := azureAuthBaseURL(r)
	values := []map[string]any{}

	memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.UserID == oid
	})
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
		values = append(values, map[string]any{
			"@odata.type": "#microsoft.graph.group",
			"@odata.id":   fmt.Sprintf("%s/v1.0/directoryObjects/%s", baseURL, grp.ID),
			"id":          grp.ID,
			"displayName": grp.DisplayName,
		})
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"@odata.context": "$metadata#directoryObjects",
		"value":          values,
	})
}

func entraGraphGroupJSON(grp EntraGraphGroup) map[string]any {
	return map[string]any{
		"@odata.context":  "$metadata#groups/$entity",
		"id":              grp.ID,
		"displayName":     grp.DisplayName,
		"description":     grp.Description,
		"mailNickname":    grp.MailNickname,
		"securityEnabled": grp.SecurityEnabled,
		"mailEnabled":     grp.MailEnabled,
	}
}

func entraGraphUserJSON(id, displayName, userPrincipalName, mail string, accountEnabled bool) map[string]any {
	return map[string]any{
		"@odata.context":    "$metadata#users/$entity",
		"id":                id,
		"displayName":       displayName,
		"userPrincipalName": userPrincipalName,
		"mail":              mail,
		"accountEnabled":    accountEnabled,
	}
}
