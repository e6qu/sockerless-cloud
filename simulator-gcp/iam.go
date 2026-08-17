package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

type GCPServiceAccount struct {
	Name        string `json:"name"`
	ProjectId   string `json:"projectId"`
	UniqueId    string `json:"uniqueId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled"`
}

// GCPCustomRole mirrors the iam#Role resource for project- and
// organization-scoped custom roles. Name is the fully-qualified resource path
// (projects/{p}/roles/{id} or organizations/{o}/roles/{id}). Deleted roles are
// soft-deleted (Deleted=true) and can be undeleted within GCP's retention
// window; the sim keeps them in the store so UndeleteRole can revive them.
type GCPCustomRole struct {
	Name                string   `json:"name"`
	Title               string   `json:"title,omitempty"`
	Description         string   `json:"description,omitempty"`
	IncludedPermissions []string `json:"includedPermissions,omitempty"`
	Stage               string   `json:"stage,omitempty"`
	Etag                string   `json:"etag"`
	Deleted             bool     `json:"deleted,omitempty"`
}

type IAMPolicy struct {
	Kind       string       `json:"kind,omitempty"`
	ResourceId string       `json:"resourceId,omitempty"`
	Bindings   []IAMBinding `json:"bindings"`
	Etag       string       `json:"etag"`
	Version    int          `json:"version"`
}

type IAMBinding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
	// Condition is a nested writable object (CEL expression + title)
	// the sim persists verbatim so setIamPolicy→getIamPolicy round-trips
	// byte-exact for conditional bindings.
	Condition json.RawMessage `json:"condition,omitempty"`
}

// gcpResourcePolicies is the shared IAM policy store for GCP resources
// (artifact registry, storage buckets, etc.). It's package-level so that
// resource-specific handlers can process :getIamPolicy / :setIamPolicy requests.
var gcpResourcePolicies sim.Store[IAMPolicy]

// GCPServiceAccountKey mirrors the `iam#ServiceAccountKey` resource. Real GCP
// only returns privateKeyData on creation; subsequent Gets omit it.
// PublicKeyData carries the base64 of the public half in the encoding the
// caller named with `publicKeyType`, and is absent when none was asked for.
type GCPServiceAccountKey struct {
	Name            string `json:"name"`
	KeyAlgorithm    string `json:"keyAlgorithm"`
	ValidAfterTime  string `json:"validAfterTime"`
	ValidBeforeTime string `json:"validBeforeTime"`
	KeyType         string `json:"keyType"`
	PrivateKeyData  string `json:"privateKeyData,omitempty"` // only on Create response
	PrivateKeyType  string `json:"privateKeyType,omitempty"` // only on Create response
	PublicKeyData   string `json:"publicKeyData,omitempty"`  // only when publicKeyType is requested
}

// serviceAccountSystemKey is the system-managed signing key Google holds for
// every service account — the key `signBlob` and `signJwt` sign with. Its
// private half is never handed to the account's owner, and it is persisted so a
// signature stays verifiable across a simulator restart, the way a real
// account's system-managed key outlives any single server process.
type serviceAccountSystemKey struct {
	Name          string `json:"name"`
	PrivateKeyPEM string `json:"privateKeyPem"`
}

// GCPServiceAccountKeyMaterial holds the published halves of a user-managed
// service-account key, keyed by the key's full resource name. The struct itself
// never appears on the wire: the OAuth2 token endpoint reads PublicKeyPEM to
// verify a JWT-bearer assertion against the account's registered keys, the way
// real Google verifies an assertion before minting a token for it, and the keys
// surface renders one of the two encodings into `publicKeyData` when a caller
// names a publicKeyType.
type GCPServiceAccountKeyMaterial struct {
	Name           string `json:"name"`
	PublicKeyPEM   string `json:"publicKeyPem"`
	CertificatePEM string `json:"certificatePem,omitempty"`
}

// serviceAccountKeyTypeWanted reports whether a keyTypes filter admits a key
// type. An absent filter admits every type, which is how the API lists both a
// service account's user-managed and system-managed keys by default.
func serviceAccountKeyTypeWanted(filter []string, keyType string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, want := range filter {
		if want == keyType || want == "KEY_TYPE_UNSPECIFIED" {
			return true
		}
	}
	return false
}

// serviceAccountSystemManagedKey returns the resource describing an existing
// service account's system-managed key, resolving (and on first use creating)
// the key it names. It reports false for an account that does not exist, which
// has no keys of either kind.
func serviceAccountSystemManagedKey(accounts sim.Store[GCPServiceAccount], saName, email string) (GCPServiceAccountKey, bool) {
	if _, ok := accounts.Get(saName); !ok {
		return GCPServiceAccountKey{}, false
	}
	material, err := serviceAccountSigningKey(saName, email)
	if err != nil {
		return GCPServiceAccountKey{}, false
	}
	return GCPServiceAccountKey{
		Name:            saName + "/keys/" + material.keyID,
		KeyAlgorithm:    "KEY_ALG_RSA_2048",
		ValidAfterTime:  saCertificateEpoch.Format(time.RFC3339),
		ValidBeforeTime: saCertificateEpoch.AddDate(10, 0, 0).Format(time.RFC3339),
		KeyType:         "SYSTEM_MANAGED",
	}, true
}

// iamServiceAccounts and iamSAKeyPublics expose the service-account and
// key-material stores to the OAuth2 token endpoint (registered separately in
// oauth2.go), which resolves a JWT-bearer assertion's issuer to a registered
// account and verifies the assertion signature against that account's keys.
// Both are assigned once in registerIAM.
var (
	iamServiceAccounts sim.Store[GCPServiceAccount]
	iamSAKeyPublics    sim.Store[GCPServiceAccountKeyMaterial]
	iamSASystemKeys    sim.Store[serviceAccountSystemKey]
)

