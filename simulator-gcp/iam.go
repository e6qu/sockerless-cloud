package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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

// iamServiceAccountRetention is how long IAM keeps a deleted service account
// before removing it permanently; undelete restores it only inside the window.
const iamServiceAccountRetention = 30 * 24 * time.Hour

// gcpDeletedServiceAccount is a deleted service account IAM still holds, keyed
// by its unique ID because a new account may take the same email meanwhile. It
// carries what undelete restores with the account: its keys, its
// system-managed key and the IAM policy set on it.
type gcpDeletedServiceAccount struct {
	Account    GCPServiceAccount              `json:"account"`
	DeleteTime time.Time                      `json:"deleteTime"`
	Keys       []GCPServiceAccountKey         `json:"keys,omitempty"`
	KeyPublics []GCPServiceAccountKeyMaterial `json:"keyPublics,omitempty"`
	SystemKey  *serviceAccountSystemKey       `json:"systemKey,omitempty"`
	Policy     *IAMPolicy                     `json:"policy,omitempty"`
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
	Kind         string           `json:"kind,omitempty"`
	ResourceId   string           `json:"resourceId,omitempty"`
	Bindings     []IAMBinding     `json:"bindings"`
	AuditConfigs []IAMAuditConfig `json:"auditConfigs,omitempty"`
	Etag         string           `json:"etag"`
	Version      int              `json:"version"`
}

// IAMAuditConfig names the Data Access audit log types a service writes for
// the resource, and the members whose calls it leaves out of them.
type IAMAuditConfig struct {
	Service         string              `json:"service"`
	AuditLogConfigs []IAMAuditLogConfig `json:"auditLogConfigs,omitempty"`
}

