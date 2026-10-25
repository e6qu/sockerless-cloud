package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerIAMServiceAccounts mounts the service account resource and its
// custom methods.
func registerIAMServiceAccounts(srv *sim.Server, serviceAccounts sim.Store[GCPServiceAccount], saKeys sim.Store[GCPServiceAccountKey], saKeyPublics sim.Store[GCPServiceAccountKeyMaterial], resourcePolicies sim.Store[IAMPolicy]) {
	// Create service account
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")

		var req struct {
			AccountId      string `json:"accountId"`
			ServiceAccount struct {
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"serviceAccount"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		// The org policy the constraint governs is resolved through the
		// project's place in the resource hierarchy, so a policy set on the
		// organization or an intervening folder reaches the project too.
		if crmOrgPolicyBooleanEnforced("projects/"+project, crmConstraintDisableServiceAccountCreation) {
			GCPError(w, http.StatusBadRequest,
				"Service account creation is not allowed on this project.", "FAILED_PRECONDITION")
			return
		}

		email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", req.AccountId, project)
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		if _, exists := serviceAccounts.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"Service account %s already exists within project projects/%s.", req.AccountId, project)
			return
		}

		sa := GCPServiceAccount{
			Name:        name,
			ProjectId:   project,
			UniqueId:    gcpNumericID(21),
			Email:       email,
			DisplayName: req.ServiceAccount.DisplayName,
			Description: req.ServiceAccount.Description,
		}
		serviceAccounts.Put(name, sa)

		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Get service account
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		sa, ok := serviceAccounts.Get(name)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Update service account — PATCH with an updateMask over the mutable
	// fields (displayName / description). Real GCP's UpdateServiceAccount
	// wraps the account under a `serviceAccount` envelope alongside the mask.
	srv.HandleFunc("PATCH /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		var req struct {
			ServiceAccount struct {
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"serviceAccount"`
			UpdateMask string `json:"updateMask"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The mask also rides as a query param in some client paths; prefer
		// the body mask, fall back to the query.
		mask := req.UpdateMask
		if mask == "" {
			mask = r.URL.Query().Get("updateMask")
		}
		for _, field := range strings.Split(mask, ",") {
			switch strings.TrimSpace(field) {
			case "displayName":
				sa.DisplayName = req.ServiceAccount.DisplayName
			case "description":
				sa.Description = req.ServiceAccount.Description
			}
		}
		serviceAccounts.Put(name, sa)
		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Update service account (legacy full-replace) — PUT carries the whole
	// ServiceAccount resource (no updateMask). Real GCP's
	// serviceAccounts.update replaces the mutable fields (displayName /
	// description) from the request body.
	srv.HandleFunc("PUT /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		var req struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		sa.DisplayName = req.DisplayName
		sa.Description = req.Description
		serviceAccounts.Put(name, sa)
		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Delete service account. IAM keeps the account for its retention window
	// under its unique ID, and its keys, system-managed key and IAM policy
	// leave the live stores with it: none authenticates or answers while it is
	// deleted, and a later account created under the same email starts with
	// none of them.
	srv.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		sa, ok := serviceAccounts.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		now := time.Now().UTC()
		iamPurgeExpiredServiceAccounts(now)
		deleted := gcpDeletedServiceAccount{Account: sa, DeleteTime: now}
		if system, ok := iamSASystemKeys.Get(name); ok {
			deleted.SystemKey = &system
			iamSASystemKeys.Delete(name)
		}
		keyPrefix := name + "/keys/"
		for _, key := range saKeys.Filter(func(k GCPServiceAccountKey) bool {
			return strings.HasPrefix(k.Name, keyPrefix)
		}) {
			deleted.Keys = append(deleted.Keys, key)
			if material, ok := saKeyPublics.Get(key.Name); ok {
				deleted.KeyPublics = append(deleted.KeyPublics, material)
			}
			saKeys.Delete(key.Name)
			saKeyPublics.Delete(key.Name)
		}
		if policy, ok := resourcePolicies.Get("serviceAccount/" + email); ok {
			deleted.Policy = &policy
			resourcePolicies.Delete("serviceAccount/" + email)
		}
		iamDeletedServiceAccounts.Put(sa.UniqueId, deleted)
		serviceAccounts.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// IAM Credentials API — short-lived tokens minted on behalf of a
	// service account. Real GCP paths:
	//   POST /v1/projects/{p}/serviceAccounts/{email}:generateAccessToken
	//   POST /v1/projects/{p}/serviceAccounts/{email}:generateIdToken
	// Sockerless runner setup (gcloud auth application-default,
	// google-github-actions/auth) calls generateAccessToken to mint
	// scoped tokens against the workload-identity-federated SA. The
	// Access driver's `id-token` category calls generateIdToken for
	// cross-Service impersonation flows where the runner SA mints an
	// ID token for a different audience SA. The minted tokens are signed
	// with the simulator's access-token key and verified by the data-plane
	// bearer middleware; the impersonation authorization itself (real GCP's
	// iam.serviceAccounts.getAccessToken permission check) is not modeled —
	// the caller is already an authenticated bearer.
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{emailAction}", func(w http.ResponseWriter, r *http.Request) {
		account, action, _ := strings.Cut(sim.PathParam(r, "emailAction"), ":")
		if action == "undelete" {
			iamUndeleteServiceAccount(w, serviceAccounts, saKeys, saKeyPublics, resourcePolicies, sim.PathParam(r, "project"), account)
			return
		}
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), account)
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		if _, ok := serviceAccounts.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		switch action {
		case "disable":
			sa, _ := serviceAccounts.Get(name)
			sa.Disabled = true
			serviceAccounts.Put(name, sa)
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
			return
		case "enable":
			sa, _ := serviceAccounts.Get(name)
			sa.Disabled = false
			serviceAccounts.Put(name, sa)
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
			return
		case "getIamPolicy", "setIamPolicy", "testIamPermissions":
			// The service account is itself a resource that carries an IAM
			// policy (e.g. granting roles/iam.serviceAccountUser to a member).
			// Reuse the shared resource-IAM store so the etag / member-
			// validation / optimistic-concurrency behavior matches buckets
			// and projects.
			handleResourceIAM(w, r, resourcePolicies, "serviceAccount/"+email, action)
			return
		}
		switch action {
		case "generateAccessToken":
			// Body: { scope, lifetime, delegates }. Response:
			// { accessToken, expireTime }.
			//
			// The token is signed with the simulator's access-token key (see
			// signAccessToken) so the data-plane bearer middleware accepts it,
			// naming the impersonated service account as its subject.
			// expireTime is a google.protobuf.Timestamp in whole seconds:
			// google-auth, behind gcloud's impersonation, parses it with
			// "%Y-%m-%dT%H:%M:%SZ" and rejects a fractional second.
			var req struct {
				Scope     []string `json:"scope"`
				Lifetime  string   `json:"lifetime"`
				Delegates []string `json:"delegates"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			lifetime, refusal := iamAccessTokenLifetime(req.Lifetime, email)
			if refusal != "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", refusal)
				return
			}
			now := time.Now()
			expires := now.Add(lifetime)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"accessToken": signAccessToken(email, now, expires),
				"expireTime":  protoJSONTimestamp(expires.Truncate(time.Second)),
			})
		case "generateIdToken":
			// Body: { audience, includeEmail, delegates }. Response: { token }.
			// Mint a real-shape RS256 JWT whose `aud` claim equals the
			// request's audience so SDKs that pre-decode the token (rare in
			// test paths, common in cross-Service auth chains) accept it, and
			// so its signature verifies against the simulator's published JWKS
			// exactly as a real Google ID token verifies against Google's.
			var req struct {
				Audience     string   `json:"audience"`
				IncludeEmail bool     `json:"includeEmail"`
				Delegates    []string `json:"delegates"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.Audience == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "audience is required")
				return
			}
			now := time.Now()
			expires := now.Add(1 * time.Hour)
			token := signServiceAccountIDToken(email, req.Audience, req.IncludeEmail, now, expires)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"token": token,
			})
		case "signBlob":
			// iamcredentials.serviceAccounts.signBlob — sign an opaque,
			// base64-encoded payload with the service account's system-managed
			// key. Body: { payload (base64), delegates }. Response:
			// { keyId, signedBlob (base64) }. The signature is RSASSA-PKCS1-v1_5
			// over SHA-256 of the decoded payload, produced with the account's
			// system-managed key, and keyId names the key the IAM keys surface
			// publishes the verifying public half under.
			var req struct {
				Payload   string   `json:"payload"`
				Delegates []string `json:"delegates"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(req.Payload)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "payload must be base64: %v", err)
				return
			}
			material, err := serviceAccountSigningKey(name, email)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			digest := sha256.Sum256(raw)
			sig, err := rsa.SignPKCS1v15(rand.Reader, material.key, crypto.SHA256, digest[:])
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "sign blob: %v", err)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"keyId":      material.keyID,
				"signedBlob": base64.StdEncoding.EncodeToString(sig),
			})
		case "signJwt":
			// iamcredentials.serviceAccounts.signJwt — sign a JWT claim set
			// (the request payload is the JSON claims string) with the SA's
			// system-managed key. Response: { keyId, signedJwt }. The result is
			// an RS256 JWS whose signature verifies against the public half the
			// IAM keys surface publishes under the reported keyId.
			var req struct {
				Payload   string   `json:"payload"`
				Delegates []string `json:"delegates"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			material, err := serviceAccountSigningKey(name, email)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			signed, err := material.jwt.SignPayload([]byte(req.Payload))
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "sign JWT: %v", err)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"keyId":     material.keyID,
				"signedJwt": signed,
			})
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported service-account action %q", action)
		}
	})

	// List service accounts
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/serviceAccounts/", project)

		accounts := serviceAccounts.Filter(func(sa GCPServiceAccount) bool {
			return strings.HasPrefix(sa.Name, prefix)
		})
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
		page, next, ok := paginateList(w, r, accounts, gcpAsc(func(v GCPServiceAccount) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"accounts": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
}

// iamServiceAccountRef resolves the project and account segments of a
// service-account resource name, where the account is an email or a unique ID
// and the project may be the - wildcard, to the account's project and email.
// An account that resolves to nothing keeps the segments as given.
func iamServiceAccountRef(accounts sim.Store[GCPServiceAccount], project, account string) (string, string) {
	if strings.Contains(account, "@") {
		if project == "-" {
			project = gcpProjectFromEmail(account)
		}
		return project, account
	}
	for _, sa := range accounts.Filter(func(sa GCPServiceAccount) bool {
		return sa.UniqueId == account && (project == "-" || sa.ProjectId == project)
	}) {
		return sa.ProjectId, sa.Email
	}
	return project, account
}

// iamPurgeExpiredServiceAccounts removes permanently every deleted account
// whose retention window has lapsed by now, with the keys and policy it held.
func iamPurgeExpiredServiceAccounts(now time.Time) {
	iamDeletedServiceAccounts.Prune(func(d gcpDeletedServiceAccount) bool {
		return !now.Before(d.DeleteTime.Add(iamServiceAccountRetention))
	})
}

// iamUndeleteServiceAccount serves projects.serviceAccounts.undelete. IAM
// finds the deleted account by its unique ID, the identifier gcloud requires,
// and restores it under the same unique ID while its retention window lasts
// and no live account has taken its email. Through the - wildcard a missing
// account answers PERMISSION_DENIED, as the API reference warns.
func iamUndeleteServiceAccount(w http.ResponseWriter, accounts sim.Store[GCPServiceAccount], keys sim.Store[GCPServiceAccountKey], keyPublics sim.Store[GCPServiceAccountKeyMaterial], policies sim.Store[IAMPolicy], project, account string) {
	iamPurgeExpiredServiceAccounts(time.Now().UTC())
	deleted, ok := iamDeletedServiceAccounts.Get(account)
	if !ok || (project != "-" && deleted.Account.ProjectId != project) {
		liveProject, email := iamServiceAccountRef(accounts, project, account)
		if _, live := accounts.Get(fmt.Sprintf("projects/%s/serviceAccounts/%s", liveProject, email)); live {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Service account %s is not deleted.", account)
			return
		}
		if project == "-" {
			GCPError(w, http.StatusForbidden,
				"Permission 'iam.serviceAccounts.undelete' denied on resource (or it may not exist).", "PERMISSION_DENIED")
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", account)
		return
	}
	sa := deleted.Account
	if _, taken := accounts.Get(sa.Name); taken {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"Service account %s cannot be restored: an existing service account has the same email.", sa.Email)
		return
	}
	accounts.Put(sa.Name, sa)
	for _, key := range deleted.Keys {
		keys.Put(key.Name, key)
	}
	for _, material := range deleted.KeyPublics {
		keyPublics.Put(material.Name, material)
	}
	if deleted.SystemKey != nil {
		iamSASystemKeys.Put(sa.Name, *deleted.SystemKey)
	}
	if deleted.Policy != nil {
		policies.Put("serviceAccount/"+sa.Email, *deleted.Policy)
	}
	iamDeletedServiceAccounts.Delete(sa.UniqueId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"restoredAccount": sa})
}