func registerIAM(srv *sim.Server) {
	serviceAccounts := sim.MakeStore[GCPServiceAccount](srv.DB(), "iam_service_accounts")
	saKeys := sim.MakeStore[GCPServiceAccountKey](srv.DB(), "iam_sa_keys")
	saKeyPublics := sim.MakeStore[GCPServiceAccountKeyMaterial](srv.DB(), "iam_sa_key_publics")
	iamServiceAccounts = serviceAccounts
	iamSAKeyPublics = saKeyPublics
	iamSASystemKeys = sim.MakeStore[serviceAccountSystemKey](srv.DB(), "iam_sa_system_keys")
	projectPolicies := sim.MakeStore[IAMPolicy](srv.DB(), "iam_project_policies")
	gcpResourcePolicies = sim.MakeStore[IAMPolicy](srv.DB(), "iam_resource_policies")
	resourcePolicies := gcpResourcePolicies
	customRoles := sim.MakeStore[GCPCustomRole](srv.DB(), "iam_custom_roles")
	if iamLROs == nil {
		iamLROs = sim.MakeStore[Operation](srv.DB(), "iam_lro_operations")
	}
	if iamResources == nil {
		iamResources = sim.MakeStore[map[string]any](srv.DB(), "iam_admin_resources")
	}

	// Cloud Resource Manager v3 — the resource-hierarchy + tagging API
	// (projects / folders / organizations / liens / tagKeys / tagValues /
	// tagBindings / tagHolds / effectiveTags). Also mounts the legacy
	// Cloud Resource Manager v1 GetProject the sim's terraform paths use.
	registerCRMv3(srv, projectPolicies, resourcePolicies)

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
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		// The org policy the constraint governs is resolved through the
		// project's place in the resource hierarchy, so a policy set on the
		// organization or an intervening folder reaches the project too.
		if crmOrgPolicyBooleanEnforced("projects/"+project, crmConstraintDisableServiceAccountCreation) {
			sim.GCPError(w, http.StatusBadRequest,
				"Service account creation is not allowed on this project.", "FAILED_PRECONDITION")
			return
		}

		email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", req.AccountId, project)
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		if _, exists := serviceAccounts.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
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
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		sa, ok := serviceAccounts.Get(name)
		if !ok {
			sim.GCPErrorf(w, 404, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Update service account — PATCH with an updateMask over the mutable
	// fields (displayName / description). Real GCP's UpdateServiceAccount
	// wraps the account under a `serviceAccount` envelope alongside the mask.
	srv.HandleFunc("PATCH /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
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
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		var req struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		sa.DisplayName = req.DisplayName
		sa.Description = req.Description
		serviceAccounts.Put(name, sa)
		sim.WriteJSON(w, http.StatusOK, sa)
	})

	// Delete service account. A service account's keys cannot outlive it —
	// real GCP invalidates them with the account — so its key rows and the
	// registered public halves go with it, and a later account created under
	// the same email starts with no keys.
	srv.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{email}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)

		serviceAccounts.Delete(name)
		iamSASystemKeys.Delete(name)
		keyPrefix := name + "/keys/"
		for _, key := range saKeys.Filter(func(k GCPServiceAccountKey) bool {
			return strings.HasPrefix(k.Name, keyPrefix)
		}) {
			saKeys.Delete(key.Name)
			saKeyPublics.Delete(key.Name)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// Create service account key — returns full key on creation only.
	// Real GCP wire: POST /v1/projects/{p}/serviceAccounts/{email}/keys
	// project="-" is the GCP wildcard: extract the project from the email.
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{email}/keys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(saName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		if crmOrgPolicyBooleanEnforced("projects/"+project, crmConstraintDisableServiceAccountKeyCreation) {
			sim.GCPError(w, http.StatusBadRequest,
				"Key creation is not allowed on this service account.", "FAILED_PRECONDITION")
			return
		}
		keyID := generateUUID()
		keyName := fmt.Sprintf("%s/keys/%s", saName, keyID)
		now := time.Now().UTC()
		key := GCPServiceAccountKey{
			Name:            keyName,
			KeyAlgorithm:    "KEY_ALG_RSA_2048",
			ValidAfterTime:  now.Format(time.RFC3339),
			ValidBeforeTime: now.AddDate(10, 0, 0).Format(time.RFC3339),
			KeyType:         "USER_MANAGED",
		}
		// Generate the key material before persisting metadata: if generation
		// fails, the store must not retain a key that never had private-key
		// material (a subsequent Get would return a phantom key).
		privateKeyData, publicKeyPEM, certificatePEM, err := gcpMakeSAKeyJSON(project, keyID, email, sa.UniqueId)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "generate key: %v", err)
			return
		}
		saKeys.Put(keyName, key)
		// Register the published halves so the OAuth2 token endpoint can verify
		// JWT-bearer assertions signed with this key, as real Google does, and
		// the keys surface can serve either publicKeyType for it.
		saKeyPublics.Put(keyName, GCPServiceAccountKeyMaterial{
			Name:           keyName,
			PublicKeyPEM:   publicKeyPEM,
			CertificatePEM: certificatePEM,
		})

		resp := key
		resp.PrivateKeyData = privateKeyData
		resp.PrivateKeyType = "TYPE_GOOGLE_CREDENTIALS_FILE"
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Get service account key. The private half is returned only by Create;
	// the public half comes back when the caller names a publicKeyType, in the
	// encoding it names. The account's system-managed key — the key signBlob
	// and signJwt sign with — is addressable here alongside its user-managed
	// keys, which is what lets a client verify a signature it was handed.
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		keyID := sim.PathParam(r, "keyId")
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		keyName := saName + "/keys/" + keyID
		publicKeyType := r.URL.Query().Get("publicKeyType")

		key, ok := saKeys.Get(keyName)
		if ok {
			material, hasMaterial := saKeyPublics.Get(keyName)
			if publicKeyType != "" && hasMaterial {
				data, err := serviceAccountPublicKeyData(publicKeyType, material.PublicKeyPEM, material.CertificatePEM)
				if err != nil {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
					return
				}
				key.PublicKeyData = data
			}
			sim.WriteJSON(w, http.StatusOK, key)
			return
		}

		system, systemOK := serviceAccountSystemManagedKey(serviceAccounts, saName, email)
		if !systemOK || system.Name != keyName {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "key %s not found", keyID)
			return
		}
		if publicKeyType != "" {
			material, err := serviceAccountSigningKey(saName, email)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			data, err := serviceAccountPublicKeyData(publicKeyType, material.publicKeyPEM, material.certificatePEM)
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			system.PublicKeyData = data
		}
		sim.WriteJSON(w, http.StatusOK, system)
	})

	// List service account keys. Both key types are listed unless keyTypes
	// narrows the result, which is how the API scopes the listing.
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/keys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		prefix := saName + "/keys/"
		wanted := r.URL.Query()["keyTypes"]
		keys := []GCPServiceAccountKey{}
		if serviceAccountKeyTypeWanted(wanted, "USER_MANAGED") {
			keys = append(keys, saKeys.Filter(func(k GCPServiceAccountKey) bool {
				return strings.HasPrefix(k.Name, prefix)
			})...)
		}
		if serviceAccountKeyTypeWanted(wanted, "SYSTEM_MANAGED") {
			if system, ok := serviceAccountSystemManagedKey(serviceAccounts, saName, email); ok {
				keys = append(keys, system)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	})

	// Delete service account key.
	srv.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		email := sim.PathParam(r, "email")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		keyID := sim.PathParam(r, "keyId")
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		keyName := saName + "/keys/" + keyID
		if !saKeys.Delete(keyName) {
			// A system-managed key belongs to Google, not to the account's
			// owner, so it is refused rather than reported missing.
			if system, ok := serviceAccountSystemManagedKey(serviceAccounts, saName, email); ok && system.Name == keyName {
				sim.GCPError(w, http.StatusBadRequest,
					"Request contains an invalid argument.", "INVALID_ARGUMENT")
				return
			}
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "key %s not found", keyID)
			return
		}
		// A deleted key is revoked: the token endpoint stops accepting
		// assertions signed with it, exactly as real Google does.
		saKeyPublics.Delete(keyName)
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
		project := sim.PathParam(r, "project")
		emailAction := sim.PathParam(r, "emailAction")
		email, action, _ := strings.Cut(emailAction, ":")
		if project == "-" {
			project = gcpProjectFromEmail(email)
		}
		name := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		if _, ok := serviceAccounts.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
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
			// naming the impersonated service account as its subject. Real
			// expiry is RFC3339Nano with timezone offset; the SDK parses it
			// with time.Parse(time.RFC3339).
			var req struct {
				Scope     []string `json:"scope"`
				Lifetime  string   `json:"lifetime"`
				Delegates []string `json:"delegates"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			lifetime, refusal := iamAccessTokenLifetime(req.Lifetime, email)
			if refusal != "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", refusal)
				return
			}
			now := time.Now()
			expires := now.Add(lifetime)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"accessToken": signAccessToken(email, now, expires),
				"expireTime":  expires.UTC().Format(time.RFC3339),
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
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.Audience == "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "audience is required")
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
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(req.Payload)
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "payload must be base64: %v", err)
				return
			}
			material, err := serviceAccountSigningKey(name, email)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			digest := sha256.Sum256(raw)
			sig, err := rsa.SignPKCS1v15(rand.Reader, material.key, crypto.SHA256, digest[:])
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "sign blob: %v", err)
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
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			material, err := serviceAccountSigningKey(name, email)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			headerJSON, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": material.keyID})
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encode JWT header: %v", err)
				return
			}
			headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
			payloadB64 := base64.RawURLEncoding.EncodeToString([]byte(req.Payload))
			signingInput := headerB64 + "." + payloadB64
			digest := sha256.Sum256([]byte(signingInput))
			sig, err := rsa.SignPKCS1v15(rand.Reader, material.key, crypto.SHA256, digest[:])
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "sign JWT: %v", err)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"keyId":     material.keyID,
				"signedJwt": signingInput + "." + base64.RawURLEncoding.EncodeToString(sig),
			})
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported service-account action %q", action)
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
		page, next, ok := paginateList(w, r, accounts)
		if !ok {
			return
		}
		resp := map[string]any{"accounts": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// iamcredentials.serviceAccounts.getAllowedLocations — returns the
	// trust-boundary locations a short-lived credential minted for this SA
	// may be used in. The sim models an unrestricted boundary ("0x0" is the
	// real wire's "all locations" sentinel encodedLocations value).
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})
	// iamcredentials.projects.locations.workloadIdentityPools.getAllowedLocations
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})
	// iamcredentials.locations.workforcePools.getAllowedLocations
	srv.HandleFunc("GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})

	// The v1 project colon-verbs (IAM triple + undelete) are mounted in
	// cloudresourcemanager.go; they resolve the project and address the
	// same per-project policy the v3 verbs use.

	// Catch-all AIP-141 IAM dispatcher (Artifact Registry + any
	// resource not handled by a more-specific verb dispatcher).
	// Resources with their own verb dispatcher (Pub/Sub topics +
	// subscriptions, Memorystore instances, etc.) delegate to
	// handleResourceIAM directly.
	srv.HandleFunc("POST /v1/{resource...}", func(w http.ResponseWriter, r *http.Request) {
		resource := sim.PathParam(r, "resource")
		var action string
		for _, verb := range []string{":getIamPolicy", ":setIamPolicy", ":testIamPermissions"} {
			if strings.HasSuffix(resource, verb) {
				action = strings.TrimPrefix(verb, ":")
				resource = strings.TrimSuffix(resource, verb)
				break
			}
		}
		if action == "" {
			http.NotFound(w, r)
			return
		}
		handleResourceIAM(w, r, resourcePolicies, resource, action)
	})

	// QueryTestablePermissions — the catalog of permissions that can be tested
	// (and thus included in a custom role) on a given resource. `gcloud iam
	// roles create/update` calls this to validate the --permissions flag before
	// issuing CreateRole/UpdateRole. Real GCP returns a paginated list scoped to
	// the resource's service surface; the sim returns the representative catalog
	// it knows about (the union of permissions its predefined roles reference,
	// plus the common service-prefixed permissions the repo exercises).
	srv.HandleFunc("POST /v1/permissions:queryTestablePermissions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string `json:"fullResourceName"`
			PageSize         int    `json:"pageSize"`
			PageToken        string `json:"pageToken"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		perms := gcpTestablePermissions()
		out := make([]map[string]any, 0, len(perms))
		for _, p := range perms {
			out = append(out, map[string]any{
				"name":                    p,
				"stage":                   "GA",
				"customRolesSupportLevel": "SUPPORTED",
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": out})
	})

	// Custom roles — projects.roles.* and organizations.roles.*. A custom
	// role is a tenant-defined IAM role with its own includedPermissions; the
	// two scopes share identical CRUD semantics, differing only in the parent
	// prefix (projects/{p} vs organizations/{o}).
	registerCustomRoles(srv, customRoles, "project", "GET /v1/projects/{parent}/roles", "GET /v1/projects/{parent}/roles/{role}",
		"POST /v1/projects/{parent}/roles", "POST /v1/projects/{parent}/roles/{roleAction}",
		"PATCH /v1/projects/{parent}/roles/{role}", "DELETE /v1/projects/{parent}/roles/{role}")
	registerCustomRoles(srv, customRoles, "organization", "GET /v1/organizations/{parent}/roles", "GET /v1/organizations/{parent}/roles/{role}",
		"POST /v1/organizations/{parent}/roles", "POST /v1/organizations/{parent}/roles/{roleAction}",
		"PATCH /v1/organizations/{parent}/roles/{role}", "DELETE /v1/organizations/{parent}/roles/{role}")

	// Workload Identity Federation pools (project-scoped) + workforce pools
	// (location-scoped, org-level) + OAuth clients. Each is a real CRUD
	// surface whose create/patch/delete/undelete return a long-running
	// Operation; the sim settles the operation synchronously (done=true).
	registerWorkloadIdentityPools(srv)
	registerWorkforcePools(srv)
	registerOAuthClients(srv)

	// Predefined roles — roles.list / roles.get. The catalog of curated
	// (Google-managed) roles. The sim carries a bounded representative set
	// (the basic roles plus the IAM/storage roles the repo references), not
	// the full ~1500-role catalog. Custom-role CRUD is a staged epic and is
	// not handled here.
	srv.HandleFunc("GET /v1/roles", func(w http.ResponseWriter, r *http.Request) {
		roles := gcpPredefinedRoles()
		// roles.list omits includedPermissions unless view=FULL.
		full := r.URL.Query().Get("view") == "FULL"
		out := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			out = append(out, gcpRoleJSON(role, full))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
	})

	srv.HandleFunc("GET /v1/roles/{role...}", func(w http.ResponseWriter, r *http.Request) {
		name := "roles/" + sim.PathParam(r, "role")
		for _, role := range gcpPredefinedRoles() {
			if role.Name == name {
				// roles.get returns the full role including includedPermissions.
				sim.WriteJSON(w, http.StatusOK, gcpRoleJSON(role, true))
				return
			}
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
	})

	// Bucket IAM - getIamPolicy
	srv.HandleFunc("GET /storage/v1/b/{bucket}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")

		policy, ok := resourcePolicies.Get("bucket/" + bucket)
		if !ok {
			policy = IAMPolicy{
				Bindings: []IAMBinding{},
				Etag:     gcpPolicyETag(),
				Version:  1,
			}
			// Persist the synthesized default so its etag is stable across
			// reads — the optimistic-concurrency check on setIamPolicy
			// validates against the etag a prior getIamPolicy returned.
			resourcePolicies.Put("bucket/"+bucket, policy)
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = "projects/_/buckets/" + bucket
		sim.WriteJSON(w, http.StatusOK, policy)
	})

	// Bucket IAM - setIamPolicy
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")

		var policy IAMPolicy
		if err := sim.ReadJSON(r, &policy); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := validateIAMMembers(policy.Bindings); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}

		current, present := resourcePolicies.Get("bucket/" + bucket)
		if gcpIAMETagConflict(w, policy.Etag, current.Etag, present) {
			return
		}
		policy.Etag = gcpPolicyETag()
		if policy.Version == 0 {
			policy.Version = 1
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = "projects/_/buckets/" + bucket
		resourcePolicies.Put("bucket/"+bucket, policy)

		sim.WriteJSON(w, http.StatusOK, policy)
	})
}

// CRMProject mirrors the cloudresourcemanager#Project (v3) resource.
type CRMProject struct {
	Name        string            `json:"name"`
	Parent      string            `json:"parent,omitempty"`
	ProjectId   string            `json:"projectId"`
	State       string            `json:"state"`
	DisplayName string            `json:"displayName,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	DeleteTime  string            `json:"deleteTime,omitempty"`
	Etag        string            `json:"etag,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// CRMFolder mirrors the cloudresourcemanager#Folder (v3) resource.
type CRMFolder struct {
	Name        string `json:"name"`
	Parent      string `json:"parent,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	State       string `json:"state"`
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
	DeleteTime  string `json:"deleteTime,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

var crmFolders sim.Store[CRMFolder]

// CRMLien mirrors the cloudresourcemanager#Lien (v3) resource.
type CRMLien struct {
	Name         string   `json:"name"`
	Parent       string   `json:"parent,omitempty"`
	Restrictions []string `json:"restrictions,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	CreateTime   string   `json:"createTime,omitempty"`
}

// CRMTagKey mirrors the cloudresourcemanager#TagKey (v3) resource.
type CRMTagKey struct {
	Name               string `json:"name"`
	Parent             string `json:"parent,omitempty"`
	ShortName          string `json:"shortName,omitempty"`
	NamespacedName     string `json:"namespacedName,omitempty"`
	Description        string `json:"description,omitempty"`
	CreateTime         string `json:"createTime,omitempty"`
	UpdateTime         string `json:"updateTime,omitempty"`
	Etag               string `json:"etag,omitempty"`
	Purpose            string `json:"purpose,omitempty"`
	AllowedValuesRegex string `json:"allowedValuesRegex,omitempty"`
}

// CRMTagValue mirrors the cloudresourcemanager#TagValue (v3) resource.
type CRMTagValue struct {
	Name           string `json:"name"`
	Parent         string `json:"parent,omitempty"`
	ShortName      string `json:"shortName,omitempty"`
	NamespacedName string `json:"namespacedName,omitempty"`
	Description    string `json:"description,omitempty"`
	CreateTime     string `json:"createTime,omitempty"`
	UpdateTime     string `json:"updateTime,omitempty"`
	Etag           string `json:"etag,omitempty"`
}

// CRMTagBinding mirrors the cloudresourcemanager#TagBinding (v3) resource.
type CRMTagBinding struct {
	Name                   string `json:"name"`
	Parent                 string `json:"parent,omitempty"`
	TagValue               string `json:"tagValue,omitempty"`
	TagValueNamespacedName string `json:"tagValueNamespacedName,omitempty"`
}

// CRMTagHold mirrors the cloudresourcemanager#TagHold (v3) resource.
type CRMTagHold struct {
	Name       string `json:"name"`
	Holder     string `json:"holder,omitempty"`
	Origin     string `json:"origin,omitempty"`
	HelpLink   string `json:"helpLink,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
}

// Fully-qualified Any types of the per-verb Cloud Resource Manager v3
// operation-metadata messages. The GAPIC clients unmarshal the metadata
// against their registered proto types, so an invented type name fails the
// client-side resolve — each operation must carry the verb's real message.
const (
	crmMetaCreateProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateProjectMetadata"
	crmMetaUpdateProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateProjectMetadata"
	crmMetaDeleteProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteProjectMetadata"
	crmMetaMoveProject                = "type.googleapis.com/google.cloud.resourcemanager.v3.MoveProjectMetadata"
	crmMetaUndeleteProject            = "type.googleapis.com/google.cloud.resourcemanager.v3.UndeleteProjectMetadata"
	crmMetaCreateFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateFolderMetadata"
	crmMetaUpdateFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateFolderMetadata"
	crmMetaDeleteFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteFolderMetadata"
	crmMetaMoveFolder                 = "type.googleapis.com/google.cloud.resourcemanager.v3.MoveFolderMetadata"
	crmMetaUndeleteFolder             = "type.googleapis.com/google.cloud.resourcemanager.v3.UndeleteFolderMetadata"
	crmMetaCreateTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagKeyMetadata"
	crmMetaUpdateTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagKeyMetadata"
	crmMetaDeleteTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagKeyMetadata"
	crmMetaCreateTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagValueMetadata"
	crmMetaUpdateTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagValueMetadata"
	crmMetaDeleteTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagValueMetadata"
	crmMetaCreateTagHold              = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagHoldMetadata"
	crmMetaDeleteTagHold              = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagHoldMetadata"
	crmMetaCreateTagBinding           = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagBindingMetadata"
	crmMetaDeleteTagBinding           = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagBindingMetadata"
	crmMetaUpdateCapability           = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateCapabilityMetadata"
	crmMetaUpdateTagBindingCollection = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagBindingCollectionMetadata"
)

// crmLRO builds a settled (done=true) long-running Operation whose response
// embeds the supplied resource and whose metadata carries the verb's real
// per-operation metadata message type. The Cloud Resource Manager LRO
// mutations (project/folder/tagKey/tagValue create/patch/delete/move/
// undelete) return an Operation; the sim settles it synchronously, matching
// how every other IAM-admin collection in this file resolves its LROs. The
// operation is persisted so a client resuming it by name
// (GET /v3/operations/{op}, the path the resourcemanager GAPIC LRO poller
// uses) reads the same record.
func crmLRO(resource any, typeName, metadataType string) Operation {
	resp := map[string]any{}
	b, _ := json.Marshal(resource)
	_ = json.Unmarshal(b, &resp)
	resp["@type"] = typeName
	op := Operation{
		Name: "operations/cp." + gcpNumericID(19),
		Metadata: map[string]any{
			"@type": metadataType,
		},
		Done:     true,
		Response: resp,
	}
	if crOperations != nil {
		crOperations.Put(op.Name, op)
	}
	return op
}

// crmEtag mints an etag for a Cloud Resource Manager resource.
func crmEtag() string {
	return base64.StdEncoding.EncodeToString([]byte(generateUUID()))
}

// crmIamVerb dispatches a Cloud Resource Manager v3 IAM colon-verb captured in
// an "{id}:verb" path parameter. It resolves the resource-scoped policy key and
// reuses the shared handleResourceIAM so etag / member-validation / optimistic-
// concurrency behaviour matches every other GCP resource's IAM surface.
func crmIamVerb(w http.ResponseWriter, r *http.Request, store sim.Store[IAMPolicy], idAction, kind string) bool {
	id, action, found := strings.Cut(idAction, ":")
	if !found {
		return false
	}
	switch action {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, store, kind+"/"+id, action)
		return true
	}
	return false
}

// registerCRMv3 mounts the Cloud Resource Manager v3 resource-hierarchy and
// tagging surface plus the Cloud Resource Manager v1 projects surface
// (cloudresourcemanager.go) that gcloud's `projects` commands and
// terraform-provider-google's google_project resource speak. Every collection
// is a real CRUD store; mutating ops that the API models as long-running
// return a settled Operation, and IAM verbs reuse the shared policy store.
func registerCRMv3(srv *sim.Server, projectPolicies, resourcePolicies sim.Store[IAMPolicy]) {
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}
	projects := sim.MakeStore[CRMProject](srv.DB(), "crm_projects")
	crmProjects = projects
	crmEnsureDefaultProject()
	crmOrganizations = sim.MakeStore[CRMOrganization](srv.DB(), "crm_organizations")
	crmEnsureDefaultOrganization()
	folders := sim.MakeStore[CRMFolder](srv.DB(), "crm_folders")
	crmFolders = folders
	crmLiens = sim.MakeStore[CRMLien](srv.DB(), "crm_liens")
	crmOrgPolicies = sim.MakeStore[CRMOrgPolicyRow](srv.DB(), "crm_org_policies")
	registerCloudResourceManagerV1(srv, projectPolicies, resourcePolicies)
	registerCloudResourceManagerV2(srv, resourcePolicies)
	tagKeys := sim.MakeStore[CRMTagKey](srv.DB(), "crm_tag_keys")
	tagValues := sim.MakeStore[CRMTagValue](srv.DB(), "crm_tag_values")
	tagBindings := sim.MakeStore[CRMTagBinding](srv.DB(), "crm_tag_bindings")
	tagHolds := sim.MakeStore[CRMTagHold](srv.DB(), "crm_tag_holds")
	const (
		typeProject  = "type.googleapis.com/google.cloud.resourcemanager.v3.Project"
		typeFolder   = "type.googleapis.com/google.cloud.resourcemanager.v3.Folder"
		typeTagKey   = "type.googleapis.com/google.cloud.resourcemanager.v3.TagKey"
		typeTagValue = "type.googleapis.com/google.cloud.resourcemanager.v3.TagValue"
		typeTagBind  = "type.googleapis.com/google.cloud.resourcemanager.v3.TagBinding"
		typeTagHold  = "type.googleapis.com/google.cloud.resourcemanager.v3.TagHold"
		typeEmpty    = "type.googleapis.com/google.protobuf.Empty"
	)

	// ---- resource semantics ----------------------------------------------
	// Resource semantics are optional cloud metadata. Resources with no
	// assigned semantics return the requested full resource name and an empty
	// map; this simulator currently exposes no API that assigns semantics.
	srv.HandleFunc("GET /v3:fetchResourceSemantics", func(w http.ResponseWriter, r *http.Request) {
		fullName := r.URL.Query().Get("fullResourceName")
		trimmed := strings.TrimPrefix(fullName, "//")
		service, resourcePath, found := strings.Cut(trimmed, "/")
		if fullName == "" || !strings.HasPrefix(fullName, "//") || !found || service == "" || resourcePath == "" {
			sim.GCPError(w, http.StatusBadRequest, "fullResourceName must be a full Google Cloud resource name", "INVALID_ARGUMENT")
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"fullResourceName": fullName,
			"semantics":        map[string]string{},
		})
	})

	// ---- Cloud Resource Manager v1 GetProject --------------------------
	// gcloud projects describe and google_project's terraform Read speak
	// the v1 read. A project the sim has never seen is a real 403 — the
	// API never discloses whether an inaccessible project exists.
	srv.HandleFunc("GET /v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		project, action, isCustomMethod := gcpCustomMethod(sim.PathParam(r, "project"))
		if isCustomMethod {
			if action == "showEffectiveAutokeyConfig" {
				kmsHandleShowEffectiveAutokeyConfig(w, "projects/"+project)
				return
			}
			gcpMethodNotFound(w)
			return
		}
		p, ok := crmResolveProject(project)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, crmV1Project(p))
	})

	// ---- projects (v3) ---------------------------------------------------
	srv.HandleFunc("GET /v3/projects:search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		rows := projects.Filter(func(p CRMProject) bool { return crmSearchMatch(p, query) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectId < rows[j].ProjectId })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"projects": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("GET /v3/projects", func(w http.ResponseWriter, r *http.Request) {
		// v3 ListProjects requires a parent and returns only ACTIVE
		// projects unless showDeleted is set; project-wide discovery is
		// SearchProjects' job.
		parent := r.URL.Query().Get("parent")
		if parent == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Request contains an invalid argument.")
			return
		}
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		rows := projects.Filter(func(p CRMProject) bool {
			return p.Parent == parent && (showDeleted || p.State == "ACTIVE")
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectId < rows[j].ProjectId })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"projects": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v3/projects", func(w http.ResponseWriter, r *http.Request) {
		var req CRMProject
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := crmValidateProjectID(req.ProjectId); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if _, exists := projects.Get(req.ProjectId); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Requested entity already exists")
			return
		}
		p := CRMProject{
			Name:        "projects/" + gcpNumericID(12),
			Parent:      req.Parent,
			ProjectId:   req.ProjectId,
			State:       "ACTIVE",
			DisplayName: req.DisplayName,
			Labels:      req.Labels,
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		projects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaCreateProject))
	})
	srv.HandleFunc("GET /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, p)
	})
	srv.HandleFunc("PATCH /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		var req CRMProject
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		applyMask := func(field string) bool { return mask == "" || strings.Contains(mask, field) }
		if applyMask("displayName") {
			p.DisplayName = req.DisplayName
		}
		if applyMask("labels") {
			p.Labels = req.Labels
		}
		p.UpdateTime = nowTimestamp()
		p.Etag = crmEtag()
		projects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaUpdateProject))
	})
	srv.HandleFunc("DELETE /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		// Real DeleteProject soft-deletes: the project enters
		// DELETE_REQUESTED (30-day pending deletion) and only an ACTIVE
		// project may be deleted.
		if p.State != "ACTIVE" {
			sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Project \"%s\" has lifecycle state %s; delete requires ACTIVE", p.ProjectId, p.State)
			return
		}
		if crmProjectDeleteBlocked(w, p) {
			return
		}
		p.State = "DELETE_REQUESTED"
		p.DeleteTime = nowTimestamp()
		p.UpdateTime = nowTimestamp()
		projects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaDeleteProject))
	})
	// projects colon-verbs: move / undelete / IAM triple.
	srv.HandleFunc("POST /v3/projects/{projectAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "projectAction")
		id, action, found := gcpCustomMethod(idAction)
		// Resolve the method before the project, the way Google's frontend
		// does, so a method this simulator does not serve is answered as an
		// unrouted method rather than with the project's 403 — which would
		// claim the project is inaccessible when the method simply is not
		// mounted here.
		if !found || !crmV3ProjectPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		p, ok := crmResolveProject(id)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		// Normalize a project-number reference to the project ID before
		// the IAM verbs so both spellings address one policy.
		if crmIamVerb(w, r, projectPolicies, p.ProjectId+":"+action, "project") {
			return
		}
		switch action {
		case "move":
			var req struct {
				DestinationParent string `json:"destinationParent"`
			}
			_ = sim.ReadJSON(r, &req)
			p.Parent = req.DestinationParent
			p.UpdateTime = nowTimestamp()
			projects.Put(p.ProjectId, p)
			sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaMoveProject))
		case "undelete":
			if p.State != "DELETE_REQUESTED" {
				sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Project \"%s\" has lifecycle state %s; undelete requires DELETE_REQUESTED", p.ProjectId, p.State)
				return
			}
			p.State = "ACTIVE"
			p.DeleteTime = ""
			p.UpdateTime = nowTimestamp()
			projects.Put(p.ProjectId, p)
			sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaUndeleteProject))
		default:
			// crmIamVerb served the IAM triple, so move and undelete above are
			// the only methods crmV3ProjectPOSTMethods still admits here.
			gcpMethodNotFound(w)
		}
	})

	// ---- folders (v3) ----------------------------------------------------
	srv.HandleFunc("GET /v3/folders:search", func(w http.ResponseWriter, r *http.Request) {
		rows := folders.Filter(func(CRMFolder) bool { return true })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"folders": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("GET /v3/folders", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		rows := folders.Filter(func(f CRMFolder) bool { return parent == "" || f.Parent == parent })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"folders": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v3/folders", func(w http.ResponseWriter, r *http.Request) {
		var req CRMFolder
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		f := CRMFolder{
			Name:        "folders/" + gcpNumericID(12),
			Parent:      req.Parent,
			DisplayName: req.DisplayName,
			State:       "ACTIVE",
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		folders.Put(f.Name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaCreateFolder))
	})
	srv.HandleFunc("GET /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		f, ok := folders.Get("folders/" + sim.PathParam(r, "folder"))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})
	srv.HandleFunc("PATCH /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := folders.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		var req CRMFolder
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.DisplayName != "" {
			f.DisplayName = req.DisplayName
		}
		f.UpdateTime = nowTimestamp()
		f.Etag = crmEtag()
		folders.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaUpdateFolder))
	})
	srv.HandleFunc("DELETE /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := folders.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		f.State = "DELETE_REQUESTED"
		f.UpdateTime = nowTimestamp()
		folders.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaDeleteFolder))
	})
	srv.HandleFunc("POST /v3/folders/{folderAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "folderAction")
		if crmIamVerb(w, r, resourcePolicies, idAction, "folder") {
			return
		}
		id, action, _ := strings.Cut(idAction, ":")
		name := "folders/" + id
		f, ok := folders.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		switch action {
		case "move":
			var req struct {
				DestinationParent string `json:"destinationParent"`
			}
			_ = sim.ReadJSON(r, &req)
			f.Parent = req.DestinationParent
			f.UpdateTime = nowTimestamp()
			folders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaMoveFolder))
		case "undelete":
			f.State = "ACTIVE"
			f.UpdateTime = nowTimestamp()
			folders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaUndeleteFolder))
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported folder action %q", action)
		}
	})

	// ---- organizations (v3) ----------------------------------------------
	// The organization store is shared with the v1 reads; an organization the
	// caller cannot see is a 403, never a 404.
	srv.HandleFunc("GET /v3/organizations:search", func(w http.ResponseWriter, r *http.Request) {
		rows := crmOrganizations.Filter(func(o CRMOrganization) bool {
			return crmOrganizationMatch(o, r.URL.Query().Get("query"))
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"organizations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("GET /v3/organizations/{org}", func(w http.ResponseWriter, r *http.Request) {
		org, ok := crmOrganizations.Get("organizations/" + sim.PathParam(r, "org"))
		if !ok {
			crmOrganizationPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, org)
	})
	srv.HandleFunc("POST /v3/organizations/{orgAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "orgAction")
		if crmIamVerb(w, r, resourcePolicies, idAction, "organization") {
			return
		}
		gcpMethodNotFound(w)
	})

	// ---- liens (v3) ------------------------------------------------------
	// One lien collection under two spellings: the handlers are shared with
	// the v1 routes in cloudresourcemanager.go.
	srv.HandleFunc("POST /v3/liens", crmCreateLien)
	srv.HandleFunc("GET /v3/liens", crmListLiens)
	srv.HandleFunc("GET /v3/liens/{lien}", crmGetLien)
	srv.HandleFunc("DELETE /v3/liens/{lien}", crmDeleteLien)

	// The v3 operations.get read lives in cloudresourcemanager.go
	// (crmGetOperation) — every CRM LRO is persisted, so a poll returns
	// the stored record and an unknown name is a real NOT_FOUND.

	// ---- tagKeys (v3) ----------------------------------------------------
	srv.HandleFunc("GET /v3/tagKeys/namespaced", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query().Get("name")
		for _, k := range tagKeys.Filter(func(CRMTagKey) bool { return true }) {
			if k.NamespacedName == ns {
				sim.WriteJSON(w, http.StatusOK, k)
				return
			}
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key %q not found", ns)
	})
	srv.HandleFunc("GET /v3/tagKeys", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		rows := tagKeys.Filter(func(k CRMTagKey) bool { return parent == "" || k.Parent == parent })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"tagKeys": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v3/tagKeys", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagKey
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		k := CRMTagKey{
			Name:               "tagKeys/" + gcpNumericID(18),
			Parent:             req.Parent,
			ShortName:          req.ShortName,
			NamespacedName:     crmNamespaced(req.Parent, req.ShortName),
			Description:        req.Description,
			Purpose:            req.Purpose,
			AllowedValuesRegex: req.AllowedValuesRegex,
			CreateTime:         nowTimestamp(),
			UpdateTime:         nowTimestamp(),
			Etag:               crmEtag(),
		}
		tagKeys.Put(k.Name, k)
		sim.WriteJSON(w, http.StatusOK, crmLRO(k, typeTagKey, crmMetaCreateTagKey))
	})
	srv.HandleFunc("GET /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		k, ok := tagKeys.Get("tagKeys/" + sim.PathParam(r, "key"))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, k)
	})
	srv.HandleFunc("PATCH /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagKeys/" + sim.PathParam(r, "key")
		k, ok := tagKeys.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key not found")
			return
		}
		var req CRMTagKey
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		if mask == "" || strings.Contains(mask, "description") {
			k.Description = req.Description
		}
		k.UpdateTime = nowTimestamp()
		k.Etag = crmEtag()
		tagKeys.Put(name, k)
		sim.WriteJSON(w, http.StatusOK, crmLRO(k, typeTagKey, crmMetaUpdateTagKey))
	})
	srv.HandleFunc("DELETE /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagKeys/" + sim.PathParam(r, "key")
		k, ok := tagKeys.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key not found")
			return
		}
		tagKeys.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(k, typeTagKey, crmMetaDeleteTagKey))
	})
	srv.HandleFunc("POST /v3/tagKeys/{keyAction}", func(w http.ResponseWriter, r *http.Request) {
		if crmIamVerb(w, r, resourcePolicies, sim.PathParam(r, "keyAction"), "tagKey") {
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported tagKey action")
	})

	// ---- tagValues (v3) --------------------------------------------------
	srv.HandleFunc("GET /v3/tagValues/namespaced", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query().Get("name")
		for _, v := range tagValues.Filter(func(CRMTagValue) bool { return true }) {
			if v.NamespacedName == ns {
				sim.WriteJSON(w, http.StatusOK, v)
				return
			}
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value %q not found", ns)
	})
	srv.HandleFunc("GET /v3/tagValues", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		rows := tagValues.Filter(func(v CRMTagValue) bool { return parent == "" || v.Parent == parent })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"tagValues": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v3/tagValues", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagValue
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		v := CRMTagValue{
			Name:           "tagValues/" + gcpNumericID(18),
			Parent:         req.Parent,
			ShortName:      req.ShortName,
			NamespacedName: crmNamespaced(req.Parent, req.ShortName),
			Description:    req.Description,
			CreateTime:     nowTimestamp(),
			UpdateTime:     nowTimestamp(),
			Etag:           crmEtag(),
		}
		tagValues.Put(v.Name, v)
		sim.WriteJSON(w, http.StatusOK, crmLRO(v, typeTagValue, crmMetaCreateTagValue))
	})
	srv.HandleFunc("GET /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := tagValues.Get("tagValues/" + sim.PathParam(r, "val"))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})
	srv.HandleFunc("PATCH /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagValues/" + sim.PathParam(r, "val")
		v, ok := tagValues.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value not found")
			return
		}
		var req CRMTagValue
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		if mask == "" || strings.Contains(mask, "description") {
			v.Description = req.Description
		}
		v.UpdateTime = nowTimestamp()
		v.Etag = crmEtag()
		tagValues.Put(name, v)
		sim.WriteJSON(w, http.StatusOK, crmLRO(v, typeTagValue, crmMetaUpdateTagValue))
	})
	srv.HandleFunc("DELETE /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagValues/" + sim.PathParam(r, "val")
		v, ok := tagValues.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value not found")
			return
		}
		tagValues.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(v, typeTagValue, crmMetaDeleteTagValue))
	})
	// tagValues tagHolds list/create live under tagValues/{val}/tagHolds.
	srv.HandleFunc("GET /v3/tagValues/{val}/tagHolds", func(w http.ResponseWriter, r *http.Request) {
		prefix := "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/"
		rows := tagHolds.Filter(func(h CRMTagHold) bool { return strings.HasPrefix(h.Name, prefix) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"tagHolds": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v3/tagValues/{val}/tagHolds", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagHold
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		h := CRMTagHold{
			Name:       "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/" + gcpNumericID(16),
			Holder:     req.Holder,
			Origin:     req.Origin,
			HelpLink:   req.HelpLink,
			CreateTime: nowTimestamp(),
		}
		tagHolds.Put(h.Name, h)
		sim.WriteJSON(w, http.StatusOK, crmLRO(h, typeTagHold, crmMetaCreateTagHold))
	})
	srv.HandleFunc("DELETE /v3/tagValues/{val}/tagHolds/{hold}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/" + sim.PathParam(r, "hold")
		if _, ok := tagHolds.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag hold not found")
			return
		}
		tagHolds.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(map[string]any{}, typeEmpty, crmMetaDeleteTagHold))
	})
	srv.HandleFunc("POST /v3/tagValues/{valAction}", func(w http.ResponseWriter, r *http.Request) {
		if crmIamVerb(w, r, resourcePolicies, sim.PathParam(r, "valAction"), "tagValue") {
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported tagValue action")
	})

	// ---- tagBindings (v3) ------------------------------------------------
	srv.HandleFunc("POST /v3/tagBindings", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagBinding
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		b := CRMTagBinding{
			Name:                   "tagBindings/" + gcpNumericID(16),
			Parent:                 req.Parent,
			TagValue:               req.TagValue,
			TagValueNamespacedName: req.TagValueNamespacedName,
		}
		tagBindings.Put(b.Name, b)
		sim.WriteJSON(w, http.StatusOK, crmLRO(b, typeTagBind, crmMetaCreateTagBinding))
	})
	srv.HandleFunc("GET /v3/tagBindings", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		rows := tagBindings.Filter(func(b CRMTagBinding) bool { return parent == "" || b.Parent == parent })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"tagBindings": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("DELETE /v3/tagBindings/{binding...}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagBindings/" + sim.PathParam(r, "binding")
		tagBindings.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(map[string]any{}, typeEmpty, crmMetaDeleteTagBinding))
	})

	// ---- effectiveTags (v3) ---------------------------------------------
	srv.HandleFunc("GET /v3/effectiveTags", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"effectiveTags": []any{}})
	})

	// ---- folders.capabilities (v3) --------------------------------------
	// A folder capability is a boolean feature toggle on the folder; the only
	// modeled one is the management capability. GET reads it, PATCH returns an
	// Operation (the API models the mutation as long-running).
	srv.HandleFunc("GET /v3/folders/{folder}/capabilities/{capability}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":  "folders/" + sim.PathParam(r, "folder") + "/capabilities/" + sim.PathParam(r, "capability"),
			"value": false,
		})
	})
	srv.HandleFunc("PATCH /v3/folders/{folder}/capabilities/{capability}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Value bool `json:"value"`
		}
		_ = sim.ReadJSON(r, &req)
		cap := map[string]any{
			"name":  "folders/" + sim.PathParam(r, "folder") + "/capabilities/" + sim.PathParam(r, "capability"),
			"value": req.Value,
		}
		sim.WriteJSON(w, http.StatusOK, crmLRO(cap, "type.googleapis.com/google.cloud.resourcemanager.v3.Capability", crmMetaUpdateCapability))
	})

	// ---- locations.tagBindingCollections (v3) ---------------------------
	// A tagBindingCollection is the full set of direct tag bindings on a
	// resource, keyed by its full-resource-name and addressable in a location.
	// GET reads it; PATCH (the only mutator) returns an Operation.
	srv.HandleFunc("GET /v3/locations/{location}/tagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":             "locations/" + sim.PathParam(r, "location") + "/tagBindingCollections/" + sim.PathParam(r, "collection"),
			"fullResourceName": "",
			"tags":             map[string]string{},
			"etag":             crmEtag(),
		})
	})
	srv.HandleFunc("PATCH /v3/locations/{location}/tagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string            `json:"fullResourceName"`
			Tags             map[string]string `json:"tags"`
		}
		_ = sim.ReadJSON(r, &req)
		coll := map[string]any{
			"name":             "locations/" + sim.PathParam(r, "location") + "/tagBindingCollections/" + sim.PathParam(r, "collection"),
			"fullResourceName": req.FullResourceName,
			"tags":             req.Tags,
			"etag":             crmEtag(),
		}
		sim.WriteJSON(w, http.StatusOK, crmLRO(coll, "type.googleapis.com/google.cloud.resourcemanager.v3.TagBindingCollection", crmMetaUpdateTagBindingCollection))
	})
	srv.HandleFunc("GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":             "locations/" + sim.PathParam(r, "location") + "/effectiveTagBindingCollections/" + sim.PathParam(r, "collection"),
			"fullResourceName": "",
			"effectiveTags":    map[string]string{},
		})
	})
}

// crmNamespaced renders a tagKey/tagValue namespaced name from its parent and
// short name (e.g. parent "organizations/123" + shortName "env" → "123/env").
func crmNamespaced(parent, shortName string) string {
	if parent == "" || shortName == "" {
		return ""
	}
	id := parent
	if i := strings.LastIndex(parent, "/"); i >= 0 {
		id = parent[i+1:]
	}
	return id + "/" + shortName
}

// saSystemKeyMu serialises first-use generation of a service account's
// system-managed key so two concurrent signs cannot each generate one and race
// to persist it, which would leave a signature verifiable against a key the
// store no longer holds.
var saSystemKeyMu sync.Mutex

// saSigningMaterial is a service account's system-managed key resolved for use:
// the private half signBlob and signJwt sign with, the identifier the key is
// published under, and the two encodings of the public half the IAM keys
// surface serves.
type saSigningMaterial struct {
	key            *rsa.PrivateKey
	keyID          string
	publicKeyPEM   string
	certificatePEM string
}

// serviceAccountSigningKey returns the system-managed key Google holds for a
// service account, generating and persisting it on first use. signBlob, signJwt
// and the keys surface all resolve the key through here, so the identifier a
// signature reports is the identifier the key is published under and the public
// half a client fetches verifies the signature it was given.
func serviceAccountSigningKey(saName, email string) (saSigningMaterial, error) {
	saSystemKeyMu.Lock()
	defer saSystemKeyMu.Unlock()

	var key *rsa.PrivateKey
	if rec, ok := iamSASystemKeys.Get(saName); ok {
		block, _ := pem.Decode([]byte(rec.PrivateKeyPEM))
		if block == nil {
			return saSigningMaterial{}, fmt.Errorf("persisted system-managed key for %s is not PEM", saName)
		}
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return saSigningMaterial{}, fmt.Errorf("parse persisted system-managed key for %s: %w", saName, err)
		}
		key = parsed
	} else {
		generated, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return saSigningMaterial{}, fmt.Errorf("generate system-managed key for %s: %w", saName, err)
		}
		iamSASystemKeys.Put(saName, serviceAccountSystemKey{
			Name: saName,
			PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(generated),
			})),
		})
		key = generated
	}

	id, err := serviceAccountKeyID(&key.PublicKey)
	if err != nil {
		return saSigningMaterial{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return saSigningMaterial{}, fmt.Errorf("marshal system-managed public key for %s: %w", saName, err)
	}
	certPEM, err := serviceAccountCertificatePEM(key, email)
	if err != nil {
		return saSigningMaterial{}, err
	}
	return saSigningMaterial{
		key:            key,
		keyID:          id,
		publicKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		certificatePEM: certPEM,
	}, nil
}

// serviceAccountKeyID derives the 40-hex-char identifier a service-account key
// is published under from its public half, so the id is stable for the life of
// the key and changes when the key does.
func serviceAccountKeyID(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal service-account public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:20]), nil
}

// serviceAccountPublicKeyData renders a key's published material in the
// encoding a `publicKeyType` names and returns it base64-encoded, which is how
// the IAM keys surface carries `publicKeyData`. An empty publicKeyType asks for
// no material and yields none, matching the API's default.
func serviceAccountPublicKeyData(publicKeyType, publicKeyPEM, certificatePEM string) (string, error) {
	switch publicKeyType {
	case "":
		return "", nil
	case "TYPE_RAW_PUBLIC_KEY":
		return base64.StdEncoding.EncodeToString([]byte(publicKeyPEM)), nil
	case "TYPE_X509_PEM_FILE":
		return base64.StdEncoding.EncodeToString([]byte(certificatePEM)), nil
	default:
		return "", fmt.Errorf("unknown publicKeyType %q", publicKeyType)
	}
}

// saCertificateEpoch fixes the validity window stamped into a service-account
// certificate so a key's certificate does not drift with the clock.
var saCertificateEpoch = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

// serviceAccountCertificatePEM wraps a service-account key in the self-signed
// X.509 certificate the TYPE_X509_PEM_FILE encoding carries, the way Google
// publishes a service account's certificate under its own email.
func serviceAccountCertificatePEM(key *rsa.PrivateKey, email string) (string, error) {
	id, err := serviceAccountKeyID(&key.PublicKey)
	if err != nil {
		return "", err
	}
	serialBytes, err := hex.DecodeString(id)
	if err != nil {
		return "", fmt.Errorf("decode key id %s: %w", id, err)
	}
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serialBytes),
		Subject:               pkix.Name{CommonName: email},
		Issuer:                pkix.Name{CommonName: email},
		NotBefore:             saCertificateEpoch,
		NotAfter:              saCertificateEpoch.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", fmt.Errorf("certify service-account key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// registerCustomRoles mounts the custom-role CRUD surface for one scope
// (projects or organizations). The route patterns differ only in the parent
// segment, so both scopes share these handlers. `scope` is "project" or
// "organization"; `parentPrefix` derives the resource-name prefix
// (projects/{p} / organizations/{o}).
func registerCustomRoles(srv *sim.Server, store sim.Store[GCPCustomRole], scope, listPat, getPat, createPat, actionPat, patchPat, deletePat string) {
	parentPrefix := func(parent string) string {
		if scope == "organization" {
			return "organizations/" + parent
		}
		return "projects/" + parent
	}

	// ListRoles
	srv.HandleFunc(listPat, func(w http.ResponseWriter, r *http.Request) {
		prefix := parentPrefix(sim.PathParam(r, "parent")) + "/roles/"
		// roles.list defaults to BASIC view (no includedPermissions);
		// view=FULL returns permissions. showDeleted controls whether
		// soft-deleted roles are returned.
		full := r.URL.Query().Get("view") == "FULL"
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		roles := store.Filter(func(role GCPCustomRole) bool {
			if !strings.HasPrefix(role.Name, prefix) {
				return false
			}
			return showDeleted || !role.Deleted
		})
		sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
		out := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			out = append(out, gcpCustomRoleJSON(role, full))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
	})

	// GetRole
	srv.HandleFunc(getPat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// CreateRole
	srv.HandleFunc(createPat, func(w http.ResponseWriter, r *http.Request) {
		parent := parentPrefix(sim.PathParam(r, "parent"))
		var req struct {
			RoleId string `json:"roleId"`
			Role   struct {
				Title               string   `json:"title"`
				Description         string   `json:"description"`
				IncludedPermissions []string `json:"includedPermissions"`
				Stage               string   `json:"stage"`
			} `json:"role"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.RoleId == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "roleId is required")
			return
		}
		name := parent + "/roles/" + req.RoleId
		if _, exists := store.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "A role named %s already exists.", name)
			return
		}
		stage := req.Role.Stage
		if stage == "" {
			stage = "ALPHA"
		}
		role := GCPCustomRole{
			Name:                name,
			Title:               req.Role.Title,
			Description:         req.Role.Description,
			IncludedPermissions: req.Role.IncludedPermissions,
			Stage:               stage,
			Etag:                gcpPolicyETag(),
		}
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// UndeleteRole — POST .../roles/{role}:undelete
	srv.HandleFunc(actionPat, func(w http.ResponseWriter, r *http.Request) {
		roleAction := sim.PathParam(r, "roleAction")
		roleID, action, found := strings.Cut(roleAction, ":")
		if !found || action != "undelete" {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported role action %q", roleAction)
			return
		}
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + roleID
		role, ok := store.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		role.Deleted = false
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// UpdateRole — PATCH with an updateMask over the mutable fields.
	srv.HandleFunc(patchPat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		var req GCPCustomRole
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Honor optimistic concurrency: a stale etag on the request body is
		// rejected with 409 ABORTED (same contract as setIamPolicy).
		if gcpIAMETagConflict(w, req.Etag, role.Etag, true) {
			return
		}
		// An empty updateMask updates every mutable field present in the body
		// (matches real GCP, which treats a missing mask as "full update").
		mask := r.URL.Query().Get("updateMask")
		fields := strings.Split(mask, ",")
		if mask == "" {
			fields = []string{"title", "description", "includedPermissions", "stage"}
		}
		for _, field := range fields {
			switch strings.TrimSpace(field) {
			case "title":
				role.Title = req.Title
			case "description":
				role.Description = req.Description
			case "includedPermissions":
				role.IncludedPermissions = req.IncludedPermissions
			case "stage":
				role.Stage = req.Stage
			}
		}
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// DeleteRole — soft-delete: set deleted=true and return the role.
	srv.HandleFunc(deletePat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		role.Deleted = true
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})
}

// gcpTestablePermissions returns the representative catalog of permissions the
// sim advertises as testable (includable in a custom role). It's the union of
// every permission referenced by the predefined roles plus the common
// service-prefixed permissions the repo's tests exercise. This bounds the
// catalog to what the sim can faithfully model rather than GCP's full ~7000.
func gcpTestablePermissions() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, role := range gcpPredefinedRoles() {
		for _, p := range role.IncludedPermissions {
			add(p)
		}
	}
	for _, p := range []string{
		"resourcemanager.projects.get",
		"resourcemanager.projects.update",
		"resourcemanager.projects.list",
		"resourcemanager.projects.setIamPolicy",
		"resourcemanager.projects.getIamPolicy",
		"storage.buckets.get",
		"storage.buckets.list",
		"storage.buckets.create",
		"storage.buckets.update",
		"storage.buckets.delete",
		"storage.buckets.getIamPolicy",
		"storage.buckets.setIamPolicy",
		"storage.objects.get",
		"storage.objects.list",
		"storage.objects.create",
		"storage.objects.delete",
		"storage.objects.update",
		"iam.serviceAccounts.actAs",
		"iam.serviceAccounts.get",
		"iam.serviceAccounts.list",
		"iam.serviceAccounts.create",
		"iam.serviceAccounts.delete",
		"iam.serviceAccounts.getIamPolicy",
		"iam.serviceAccounts.setIamPolicy",
		"iam.roles.get",
		"iam.roles.list",
		"iam.roles.create",
		"iam.roles.update",
		"iam.roles.delete",
	} {
		add(p)
	}
	sort.Strings(out)
	return out
}