type IAMAuditLogConfig struct {
	LogType         string   `json:"logType"`
	ExemptedMembers []string `json:"exemptedMembers,omitempty"`
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
// KeyOrigin records who supplied the key material: GOOGLE_PROVIDED for keys
// the service generated, USER_PROVIDED for keys uploaded by the caller.
// Disabled is the bit keys.enable / keys.disable flip; a disabled key stops
// authenticating at the OAuth2 token endpoint until re-enabled.
type GCPServiceAccountKey struct {
	Name            string `json:"name"`
	KeyAlgorithm    string `json:"keyAlgorithm"`
	ValidAfterTime  string `json:"validAfterTime"`
	ValidBeforeTime string `json:"validBeforeTime"`
	KeyType         string `json:"keyType"`
	KeyOrigin       string `json:"keyOrigin,omitempty"`
	Disabled        bool   `json:"disabled,omitempty"`
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

// iamServiceAccounts and iamSAKeyPublics expose the service-account and
// key-material stores to the OAuth2 token endpoint (registered separately in
// oauth2.go), which resolves a JWT-bearer assertion's issuer to a registered
// account and verifies the assertion signature against that account's keys.
// Both are assigned once in registerIAM.
var (
	iamServiceAccounts sim.Store[GCPServiceAccount]
	// iamDeletedServiceAccounts holds deleted accounts by unique ID for the
	// retention window.
	iamDeletedServiceAccounts sim.Store[gcpDeletedServiceAccount]
	iamSAKeys                 sim.Store[GCPServiceAccountKey]
	iamSAKeyPublics           sim.Store[GCPServiceAccountKeyMaterial]
	iamSASystemKeys           sim.Store[serviceAccountSystemKey]
	// iamSessionRevocations holds, per workforce principal, the Unix second at
	// or before which every access token issued to it is revoked.
	iamSessionRevocations sim.Store[int64]
	// iamCustomRoles is read outside registerIAM by the permission check, which
	// resolves a binding's role to the permissions it includes.
	iamCustomRoles sim.Store[GCPCustomRole]
)

func registerIAM(srv *sim.Server) {
	serviceAccounts := sim.MakeStore[GCPServiceAccount](srv.DB(), "iam_service_accounts")
	saKeys := sim.MakeStore[GCPServiceAccountKey](srv.DB(), "iam_sa_keys")
	saKeyPublics := sim.MakeStore[GCPServiceAccountKeyMaterial](srv.DB(), "iam_sa_key_publics")
	iamServiceAccounts = serviceAccounts
	iamDeletedServiceAccounts = sim.MakeStore[gcpDeletedServiceAccount](srv.DB(), "iam_deleted_service_accounts")
	iamSAKeys = saKeys
	iamSAKeyPublics = saKeyPublics
	iamSASystemKeys = sim.MakeStore[serviceAccountSystemKey](srv.DB(), "iam_sa_system_keys")
	iamSessionRevocations = sim.MakeStore[int64](srv.DB(), "iam_workforce_session_revocations")
	projectPolicies := sim.MakeStore[IAMPolicy](srv.DB(), "iam_project_policies")
	gcpProjectPolicies = projectPolicies
	gcpResourcePolicies = sim.MakeStore[IAMPolicy](srv.DB(), "iam_resource_policies")
	resourcePolicies := gcpResourcePolicies
	customRoles := sim.MakeStore[GCPCustomRole](srv.DB(), "iam_custom_roles")
	iamCustomRoles = customRoles
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
	registerIAMServiceAccounts(srv, serviceAccounts, saKeys, saKeyPublics, resourcePolicies)
	registerIAMServiceAccountKeys(srv, serviceAccounts, saKeys, saKeyPublics)
	registerIAMCatalog(srv, customRoles)

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
			if cloudBillingUnimplementedTaskWrite(w, r, resource) {
				return
			}
			http.NotFound(w, r)
			return
		}
		handleResourceIAM(w, r, resourcePolicies, resource, action)
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

	// Bucket IAM - getIamPolicy
	srv.HandleFunc("GET /storage/v1/b/{bucket}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if _, exists := gcsBuckets.Get(bucket); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return
		}

		policy, ok := resourcePolicies.Get("bucket/" + bucket)
		if !ok {
			policy = gcsSeedDefaultBucketPolicy(bucket)
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = "projects/_/buckets/" + bucket
		sim.WriteJSON(w, http.StatusOK, policy)
	})

	// Bucket IAM - setIamPolicy
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if _, exists := gcsBuckets.Get(bucket); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return
		}

		var policy IAMPolicy
		if err := sim.ReadJSON(r, &policy); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// A policy is its bindings; the document requires them of this method,
		// and a set with none is a set of nothing rather than a policy that
		// grants nobody.
		if len(policy.Bindings) == 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the policy must carry at least one binding")
			return
		}
		if err := validateIAMMembers(policy.Bindings); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}

		gcpIAMPolicyWrites.Lock()
		defer gcpIAMPolicyWrites.Unlock()
		current, present := resourcePolicies.Get("bucket/" + bucket)
		// Cloud Storage refuses a stale etag as its own failed precondition,
		// 412, which gsutil and gcloud retry on; Resource Manager's 409 ABORTED
		// is not its answer.
		if policy.Etag != "" && (!present || policy.Etag != current.Etag) {
			writeGCSPreconditionFailed(w)
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
		GCPErrorf(w, http.StatusConflict, "ABORTED",
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
	typedPrefixes := []string{"user:", "serviceAccount:", "group:", "domain:", "principal:", "principalSet:",
		// The project convenience members Cloud Storage grants a bucket's
		// legacy roles to at creation.
		"projectOwner:", "projectEditor:", "projectViewer:"}
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
// gcpIAMPolicyWrites serializes each policy read-check-write here, so two
// setIamPolicy calls carrying the same etag cannot both pass the check and the
// later one silently drop the earlier one's bindings, and a getIamPolicy that
// seeds the default cannot overwrite a policy set in between.
var gcpIAMPolicyWrites sync.Mutex

func handleResourceIAM(w http.ResponseWriter, r *http.Request, store sim.Store[IAMPolicy], resource, action string) {
	switch action {
	case "getIamPolicy":
		gcpIAMPolicyWrites.Lock()
		defer gcpIAMPolicyWrites.Unlock()
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
			Policy     IAMPolicy `json:"policy"`
			UpdateMask string    `json:"updateMask"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := validateIAMMembers(req.Policy.Bindings); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if err := validateIAMAuditConfigs(req.Policy.AuditConfigs); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		gcpIAMPolicyWrites.Lock()
		defer gcpIAMPolicyWrites.Unlock()
		current, present := store.Get(resource)
		if gcpIAMETagConflict(w, req.Policy.Etag, current.Etag, present) {
			return
		}
		// The default mask is "bindings,etag": a policy's auditConfigs change
		// only when the mask names them.
		if !slices.Contains(strings.Split(strings.ReplaceAll(req.UpdateMask, " ", ""), ","), "auditConfigs") {
			req.Policy.AuditConfigs = current.AuditConfigs
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		policy, _ := store.Get(resource)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"permissions": gcpAnswerTestIamPermissions(r, resource, policy, req.Permissions, gcpIAMResourceNamed(resource))})
	default:
		http.NotFound(w, r)
	}
}

// iamAuditLogTypes are the Data Access audit log types an auditLogConfig
// enables.
var iamAuditLogTypes = []string{"ADMIN_READ", "DATA_READ", "DATA_WRITE"}

func validateIAMAuditConfigs(configs []IAMAuditConfig) error {
	for i, config := range configs {
		if config.Service == "" {
			return fmt.Errorf("policy.auditConfigs[%d].service is required", i)
		}
		for j, logConfig := range config.AuditLogConfigs {
			if !slices.Contains(iamAuditLogTypes, logConfig.LogType) {
				return fmt.Errorf("policy.auditConfigs[%d].auditLogConfigs[%d].logType %q is not one of %v", i, j, logConfig.LogType, iamAuditLogTypes)
			}
			if err := validateIAMMembers([]IAMBinding{{Members: logConfig.ExemptedMembers}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// gcpResourceIAMStore returns the package-level resource-IAM store
// used by per-resource handlers. Centralises the cross-service IAM
// policy persistence so getIamPolicy / setIamPolicy round-trips
// match regardless of which resource type registered the policy.
func gcpResourceIAMStore() sim.Store[IAMPolicy] { return gcpResourcePolicies }

// gcpProjectFromEmail extracts the project ID from a GCP service account email.
// When the GCP API receives project="-" (a valid wildcard), the SDK resolves the
// project from the account email: {accountId}@{project}.iam.gserviceaccount.com,
// or, for Compute Engine's default service account,
// {projectNumber}-compute@developer.gserviceaccount.com.
func gcpProjectFromEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	local, host := email[:at], email[at+1:]
	if host == computeDefaultServiceAccountDomain {
		number, ok := strings.CutSuffix(local, "-compute")
		if !ok {
			return ""
		}
		p, found := crmResolveProject(number)
		if !found {
			return ""
		}
		return p.ProjectId
	}
	return strings.TrimSuffix(host, ".iam.gserviceaccount.com")
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