// iamAccessTokenLifetime resolves how long a generateAccessToken request's
// token lives. An absent lifetime takes the documented one-hour default; a
// present one is honoured up to the ceiling in force for the service account,
// which is one hour unless an Organization Policy enforcing
// constraints/iam.allowServiceAccountCredentialLifetimeExtension lists it, and
// twelve hours when one does.
//
// A lifetime the account may not have is reported through the second return,
// which is the INVALID_ARGUMENT message the caller receives — a wire string
// rather than a Go error, so it reads the way the API's own messages read.
func iamAccessTokenLifetime(requested, email string) (time.Duration, string) {
	if strings.TrimSpace(requested) == "" {
		return iamAccessTokenDefaultLifetime, ""
	}
	lifetime, err := parseGoogleDuration(requested)
	if err != nil {
		return 0, fmt.Sprintf("Invalid value at 'lifetime' (type.googleapis.com/google.protobuf.Duration), %q", requested)
	}
	if lifetime <= 0 {
		return 0, fmt.Sprintf("Requested lifetime %s is not a positive duration.", requested)
	}
	maximum := iamAccessTokenMaximumLifetime(email)
	if lifetime > maximum {
		return 0, fmt.Sprintf("Requested lifetime %s exceeds the maximum lifetime of %ds allowed for service account %s.",
			requested, int(maximum.Seconds()), email)
	}
	return lifetime, ""
}