// gcpCustomRoleJSON renders a custom role into the roles.get / roles.list wire
// shape. includedPermissions is omitted unless full (roles.list BASIC view
// carries no permissions; roles.get and create/update return FULL).
func gcpCustomRoleJSON(role GCPCustomRole, full bool) map[string]any {
	out := map[string]any{
		"name":  role.Name,
		"title": role.Title,
		"etag":  role.Etag,
	}
	if role.Description != "" {
		out["description"] = role.Description
	}
	if role.Stage != "" {
		out["stage"] = role.Stage
	}
	if role.Deleted {
		out["deleted"] = true
	}
	if full {
		out["includedPermissions"] = role.IncludedPermissions
	}
	return out
}

// gcpIAMETagConflict enforces the optimistic-concurrency contract real Cloud
// IAM applies on setIamPolicy: a request whose policy carries a non-empty etag
// that does not match the currently-stored policy's etag is rejected with 409
// ABORTED so the caller re-reads and retries. An empty request etag means the
// caller opted out of the check (a blind overwrite), which GCP permits.
func gcpIAMETagConflict(w http.ResponseWriter, reqEtag, currentEtag string, present bool) bool {
	if reqEtag == "" {
		return false
	}
	if !present || reqEtag != currentEtag {
		sim.GCPErrorf(w, http.StatusConflict, "ABORTED",
			"There were concurrent policy changes. Please retry the whole read-modify-write with exponential backoff.")
		return true
	}
	return false
}

