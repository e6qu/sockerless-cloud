package gcp_sdk_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/iam/v1"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// crmOpResourceName extracts the embedded resource's name field from a settled
// long-running Operation's response envelope (a json.RawMessage in the SDK).
func crmOpResourceName(t *testing.T, op *crm.Operation) string {
	t.Helper()
	require.NotEmpty(t, op.Response, "operation must carry a response")
	var m map[string]any
	require.NoError(t, json.Unmarshal(op.Response, &m))
	name, _ := m["name"].(string)
	return name
}

func crmV3Service(t *testing.T) *crm.Service {
	t.Helper()
	svc, err := crm.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestResourceManagerV3_FetchResourceSemanticsNameValidation pins how
// fetchResourceSemantics reads its fullResourceName. Semantics are optional
// metadata no API in this cloud slice assigns, so every resource answers with
// its own name and an empty map; what the method decides is whether the name it
// was handed is a full resource name at all, and that is what this covers.
func TestResourceManagerV3_FetchResourceSemanticsNameValidation(t *testing.T) {
	const fullName = "//cloudresourcemanager.googleapis.com/projects/735298346210"
	resp, err := simAuthHTTPClient().Get(baseURL + "/v3:fetchResourceSemantics?fullResourceName=" + url.QueryEscape(fullName))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct {
		FullResourceName string            `json:"fullResourceName"`
		Semantics        map[string]string `json:"semantics"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, fullName, got.FullResourceName)
	assert.Empty(t, got.Semantics, "no API in this slice assigns semantics, so none come back")

	// A full resource name is "//{service}/{resource path}". Each of these
	// breaks one part of that shape and must be refused rather than answered
	// with an empty semantics map.
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"absent", ""},
		{"empty", "?fullResourceName="},
		{"no leading slashes", "?fullResourceName=" + url.QueryEscape("cloudresourcemanager.googleapis.com/projects/1")},
		{"single leading slash", "?fullResourceName=" + url.QueryEscape("/cloudresourcemanager.googleapis.com/projects/1")},
		{"no service", "?fullResourceName=" + url.QueryEscape("///projects/1")},
		{"no resource path", "?fullResourceName=" + url.QueryEscape("//cloudresourcemanager.googleapis.com")},
		{"empty resource path", "?fullResourceName=" + url.QueryEscape("//cloudresourcemanager.googleapis.com/")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid, err := simAuthHTTPClient().Get(baseURL + "/v3:fetchResourceSemantics" + tc.query)
			require.NoError(t, err)
			defer invalid.Body.Close()
			invalidBody, err := io.ReadAll(invalid.Body)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, invalid.StatusCode, string(invalidBody))
			assert.Contains(t, string(invalidBody), "INVALID_ARGUMENT")
			assert.NotContains(t, string(invalidBody), "semantics",
				"a refused name must not also be answered")
		})
	}
}

func TestResourceManagerV3_ProjectLifecycle(t *testing.T) {
	svc := crmV3Service(t)

	// Create returns a settled long-running Operation embedding the project.
	op, err := svc.Projects.Create(&crm.Project{
		ProjectId:   "crm-proj-lifecycle",
		DisplayName: "CRM Lifecycle",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	got, err := svc.Projects.Get("projects/crm-proj-lifecycle").Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-proj-lifecycle", got.ProjectId)
	assert.Equal(t, "ACTIVE", got.State)

	// Patch displayName via updateMask.
	pop, err := svc.Projects.Patch(got.Name, &crm.Project{DisplayName: "Renamed"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	assert.True(t, pop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.DisplayName)

	// Move to a new parent.
	mop, err := svc.Projects.Move(got.Name, &crm.MoveProjectRequest{
		DestinationParent: "folders/000000000001",
	}).Do()
	require.NoError(t, err)
	assert.True(t, mop.Done)

	// List under the new parent surfaces the project.
	list, err := svc.Projects.List().Parent("folders/000000000001").Do()
	require.NoError(t, err)
	var found bool
	for _, p := range list.Projects {
		if p.ProjectId == "crm-proj-lifecycle" {
			found = true
		}
	}
	assert.True(t, found, "moved project should appear under new parent")

	// Search returns all projects.
	sr, err := svc.Projects.Search().Do()
	require.NoError(t, err)
	assert.NotEmpty(t, sr.Projects)

	// Delete then undelete round-trips state.
	dop, err := svc.Projects.Delete(got.Name).Do()
	require.NoError(t, err)
	assert.True(t, dop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", got.State)
	uop, err := svc.Projects.Undelete(got.Name, &crm.UndeleteProjectRequest{}).Do()
	require.NoError(t, err)
	assert.True(t, uop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State)
}

func TestResourceManagerV3_ProjectIAM(t *testing.T) {
	svc := crmV3Service(t)
	const name = "projects/crm-iam-proj"

	// IAM verbs address an existing project; a missing one is a real 403.
	cop, err := svc.Projects.Create(&crm.Project{ProjectId: "crm-iam-proj"}).Do()
	require.NoError(t, err)
	require.True(t, cop.Done)

	set, err := svc.Projects.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{
			Bindings: []*crm.Binding{{
				Role:    "roles/viewer",
				Members: []string{"user:alice@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.Projects.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/viewer", got.Bindings[0].Role)

	tip, err := svc.Projects.TestIamPermissions(name, &crm.TestIamPermissionsRequest{
		Permissions: []string{"resourcemanager.projects.get"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"resourcemanager.projects.get"}, tip.Permissions)
}

func TestResourceManagerV3_FolderLifecycle(t *testing.T) {
	svc := crmV3Service(t)

	op, err := svc.Folders.Create(&crm.Folder{
		DisplayName: "crm-folder",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	// The folder name lives in the operation response.
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-folder", got.DisplayName)
	assert.Equal(t, "ACTIVE", got.State)

	_, err = svc.Folders.Patch(name, &crm.Folder{DisplayName: "crm-folder-2"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	got, err = svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-folder-2", got.DisplayName)

	_, err = svc.Folders.Move(name, &crm.MoveFolderRequest{
		DestinationParent: "folders/000000000099",
	}).Do()
	require.NoError(t, err)

	list, err := svc.Folders.List().Parent("folders/000000000099").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Folders)

	sr, err := svc.Folders.Search().Do()
	require.NoError(t, err)
	assert.NotEmpty(t, sr.Folders)

	_, err = svc.Folders.Delete(name).Do()
	require.NoError(t, err)
	got, err = svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", got.State)
	_, err = svc.Folders.Undelete(name, &crm.UndeleteFolderRequest{}).Do()
	require.NoError(t, err)

	// Folder IAM.
	_, err = svc.Folders.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.folderViewer", Members: []string{"user:bob@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.Folders.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
}

func TestResourceManagerV3_Liens(t *testing.T) {
	svc := crmV3Service(t)

	lien, err := svc.Liens.Create(&crm.Lien{
		Parent:       "projects/crm-lien-proj",
		Restrictions: []string{"resourcemanager.projects.delete"},
		Reason:       "protect",
		Origin:       "test",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, lien.Name)
	assert.Equal(t, "protect", lien.Reason)

	got, err := svc.Liens.Get(lien.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, lien.Name, got.Name)

	list, err := svc.Liens.List().Parent("projects/crm-lien-proj").Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Liens)

	_, err = svc.Liens.Delete(lien.Name).Do()
	require.NoError(t, err)
	_, err = svc.Liens.Get(lien.Name).Do()
	assert.Error(t, err, "deleted lien should be gone")
}

func TestResourceManagerV3_TagKeys(t *testing.T) {
	svc := crmV3Service(t)

	op, err := svc.TagKeys.Create(&crm.TagKey{
		Parent:      "organizations/123456789012",
		ShortName:   "environment",
		Description: "deploy env",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.TagKeys.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "environment", got.ShortName)
	assert.Equal(t, "123456789012/environment", got.NamespacedName)

	ns, err := svc.TagKeys.GetNamespaced().Name("123456789012/environment").Do()
	require.NoError(t, err)
	assert.Equal(t, got.Name, ns.Name)

	list, err := svc.TagKeys.List().Parent("organizations/123456789012").Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.TagKeys)

	_, err = svc.TagKeys.Patch(name, &crm.TagKey{Description: "updated"}).
		UpdateMask("description").Do()
	require.NoError(t, err)
	got, err = svc.TagKeys.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Description)

	// TagKey IAM.
	_, err = svc.TagKeys.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.tagAdmin", Members: []string{"user:tag@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.TagKeys.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)

	_, err = svc.TagKeys.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.TagKeys.Get(name).Do()
	assert.Error(t, err)
}

func TestResourceManagerV3_TagValues(t *testing.T) {
	svc := crmV3Service(t)

	kop, err := svc.TagKeys.Create(&crm.TagKey{
		Parent:    "organizations/123456789012",
		ShortName: "tier",
	}).Do()
	require.NoError(t, err)
	keyName := crmOpResourceName(t, kop)

	op, err := svc.TagValues.Create(&crm.TagValue{
		Parent:    keyName,
		ShortName: "prod",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.TagValues.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "prod", got.ShortName)

	list, err := svc.TagValues.List().Parent(keyName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.TagValues)

	// TagValue IAM.
	_, err = svc.TagValues.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.tagUser", Members: []string{"user:tv@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.TagValues.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
}

// TestResourceManagerV3_TagsInForceAreTheOnesBound drives the reads that report
// which tags apply to a resource. Each of them answered a fixed empty answer:
// effectiveTags for a resource with a binding, a binding collection whose
// PATCH stored nothing, and a folder capability whose read always said false.
// Every one of those contradicts the write that came before it.
func TestResourceManagerV3_TagsInForceAreTheOnesBound(t *testing.T) {
	svc := crmV3Service(t)

	kop, err := svc.TagKeys.Create(&crm.TagKey{
		Parent:    "organizations/123456789012",
		ShortName: "inforce",
	}).Do()
	require.NoError(t, err)
	keyName := crmOpResourceName(t, kop)

	vop, err := svc.TagValues.Create(&crm.TagValue{Parent: keyName, ShortName: "yes"}).Do()
	require.NoError(t, err)
	valueName := crmOpResourceName(t, vop)

	const resource = "//cloudresourcemanager.googleapis.com/projects/735298346299"
	_, err = svc.TagBindings.Create(&crm.TagBinding{
		Parent: resource, TagValue: valueName,
	}).Do()
	require.NoError(t, err)

	// The tag bound a moment ago is in force on the resource it was bound to.
	inForce, err := svc.EffectiveTags.List().Parent(resource).Do()
	require.NoError(t, err)
	require.Len(t, inForce.EffectiveTags, 1)
	assert.Equal(t, valueName, inForce.EffectiveTags[0].TagValue)
	assert.False(t, inForce.EffectiveTags[0].Inherited)

	// A resource nothing was bound to has none.
	none, err := svc.EffectiveTags.List().
		Parent("//cloudresourcemanager.googleapis.com/projects/735298346298").Do()
	require.NoError(t, err)
	assert.Empty(t, none.EffectiveTags)

	// A collection reports the tags it was told to hold. Its name carries the
	// resource, percent-encoded, so the read knows what it is about.
	collection := "locations/global/tagBindingCollections/" + url.PathEscape(resource)
	_, err = svc.Locations.TagBindingCollections.Patch(collection, &crm.TagBindingCollection{
		FullResourceName: resource,
		Tags:             map[string]string{"123456789012/inforce": "yes"},
	}).Do()
	require.NoError(t, err)

	held, err := svc.Locations.TagBindingCollections.Get(collection).Do()
	require.NoError(t, err)
	assert.Equal(t, resource, held.FullResourceName)
	assert.Equal(t, map[string]string{"123456789012/inforce": "yes"}, held.Tags)

	effective, err := svc.Locations.EffectiveTagBindingCollections.Get(
		"locations/global/effectiveTagBindingCollections/" + url.PathEscape(resource)).Do()
	require.NoError(t, err)
	assert.Equal(t, resource, effective.FullResourceName)
	assert.Equal(t, "yes", effective.EffectiveTags["123456789012/inforce"])

	// A folder capability reports the value it was set to.
	const capability = "folders/123456789012/capabilities/app-management"
	_, err = svc.Folders.Capabilities.Patch(capability, &crm.Capability{Value: true}).Do()
	require.NoError(t, err)
	got, err := svc.Folders.Capabilities.Get(capability).Do()
	require.NoError(t, err)
	assert.True(t, got.Value, "a capability read must report the value a patch set")
}

// TestIAMCredentials_SignBlobAndJwt drives the two IAM Credentials signing
// methods the way a relying party drives them: sign, then fetch the public half
// of the named key from the IAM service-account keys surface and verify the
// signature against it. That is the whole point of a signature — a caller that
// only checked the encoding of the bytes would accept any bytes at all.
func TestIAMCredentials_SignBlobAndJwt(t *testing.T) {
	// The credential ops require the service account to exist; create it via
	// the IAM SDK first (same flow a real caller's IaC would have run).
	iamSvc := iamService(t)
	_, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "signer",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Signer SA"},
		}).Do()
	require.NoError(t, err)

	svc := iamCredentialsService(t)
	const name = "projects/-/serviceAccounts/signer@test-project.iam.gserviceaccount.com"

	payload := []byte("hello-sign")
	blob, err := svc.Projects.ServiceAccounts.SignBlob(name, &iamcredentials.SignBlobRequest{
		Payload: base64.StdEncoding.EncodeToString(payload),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, blob.KeyId)
	require.NotEmpty(t, blob.SignedBlob)
	signature, derr := base64.StdEncoding.DecodeString(blob.SignedBlob)
	require.NoError(t, derr, "signedBlob must be valid base64")

	// The key the signature names must be the account's system-managed key,
	// the one Google holds and never hands out, published on the keys surface.
	pub := systemManagedPublicKey(t, iamSvc, name, blob.KeyId)

	blobDigest := sha256.Sum256(payload)
	require.NoError(t,
		rsa.VerifyPKCS1v15(pub, crypto.SHA256, blobDigest[:], signature),
		"signedBlob must verify as RSASSA-PKCS1-v1_5 over SHA-256 of the payload, under the reported keyId")

	claims := map[string]string{
		"sub": "signer@test-project.iam.gserviceaccount.com",
		"aud": "https://example.googleapis.com/",
		"iss": "signer@test-project.iam.gserviceaccount.com",
	}
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	jwt, err := svc.Projects.ServiceAccounts.SignJwt(name, &iamcredentials.SignJwtRequest{
		Payload: string(claimsJSON),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, jwt.KeyId)
	require.NotEmpty(t, jwt.SignedJwt)

	parts := strings.Split(jwt.SignedJwt, ".")
	require.Len(t, parts, 3, "signedJwt must be a 3-part JWS")

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err, "the JWS header must be base64url without padding")
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	assert.Equal(t, "RS256", header.Alg)
	assert.Equal(t, "JWT", header.Typ)
	assert.Equal(t, jwt.KeyId, header.Kid,
		"the header's kid must name the key the response reports, so a verifier can find the public half")

	// The claim set is the caller's, passed through untouched: signJwt signs
	// what it was given rather than composing claims of its own.
	claimsSegment, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "the JWS claims must be base64url without padding")
	var roundTripped map[string]string
	require.NoError(t, json.Unmarshal(claimsSegment, &roundTripped))
	assert.Equal(t, claims, roundTripped)

	jwtPub := systemManagedPublicKey(t, iamSvc, name, jwt.KeyId)
	jwtDigest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	jwtSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err, "the JWS signature must be base64url without padding")
	require.NoError(t,
		rsa.VerifyPKCS1v15(jwtPub, crypto.SHA256, jwtDigest[:], jwtSig),
		"signedJwt must verify as RS256 over its own signing input, under the reported keyId")
}

// systemManagedPublicKey resolves the public half of a service account's
// system-managed key from the IAM keys surface — list the account's
// system-managed keys, find the one the signature named, and read its
// publicKeyData. This is the route a relying party takes to verify a signature
// it was handed, and it is the only way to tell a real signature from arbitrary
// bytes.
func systemManagedPublicKey(t *testing.T, svc *iam.Service, saName, keyID string) *rsa.PublicKey {
	t.Helper()

	list, err := svc.Projects.ServiceAccounts.Keys.List(saName).KeyTypes("SYSTEM_MANAGED").Do()
	require.NoError(t, err)
	var keyName string
	for _, key := range list.Keys {
		assert.Equal(t, "SYSTEM_MANAGED", key.KeyType,
			"a keyTypes=SYSTEM_MANAGED listing must not carry user-managed keys")
		if strings.HasSuffix(key.Name, "/keys/"+keyID) {
			keyName = key.Name
			assert.Equal(t, "KEY_ALG_RSA_2048", key.KeyAlgorithm)
		}
	}
	require.NotEmpty(t, keyName,
		"the signature's keyId %q must name one of the account's published keys", keyID)

	// publicKeyData is served only when the caller names the encoding it wants,
	// as the real API serves it.
	key, err := svc.Projects.ServiceAccounts.Keys.Get(keyName).PublicKeyType("TYPE_RAW_PUBLIC_KEY").Do()
	require.NoError(t, err)
	require.NotEmpty(t, key.PublicKeyData, "the published key must carry its public half")

	raw, err := base64.StdEncoding.DecodeString(key.PublicKeyData)
	require.NoError(t, err, "publicKeyData must be base64")
	block, _ := pem.Decode(raw)
	require.NotNil(t, block, "TYPE_RAW_PUBLIC_KEY carries a PEM public key: %s", raw)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err, "the published public key must be PKIX DER inside its PEM block")
	pub, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok, "a KEY_ALG_RSA_2048 key must publish an RSA public key, got %T", parsed)
	return pub
}