// iamAccessTokenMaximumLifetime reports the longest access token that may be
// minted for a service account: twelve hours when an Organization Policy in
// force over its project lists it as an allowed value of
// constraints/iam.allowServiceAccountCredentialLifetimeExtension, and the
// default one hour otherwise.
func iamAccessTokenMaximumLifetime(email string) time.Duration {
	project := gcpProjectFromEmail(email)
	if project != "" && crmOrgPolicyListAllows("projects/"+project, crmConstraintAllowTokenLifetimeExtension, email) {
		return iamAccessTokenExtendedLifetime
	}
	return iamAccessTokenDefaultLifetime
}

// The rule the IAM Service Account Credentials API publishes for the
// `lifetime` member of GenerateAccessTokenRequest, verbatim from the
// iamcredentials v1 Discovery document vendored at
// specs/cloud-api/gcp/iamcredentials-v1.discovery.json.gz:
//
//	The desired lifetime duration of the access token in seconds. By default,
//	the maximum allowed value is 1 hour. To set a lifetime of up to 12 hours,
//	you can add the service account as an allowed value in an Organization
//	Policy that enforces the
//	`constraints/iam.allowServiceAccountCredentialLifetimeExtension`
//	constraint. See detailed instructions at
//	https://cloud.google.com/iam/help/credentials/lifetime If a value is not
//	specified, the token's lifetime will be set to a default value of 1 hour.
//
// Twelve hours is the ceiling in every case: Google's own client libraries
// refuse a longer one before the request leaves the process
// (google-auth-library-java, ImpersonatedCredentials: "lifetime must be less
// than or equal to 43200").
const (
	iamAccessTokenDefaultLifetime  = time.Hour
	iamAccessTokenExtendedLifetime = 12 * time.Hour
)