// validateIAMMembers checks every member in every binding against the member
// syntax real Cloud IAM accepts, rejecting malformed members with an error the
// caller surfaces as 400 INVALID_ARGUMENT — matching real GCP, which rejects a
// setIamPolicy carrying a member like "robot@x.com" (no type prefix) or an
// unknown prefix. Typed members ("user:", "serviceAccount:", "group:",
// "domain:", "principal:", "principalSet:") must carry a non-empty identifier;
// "allUsers" and "allAuthenticatedUsers" are the only bare (untyped) members.
func validateIAMMembers(bindings []IAMBinding) error {
	typedPrefixes := []string{"user:", "serviceAccount:", "group:", "domain:", "principal:", "principalSet:"}
	for _, b := range bindings {
		for _, m := range b.Members {
			if m == "allUsers" || m == "allAuthenticatedUsers" {
				continue
			}
			matched := false
			for _, p := range typedPrefixes {
				if strings.HasPrefix(m, p) {
					id := strings.TrimPrefix(m, p)
					if id == "" {
						return fmt.Errorf("invalid member: %s", m)
					}
					// user:/serviceAccount:/group:/domain: carry an email or
					// domain; principal:/principalSet: carry an IAM resource
					// path. Require a structurally-plausible identifier for the
					// email/domain forms (a dot, e.g. "@example.com" or
					// "example.com") so a bare token like "user:bob" is rejected
					// as real GCP rejects it.
					switch p {
					case "user:", "serviceAccount:", "group:", "domain:":
						if !strings.Contains(id, ".") {
							return fmt.Errorf("invalid member: %s", m)
						}
					}
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("invalid member: %s", m)
			}
		}
	}
	return nil
}

// handleResourceIAM processes the three AIP-141 IAM verbs against a named
// resource: getIamPolicy / setIamPolicy / testIamPermissions. Every GCP
// resource type exposes this triple; the sim's per-resource handlers
// delegate the verb branch here.
func handleResourceIAM(w http.ResponseWriter, r *http.Request, store sim.Store[IAMPolicy], resource, action string) {
	switch action {
	case "getIamPolicy":
		policy, ok := store.Get(resource)
		if !ok {
			policy = IAMPolicy{
				Bindings: []IAMBinding{},
				Etag:     gcpPolicyETag(),
				Version:  1,
			}
			// Persist the synthesized default so its etag is stable across
			// reads — the optimistic-concurrency check on setIamPolicy
			// validates against the etag a prior getIamPolicy returned.
			store.Put(resource, policy)
		}
		sim.WriteJSON(w, http.StatusOK, policy)
	case "setIamPolicy":
		var req struct {
			Policy IAMPolicy `json:"policy"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := validateIAMMembers(req.Policy.Bindings); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		current, present := store.Get(resource)
		if gcpIAMETagConflict(w, req.Policy.Etag, current.Etag, present) {
			return
		}
		req.Policy.Etag = gcpPolicyETag()
		if req.Policy.Version == 0 {
			req.Policy.Version = 1
		}
		store.Put(resource, req.Policy)
		sim.WriteJSON(w, http.StatusOK, req.Policy)
	case "testIamPermissions":
		var req struct {
			Permissions []string `json:"permissions"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Sim doesn't model authorization; echo the requested set as
		// allowed. Real GCP filters to the subset the caller actually
		// has — but every caller in the sim is effectively a project
		// admin, so the full echo is the truthful response. A real-subset
		// evaluation against an authz model is a staged epic; the
		// admin-echo behavior is intentionally unchanged here.
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": req.Permissions})
	default:
		http.NotFound(w, r)
	}
}

// gcpResourceIAMStore returns the package-level resource-IAM store
// used by per-resource handlers. Centralises the cross-service IAM
// policy persistence so getIamPolicy / setIamPolicy round-trips
// match regardless of which resource type registered the policy.
func gcpResourceIAMStore() sim.Store[IAMPolicy] { return gcpResourcePolicies }

// gcpProjectFromEmail extracts the project ID from a GCP service account email.
// When the GCP API receives project="-" (a valid wildcard), the SDK resolves the
// project from the account email: {accountId}@{project}.iam.gserviceaccount.com.
func gcpProjectFromEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	host := email[at+1:]
	return strings.TrimSuffix(host, ".iam.gserviceaccount.com")
}

// gcpPredefinedRole is a curated (Google-managed) IAM role as returned by
// roles.get / roles.list.
type gcpPredefinedRole struct {
	Name                string
	Title               string
	Description         string
	IncludedPermissions []string
}

// gcpPredefinedRoles returns the bounded representative set of predefined roles
// the sim serves. Etag is deterministic per-role (these roles are immutable),
// so a roles.get round-trips a stable etag. This is intentionally a handful of
// roles, not the full GCP catalog.
func gcpPredefinedRoles() []gcpPredefinedRole {
	return []gcpPredefinedRole{
		{
			Name:        "roles/viewer",
			Title:       "Viewer",
			Description: "Read access to all resources.",
			IncludedPermissions: []string{
				"resourcemanager.projects.get",
				"storage.buckets.get",
				"storage.objects.get",
				"storage.objects.list",
			},
		},
		{
			Name:        "roles/editor",
			Title:       "Editor",
			Description: "Edit access to all resources.",
			IncludedPermissions: []string{
				"resourcemanager.projects.get",
				"storage.buckets.get",
				"storage.buckets.update",
				"storage.objects.create",
				"storage.objects.delete",
				"storage.objects.get",
				"storage.objects.list",
			},
		},
		{
			Name:        "roles/owner",
			Title:       "Owner",
			Description: "Full access to all resources.",
			IncludedPermissions: []string{
				"resourcemanager.projects.get",
				"resourcemanager.projects.setIamPolicy",
				"iam.serviceAccounts.create",
				"iam.serviceAccounts.delete",
				"storage.buckets.setIamPolicy",
			},
		},
		{
			Name:        "roles/iam.serviceAccountUser",
			Title:       "Service Account User",
			Description: "Run operations as the service account.",
			IncludedPermissions: []string{
				"iam.serviceAccounts.actAs",
				"iam.serviceAccounts.get",
				"iam.serviceAccounts.list",
			},
		},
		{
			Name:        "roles/storage.objectViewer",
			Title:       "Storage Object Viewer",
			Description: "Read access to GCS objects.",
			IncludedPermissions: []string{
				"storage.objects.get",
				"storage.objects.list",
			},
		},
	}
}

// gcpRoleJSON renders a predefined role into the roles.get / roles.list wire
// shape. includedPermissions is omitted unless full (roles.list defaults to
// BASIC view, which carries no permissions; roles.get returns FULL).
func gcpRoleJSON(role gcpPredefinedRole, full bool) map[string]any {
	out := map[string]any{
		"name":        role.Name,
		"title":       role.Title,
		"description": role.Description,
		"stage":       "GA",
		// Predefined roles are immutable; a deterministic etag keeps
		// roles.get idempotent across reads.
		"etag": base64.StdEncoding.EncodeToString([]byte(role.Name)),
	}
	if full {
		out["includedPermissions"] = role.IncludedPermissions
	}
	return out
}

// gcpNumericID returns a random decimal string of the given length, matching
// the shape of GCP's service-account uniqueId / client_id (a ~21-digit numeric
// principal identifier, not a hex UUID). The first digit is 1-9 so the value
// is a full-length number with no leading zero.
func gcpNumericID(digits int) string {
	if digits <= 0 {
		digits = 21
	}
	b := make([]byte, digits)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand on a healthy host does not fail; if it does the
		// process is in an unrecoverable state. Panic rather than emit a
		// predictable or zero-valued identifier.
		panic(fmt.Sprintf("gcpNumericID: read random: %v", err))
	}
	out := make([]byte, digits)
	out[0] = byte('1' + int(b[0])%9)
	for i := 1; i < digits; i++ {
		out[i] = byte('0' + int(b[i])%10)
	}
	return string(out)
}

// gcpMakeSAKeyJSON generates a real RSA-2048 key pair and returns the private
// half encoded as a base64 GCP service-account JSON credential file — matching
// the exact shape real GCP returns for CreateServiceAccountKey — together with
// the two published encodings of the public half. The caller registers those so
// the OAuth2 token endpoint can verify assertions signed with this key and the
// keys surface can serve either publicKeyType.
func gcpMakeSAKeyJSON(project, keyID, email, clientID string) (privateKeyData, publicKeyPEM, certificatePEM string, err error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", fmt.Errorf("generate RSA key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	certPEM, err := serviceAccountCertificatePEM(priv, email)
	if err != nil {
		return "", "", "", err
	}

	payload := map[string]string{
		"type":                        "service_account",
		"project_id":                  project,
		"private_key_id":              keyID,
		"private_key":                 string(privPEM),
		"client_email":                email,
		"client_id":                   clientID,
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + email,
		"universe_domain":             "googleapis.com",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal JSON key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), string(pubPEM), certPEM, nil
}

// iamLROs holds the long-running operations the IAM admin surface returns for
// its mutating verbs (pool/provider/oauth-client create/patch/delete/undelete).
// Each operation's GET endpoint lives under the resource path that produced it
// (e.g. .../workloadIdentityPools/{p}/operations/{op}), so the IAM operations
// are kept in their own store keyed by the full operation resource name.
var iamLROs sim.Store[Operation]

// iamResources is the generic store for every IAM admin resource the sim
// models as a JSON map (workload identity pools + their providers / namespaces
// / managed identities / keys, workforce pools + their providers / subjects /
// scim tenants / tokens / keys, and OAuth clients + credentials). Each row is
// the resource's wire JSON keyed by its fully-qualified resource name; soft-
// deleted resources keep their row (state=DELETED) so undelete can revive them.
var iamResources sim.Store[map[string]any]

// newIAMLRO settles a long-running operation synchronously (done=true) with the
// resource embedded as the protobuf Any response, stores it under
// {opCollection}/operations/{id}, and returns it. opCollection is the resource
// path the operation hangs off (e.g. the pool name), matching real GCP where a
// pool operation's name is .../workloadIdentityPools/{p}/operations/{id}.
func newIAMLRO(opCollection string, resource map[string]any, typeName string) Operation {
	if iamLROs == nil {
		// The operations store is created in registerIAM; a nil here means a
		// handler ran before registration, which never happens at runtime.
		panic("newIAMLRO: iamLROs store not initialized")
	}
	opID := generateUUID()
	response := map[string]any{"@type": typeName}
	for k, v := range resource {
		response[k] = v
	}
	var target string
	if n, ok := resource["name"].(string); ok {
		target = n
	}
	op := Operation{
		Name: opCollection + "/operations/" + opID,
		Metadata: map[string]any{
			"@type":      "type.googleapis.com/google.iam.admin.v1.OperationMetadata",
			"createTime": nowTimestamp(),
			"target":     target,
		},
		Done:     true,
		Response: response,
	}
	iamLROs.Put(op.Name, op)
	return op
}

// iamApplyMask copies the masked fields from src into dst. An empty mask copies
// every field present in src (real GCP treats a missing updateMask on these
// resources as a full update of the provided fields).
func iamApplyMask(dst, src map[string]any, mask string) {
	if strings.TrimSpace(mask) == "" {
		for k, v := range src {
			if k == "name" || k == "state" {
				continue
			}
			dst[k] = v
		}
		return
	}
	for _, f := range strings.Split(mask, ",") {
		f = strings.TrimSpace(f)
		if f == "" || f == "name" || f == "state" {
			continue
		}
		if v, ok := src[f]; ok {
			dst[f] = v
		} else {
			delete(dst, f)
		}
	}
}

// iamGenericCRUD wires the standard list/get/create/patch/delete/undelete +
// per-resource operations.get surface for one IAM admin collection. It is the
// shared engine behind workload-identity pools (and their nested providers /
// namespaces / managed identities / keys) and workforce pools (and their
// providers / subjects / scim tenants / tokens / keys).
//
//   - parentPat captures the collection's parent in the {parent...} param, so a
//     POST/GET-list resolves the parent name; createParam is the query
//     parameter carrying the new resource ID (e.g. workloadIdentityPoolId).
//   - childField is the list-response array field name (e.g.
//     "workloadIdentityPools").
//   - resType is the protobuf Any @type for this resource's LRO response.
//   - mutatingLRO controls whether create/patch/delete return an Operation
//     (true for pools/providers/identities) or the resource/Empty directly
//     (true for every IAM admin collection here; oauth clients differ and are
//     handled separately).
type iamCollection struct {
	srv         *sim.Server
	collPath    string // e.g. "/v1/projects/{project}/locations/{location}/workloadIdentityPools"
	parentName  func(r *http.Request) string
	childField  string
	createParam string
	resType     string
	stateField  bool // emit state=ACTIVE / DELETED
}

func (c iamCollection) name(r *http.Request, id string) string {
	return c.parentName(r) + "/" + lastCollSegment(c.collPath) + "/" + id
}

// lastCollSegment returns the trailing literal collection segment of a route
// pattern (e.g. "workloadIdentityPools" from ".../workloadIdentityPools").
func lastCollSegment(pat string) string {
	seg := pat[strings.LastIndex(pat, "/")+1:]
	return seg
}

func (c iamCollection) register() {
	// List
	c.srv.HandleFunc("GET "+c.collPath, func(w http.ResponseWriter, r *http.Request) {
		prefix := c.parentName(r) + "/" + lastCollSegment(c.collPath) + "/"
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			if !strings.HasPrefix(name, prefix) {
				return false
			}
			// only direct children (no extra "/" after the id)
			if strings.Contains(strings.TrimPrefix(name, prefix), "/") {
				return false
			}
			if !showDeleted && m["state"] == "DELETED" {
				return false
			}
			return true
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{c.childField: out})
	})

	// Get / listAttestationRules (colon-verb fan-in on the same path)
	c.srv.HandleFunc("GET "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		if base, verb, found := strings.Cut(id, ":"); found {
			if verb == "listAttestationRules" {
				name := c.name(r, base)
				if _, ok := iamResources.Get(name); !ok {
					sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
					return
				}
				sim.WriteJSON(w, http.StatusOK, map[string]any{"attestationRules": []any{}})
				return
			}
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported verb %q", verb)
			return
		}
		name := c.name(r, id)
		m, ok := iamResources.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})

	// Create (returns an LRO) / colon-verb POSTs (undelete, attestation rules)
	c.srv.HandleFunc("POST "+c.collPath, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get(c.createParam)
		if id == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s is required", c.createParam)
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := c.name(r, id)
		if _, exists := iamResources.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		if c.stateField {
			res["state"] = "ACTIVE"
		}
		iamResources.Put(name, res)
		op := newIAMLRO(name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Colon-verb POSTs on a specific resource: undelete (revives a soft-
	// deleted resource) and the attestation-rule mutators. All return an LRO.
	c.srv.HandleFunc("POST "+c.collPath+"/{idAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "idAction")
		id, verb, found := strings.Cut(idAction, ":")
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported request %q", idAction)
			return
		}
		name := c.name(r, id)
		switch verb {
		case "undelete":
			res, ok := iamResources.Get(name)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
				return
			}
			if c.stateField {
				res["state"] = "ACTIVE"
				iamResources.Put(name, res)
			}
			op := newIAMLRO(name, res, c.resType)
			sim.WriteJSON(w, http.StatusOK, op)
		case "addAttestationRule", "removeAttestationRule", "setAttestationRules":
			res, ok := iamResources.Get(name)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
				return
			}
			op := newIAMLRO(name, res, c.resType)
			sim.WriteJSON(w, http.StatusOK, op)
		case "getIamPolicy", "setIamPolicy", "testIamPermissions":
			// The pool/provider is itself an IAM resource; reuse the shared
			// resource-IAM store so its policy round-trips like any other.
			handleResourceIAM(w, r, gcpResourcePolicies, name, verb)
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported verb %q", verb)
		}
	})

	// Patch (returns an LRO)
	c.srv.HandleFunc("PATCH "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := c.name(r, sim.PathParam(r, "id"))
		res, ok := iamResources.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(name, res)
		op := newIAMLRO(name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Delete — soft-delete (state=DELETED) so undelete can revive it. Returns
	// an LRO whose embedded response is the deleted resource.
	c.srv.HandleFunc("DELETE "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := c.name(r, sim.PathParam(r, "id"))
		res, ok := iamResources.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		if c.stateField {
			res["state"] = "DELETED"
			iamResources.Put(name, res)
		} else {
			iamResources.Delete(name)
		}
		op := newIAMLRO(name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Per-resource operations.get
	c.srv.HandleFunc("GET "+c.collPath+"/{id}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		opName := c.name(r, sim.PathParam(r, "id")) + "/operations/" + sim.PathParam(r, "op")
		serveIAMOperation(w, opName)
	})
}

// serveIAMOperation returns a stored IAM LRO by name (or a settled, done=true
// operation if the name is well-formed but no record exists — real GCP keeps
// completed operations queryable; the sim settles synchronously so any
// operation it minted is in the store).
func serveIAMOperation(w http.ResponseWriter, opName string) {
	if op, ok := iamLROs.Get(opName); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", opName)
}

// registerWorkloadIdentityPools mounts the project-scoped Workload Identity
// Federation surface: pools, providers, namespaces, managed identities,
// provider keys, and the operations.get endpoints at every level.
func registerWorkloadIdentityPools(srv *sim.Server) {
	const base = "/v1/projects/{project}/locations/{location}"
	wipParent := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	}

	// Pools.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools",
		parentName:  wipParent,
		childField:  "workloadIdentityPools",
		createParam: "workloadIdentityPoolId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPool",
		stateField:  true,
	}.register()

	// Providers.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/providers",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool")
		},
		childField:  "workloadIdentityPoolProviders",
		createParam: "workloadIdentityPoolProviderId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolProvider",
		stateField:  true,
	}.register()

	// Provider keys.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/providers/{provider}/keys",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workloadIdentityPoolProviderKeys",
		createParam: "workloadIdentityPoolProviderKeyId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolProviderKey",
		stateField:  true,
	}.register()

	// Namespaces.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/namespaces",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool")
		},
		childField:  "workloadIdentityPoolNamespaces",
		createParam: "workloadIdentityPoolNamespaceId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolNamespace",
		stateField:  true,
	}.register()

	// Managed identities.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool") + "/namespaces/" + sim.PathParam(r, "namespace")
		},
		childField:  "workloadIdentityPoolManagedIdentities",
		createParam: "workloadIdentityPoolManagedIdentityId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolManagedIdentity",
		stateField:  true,
	}.register()

	// Workload-source operations.get — the only operations endpoint the
	// generic collection doesn't already mint (workloadSources has no CRUD in
	// the doc, just a nested operations.get).
	srv.HandleFunc("GET "+base+"/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities/{mi}/workloadSources/{ws}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		serveIAMOperation(w, fmt.Sprintf("projects/%s/locations/%s/workloadIdentityPools/%s/namespaces/%s/managedIdentities/%s/workloadSources/%s/operations/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "namespace"), sim.PathParam(r, "mi"), sim.PathParam(r, "ws"), sim.PathParam(r, "op")))
	})

	// managedIdentities :listAttestationRules — colon-verb fan-in handled by
	// the generic Get handler already (the {id} param captures "id:verb").
}

// registerWorkforcePools mounts the location-scoped (organization-level)
// workforce-pool surface: pools, providers, subjects, scim tenants, scim
// tokens, provider keys, and operations.get at the relevant levels.
func registerWorkforcePools(srv *sim.Server) {
	const base = "/v1/locations/{location}"
	wfpParent := func(r *http.Request) string {
		return "locations/" + sim.PathParam(r, "location")
	}

	// Pools.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools",
		parentName:  wfpParent,
		childField:  "workforcePools",
		createParam: "workforcePoolId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePool",
		stateField:  true,
	}.register()

	// Providers.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool")
		},
		childField:  "workforcePoolProviders",
		createParam: "workforcePoolProviderId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProvider",
		stateField:  true,
	}.register()

	// Provider keys.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/keys",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workforcePoolProviderKeys",
		createParam: "workforcePoolProviderKeyId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderKey",
		stateField:  true,
	}.register()

	// SCIM tenants.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/scimTenants",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workforcePoolProviderScimTenants",
		createParam: "workforcePoolProviderScimTenantId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderScimTenant",
		stateField:  true,
	}.register()

	// SCIM tokens.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/scimTenants/{tenant}/tokens",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider") + "/scimTenants/" + sim.PathParam(r, "tenant")
		},
		childField:  "workforcePoolProviderScimTokens",
		createParam: "workforcePoolProviderScimTokenId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderScimToken",
		stateField:  true,
	}.register()

	// Subjects — only delete (soft) + undelete + operations.get in the doc.
	srv.HandleFunc("DELETE "+base+"/workforcePools/{pool}/subjects/{subject}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("locations/%s/workforcePools/%s/subjects/%s",
			sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "subject"))
		res := map[string]any{"name": name, "state": "DELETED"}
		iamResources.Put(name, res)
		op := newIAMLRO(name, res, "type.googleapis.com/google.iam.v1.WorkforcePoolSubject")
		sim.WriteJSON(w, http.StatusOK, op)
	})
	srv.HandleFunc("GET "+base+"/workforcePools/{pool}/subjects/{subject}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		serveIAMOperation(w, fmt.Sprintf("locations/%s/workforcePools/%s/subjects/%s/operations/%s",
			sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "subject"), sim.PathParam(r, "op")))
	})
	// Pool-, provider-, and key-level operations.get are minted by the generic
	// collection registrations above (their per-resource operations.get).
}

// registerOAuthClients mounts the project-scoped OAuth-client surface. Unlike
// pools, oauthClients return the resource directly (not an Operation) on
// create/patch; delete returns the soft-deleted resource and credential delete
// returns Empty. Credentials carry a clientSecret only on the resource itself.
func registerOAuthClients(srv *sim.Server) {
	const base = "/v1/projects/{project}/locations/{location}/oauthClients"
	clientName := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "client"))
	}

	// List clients.
	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("projects/%s/locations/%s/oauthClients/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			return strings.HasPrefix(name, prefix) && !strings.Contains(strings.TrimPrefix(name, prefix), "/")
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"oauthClients": out})
	})

	// Get client.
	srv.HandleFunc("GET "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := iamResources.Get(clientName(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})

	// Create client — returns the resource directly.
	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("oauthClientId")
		if id == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "oauthClientId is required")
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
		if _, exists := iamResources.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		res["clientId"] = id
		res["state"] = "ACTIVE"
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Undelete client — POST .../oauthClients/{client}:undelete. Returns the
	// revived resource directly (oauthClients are not LRO-based).
	srv.HandleFunc("POST "+base+"/{clientAction}", func(w http.ResponseWriter, r *http.Request) {
		clientAction := sim.PathParam(r, "clientAction")
		id, verb, found := strings.Cut(clientAction, ":")
		if !found || verb != "undelete" {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported request %q", clientAction)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
		res, ok := iamResources.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		res["state"] = "ACTIVE"
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Patch client.
	srv.HandleFunc("PATCH "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(clientName(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(clientName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Delete client — soft-delete, returns the resource.
	srv.HandleFunc("DELETE "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(clientName(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		res["state"] = "DELETED"
		iamResources.Put(clientName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Credentials — list / get / create / patch / delete (Empty).
	credName := func(r *http.Request) string {
		return clientName(r) + "/credentials/" + sim.PathParam(r, "cred")
	}
	srv.HandleFunc("GET "+base+"/{client}/credentials", func(w http.ResponseWriter, r *http.Request) {
		prefix := clientName(r) + "/credentials/"
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			return strings.HasPrefix(name, prefix)
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"oauthClientCredentials": out})
	})
	srv.HandleFunc("GET "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := iamResources.Get(credName(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})
	srv.HandleFunc("POST "+base+"/{client}/credentials", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("oauthClientCredentialId")
		if id == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "oauthClientCredentialId is required")
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := clientName(r) + "/credentials/" + id
		if _, exists := iamResources.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		res["clientSecret"] = "sim-secret-" + generateUUID()
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})
	srv.HandleFunc("PATCH "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(credName(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(credName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})
	srv.HandleFunc("DELETE "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		if !iamResources.Delete(credName(r)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})
}

// --- IAM Credentials access-token lifetime ---
//
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

// parseGoogleDuration reads the `google-duration` JSON encoding — a decimal
// number of seconds, optionally fractional to nanosecond precision, with a
// trailing "s" — that proto3 gives google.protobuf.Duration.
func parseGoogleDuration(s string) (time.Duration, error) {
	body, ok := strings.CutSuffix(strings.TrimSpace(s), "s")
	if !ok {
		return 0, fmt.Errorf("duration %q does not end in 's'", s)
	}
	seconds, err := strconv.ParseFloat(body, 64)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not a number of seconds: %w", s, err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
