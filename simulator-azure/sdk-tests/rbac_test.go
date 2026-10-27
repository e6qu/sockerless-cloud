package azure_sdk_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	readerRoleID                    = "acdd72a7-3385-48ef-bd42-f606fba81ae7"
	contributorRoleID               = "b24988ac-6180-42a0-ab88-20f7382dd24c"
	storageBlobDataReaderRoleID     = "2a2b9908-6ea1-4ae2-8e65-a410df84e7d1"
	storageBlobDataContributorRole  = "ba92f5b4-2d11-453d-a403-e96b0029c9fe"
	eventGridDataSenderRoleID       = "d5a91429-5739-47e2-a06b-3470a27159e7"
	rbacAuthorizationFailedFragment = "does not have authorization to perform action"
)

// testServicePrincipal is an application registration with a service
// principal and a client secret, created through Microsoft Graph as an
// operator creates one for a workload.
type testServicePrincipal struct {
	appObjectID, appID, objectID, secret string
}

func (p testServicePrincipal) credential() *appSecretCredential {
	return &appSecretCredential{clientID: p.appID, clientSecret: p.secret}
}

// rbacGraph sends one Microsoft Graph request as the bootstrap principal.
func rbacGraph(t *testing.T, method, path string, body any, want int) map[string]any {
	t.Helper()
	token, _, err := fetchSimAccessToken("https://graph.microsoft.com/.default")
	require.NoError(t, err)
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, payload)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, want, resp.StatusCode, "%s %s: %s", method, path, data)
	out := map[string]any{}
	if len(data) > 0 {
		require.NoError(t, json.Unmarshal(data, &out))
	}
	return out
}

func newTestServicePrincipal(t *testing.T, name string) testServicePrincipal {
	t.Helper()
	app := rbacGraph(t, http.MethodPost, "/v1.0/applications", map[string]any{"displayName": name}, http.StatusCreated)
	p := testServicePrincipal{}
	p.appObjectID, _ = app["id"].(string)
	p.appID, _ = app["appId"].(string)
	t.Cleanup(func() {
		rbacGraph(t, http.MethodDelete, "/v1.0/applications/"+p.appObjectID, nil, http.StatusNoContent)
	})
	sp := rbacGraph(t, http.MethodPost, "/v1.0/servicePrincipals", map[string]any{"appId": p.appID}, http.StatusCreated)
	p.objectID, _ = sp["id"].(string)
	secret := rbacGraph(t, http.MethodPost, "/v1.0/applications/"+p.appObjectID+"/addPassword",
		map[string]any{"passwordCredential": map[string]any{"displayName": "rbac"}}, http.StatusOK)
	p.secret, _ = secret["secretText"].(string)
	require.NotEmpty(t, p.secret)
	return p
}

// assignRole assigns a built-in or custom role definition to a principal at a
// scope as the bootstrap principal, and returns the assignment's ID.
func assignRole(t *testing.T, scope, roleDefinitionID, principalID string) string {
	t.Helper()
	client, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created, err := client.Create(ctx, scope, newRBACGUID(), armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			RoleDefinitionID: to.Ptr("/subscriptions/" + subscriptionID + "/providers/Microsoft.Authorization/roleDefinitions/" + roleDefinitionID),
			PrincipalID:      to.Ptr(principalID),
			PrincipalType:    to.Ptr(armauthorization.PrincipalTypeServicePrincipal),
		},
	}, nil)
	require.NoError(t, err)
	id := *created.ID
	t.Cleanup(func() { _, _ = client.DeleteByID(ctx, id, nil) })
	return id
}

func newRBACGUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// requireAuthorizationFailed asserts Azure Resource Manager refused an
// operation with 403 AuthorizationFailed naming the action it checked.
func requireAuthorizationFailed(t *testing.T, err error, action string) {
	t.Helper()
	require.Error(t, err)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, http.StatusForbidden, respErr.StatusCode)
	assert.Equal(t, "AuthorizationFailed", respErr.ErrorCode)
	assert.Contains(t, err.Error(), rbacAuthorizationFailedFragment)
	assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower("'"+action+"'"))
}

// TestRBAC_ReaderReadsButCannotWriteUntilContributor proves Azure Resource
// Manager evaluates the caller's role assignments: Reader on a resource group
// reads it and its resources but refuses creates and deletes, and Contributor
// on the same group admits them while still refusing role assignment writes.
func TestRBAC_ReaderReadsButCannotWriteUntilContributor(t *testing.T) {
	t.Parallel()
	const group, other = "rbac-reader-rg", "rbac-reader-other-rg"
	createResourceGroup(t, group)
	createResourceGroup(t, other)
	ownerIdentities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = ownerIdentities.CreateOrUpdate(ctx, group, "rbac-existing", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	principal := newTestServicePrincipal(t, "rbac-reader")
	groupScope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + group
	assignRole(t, groupScope, readerRoleID, principal.objectID)

	cred := principal.credential()
	groups, err := armresources.NewResourceGroupsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	got, err := groups.Get(ctx, group, nil)
	require.NoError(t, err, "Reader on the group reads it")
	assert.Equal(t, group, *got.Name)
	_, err = identities.Get(ctx, group, "rbac-existing", nil)
	require.NoError(t, err, "Reader on the group reads its resources")

	// The subscription-wide list answers with the groups the caller may read.
	var listed []string
	pager := groups.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, g := range page.Value {
			listed = append(listed, *g.Name)
		}
	}
	assert.Contains(t, listed, group)
	assert.NotContains(t, listed, other)

	_, err = groups.Get(ctx, other, nil)
	requireAuthorizationFailed(t, err, "Microsoft.Resources/subscriptions/resourceGroups/read")

	_, err = identities.CreateOrUpdate(ctx, group, "rbac-new", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	requireAuthorizationFailed(t, err, "Microsoft.ManagedIdentity/userAssignedIdentities/write")
	assert.Contains(t, err.Error(), principal.objectID)
	assert.Contains(t, err.Error(), groupScope+"/providers/Microsoft.ManagedIdentity/userAssignedIdentities/rbac-new")

	_, err = identities.Delete(ctx, group, "rbac-existing", nil)
	requireAuthorizationFailed(t, err, "Microsoft.ManagedIdentity/userAssignedIdentities/delete")

	assignRole(t, groupScope, contributorRoleID, principal.objectID)
	_, err = identities.CreateOrUpdate(ctx, group, "rbac-new", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err, "Contributor on the group creates in it")
	_, err = identities.Delete(ctx, group, "rbac-new", nil)
	require.NoError(t, err, "Contributor on the group deletes in it")

	// Contributor's notActions exclude Microsoft.Authorization writes.
	assignments, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	_, err = assignments.Create(ctx, groupScope, newRBACGUID(), armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			RoleDefinitionID: to.Ptr("/subscriptions/" + subscriptionID + "/providers/Microsoft.Authorization/roleDefinitions/" + readerRoleID),
			PrincipalID:      to.Ptr(principal.objectID),
		},
	}, nil)
	requireAuthorizationFailed(t, err, "Microsoft.Authorization/roleAssignments/write")
}

// TestRBAC_NotActionsExcludeAnOperation proves a permission entry's
// notActions subtract from its actions: a custom role granting every action
// but deleting identities creates an identity and is refused its delete.
func TestRBAC_NotActionsExcludeAnOperation(t *testing.T) {
	t.Parallel()
	const group = "rbac-notactions-rg"
	createResourceGroup(t, group)
	groupScope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + group

	definitions, err := armauthorization.NewRoleDefinitionsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	roleID := newRBACGUID()
	_, err = definitions.CreateOrUpdate(ctx, "/subscriptions/"+subscriptionID, roleID, armauthorization.RoleDefinition{
		Properties: &armauthorization.RoleDefinitionProperties{
			RoleName: to.Ptr("RBAC NotActions " + roleID),
			RoleType: to.Ptr("CustomRole"),
			Permissions: []*armauthorization.Permission{{
				Actions:    []*string{to.Ptr("*")},
				NotActions: []*string{to.Ptr("Microsoft.ManagedIdentity/userAssignedIdentities/delete")},
			}},
			AssignableScopes: []*string{to.Ptr("/subscriptions/" + subscriptionID)},
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = definitions.Delete(ctx, "/subscriptions/"+subscriptionID, roleID, nil) })

	principal := newTestServicePrincipal(t, "rbac-notactions")
	assignRole(t, groupScope, roleID, principal.objectID)

	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, principal.credential(), clientOpts())
	require.NoError(t, err)
	_, err = identities.CreateOrUpdate(ctx, group, "rbac-kept", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = identities.Delete(ctx, group, "rbac-kept", nil)
	requireAuthorizationFailed(t, err, "Microsoft.ManagedIdentity/userAssignedIdentities/delete")
}

// TestRBAC_SubscriptionAssignmentIsInherited proves an assignment at the
// subscription reaches every resource group beneath it.
func TestRBAC_SubscriptionAssignmentIsInherited(t *testing.T) {
	t.Parallel()
	const group = "rbac-inherited-rg"
	createResourceGroup(t, group)
	principal := newTestServicePrincipal(t, "rbac-inherited")

	groups, err := armresources.NewResourceGroupsClient(subscriptionID, principal.credential(), clientOpts())
	require.NoError(t, err)
	_, err = groups.Get(ctx, group, nil)
	requireAuthorizationFailed(t, err, "Microsoft.Resources/subscriptions/resourceGroups/read")

	assignRole(t, "/subscriptions/"+subscriptionID, readerRoleID, principal.objectID)
	_, err = groups.Get(ctx, group, nil)
	require.NoError(t, err, "Reader on the subscription reads its groups")
	_, err = groups.CreateOrUpdate(ctx, "rbac-inherited-new-rg", armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	requireAuthorizationFailed(t, err, "Microsoft.Resources/subscriptions/resourceGroups/write")
}

// TestRBAC_TokenFromAnotherTenantIsRefused proves Azure Resource Manager
// accepts only tokens the subscription's own tenant issued.
func TestRBAC_TokenFromAnotherTenantIsRefused(t *testing.T) {
	t.Parallel()
	const foreign = "22222222-2222-2222-2222-222222222222"
	token, _, err := fetchSimAccessTokenInTenant(foreign, "https://management.azure.com/.default")
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/subscriptions/"+subscriptionID+"/resourcegroups?api-version=2021-04-01", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, string(body), "InvalidAuthenticationTokenTenant")
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "/"+simTenantID)
}

// rbacBlobAccount provisions a storage account with a container holding one
// blob, written with the account key, and returns the account's resource ID.
func rbacBlobAccount(t *testing.T, group, account string, sharedKey bool) string {
	t.Helper()
	ensureStorageARMResourceGroup(t, group)
	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := accounts.BeginCreate(ctx, group, account, armstorage.AccountCreateParameters{
		Kind:     to.Ptr(armstorage.KindStorageV2),
		SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	client := newBlobTestClient(t, account)
	_, err = client.CreateContainer(ctx, "data", nil)
	require.NoError(t, err)
	_, err = client.UploadBuffer(ctx, "data", "hello.txt", []byte("hello"), nil)
	require.NoError(t, err)
	if !sharedKey {
		_, err = accounts.Update(ctx, group, account, armstorage.AccountUpdateParameters{
			Properties: &armstorage.AccountPropertiesUpdateParameters{AllowSharedKeyAccess: to.Ptr(false)},
		}, nil)
		require.NoError(t, err)
	}
	return *created.ID
}

func rbacBlobClient(t *testing.T, account string, cred azcore.TokenCredential) *azblob.Client {
	t.Helper()
	client, err := azblob.NewClient(storageSDKURL(t, account, "blob"), cred, &azblob.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport:                       storageSDKTransport{},
		InsecureAllowCredentialWithHTTP: true,
	}})
	require.NoError(t, err)
	return client
}

func rbacDownload(client *azblob.Client) (string, error) {
	resp, err := client.DownloadStream(ctx, "data", "hello.txt", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}

// TestRBAC_StorageBlobDataReaderReadsButCannotWrite proves a Microsoft Entra
// caller reaches blobs through data actions alone: none at all is refused,
// Storage Blob Data Reader reads and is refused writes, and Storage Blob Data
// Contributor writes.
func TestRBAC_StorageBlobDataReaderReadsButCannotWrite(t *testing.T) {
	t.Parallel()
	const account = "rbacblobreader"
	accountID := rbacBlobAccount(t, "rbac-storage-rg", account, true)
	principal := newTestServicePrincipal(t, "rbac-blob-reader")
	client := rbacBlobClient(t, account, principal.credential())

	_, err := rbacDownload(client)
	requireBlobErrorCode(t, err, "AuthorizationPermissionMismatch", "a principal without a data role reads")

	assignRole(t, accountID, storageBlobDataReaderRoleID, principal.objectID)
	got, err := rbacDownload(client)
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
	_, err = client.UploadBuffer(ctx, "data", "written.txt", []byte("x"), nil)
	requireBlobErrorCode(t, err, "AuthorizationPermissionMismatch", "Storage Blob Data Reader writes")

	assignRole(t, accountID+"/blobServices/default/containers/data", storageBlobDataContributorRole, principal.objectID)
	_, err = client.UploadBuffer(ctx, "data", "written.txt", []byte("x"), nil)
	require.NoError(t, err, "Storage Blob Data Contributor on the container writes")

	// Owner of the subscription holds no data action: a bearer of the
	// bootstrap principal reads no blob.
	_, err = rbacDownload(rbacBlobClient(t, account, &fakeCredential{}))
	requireBlobErrorCode(t, err, "AuthorizationPermissionMismatch", "the subscription's Owner reads with a bearer")
}

// TestRBAC_AllowSharedKeyAccessFalseRefusesKeys proves an account whose
// allowSharedKeyAccess is false refuses Shared Key and service SAS while a
// Microsoft Entra caller with a data role still reads.
func TestRBAC_AllowSharedKeyAccessFalseRefusesKeys(t *testing.T) {
	t.Parallel()
	const account = "rbacnosharedkey"
	accountID := rbacBlobAccount(t, "rbac-storage-rg", account, false)
	key := blobTestSharedKey(t, account)

	_, err := rbacDownload(newBlobTestClient(t, account))
	requireBlobErrorCode(t, err, "KeyBasedAuthenticationNotPermitted", "Shared Key on an account without shared key access")

	signed, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPSandHTTP,
		ExpiryTime:    time.Now().UTC().Add(time.Hour),
		Permissions:   (&sas.BlobPermissions{Read: true}).String(),
		ContainerName: "data",
		BlobName:      "hello.txt",
	}.SignWithSharedKey(key)
	require.NoError(t, err)
	resp := storageRawRequestSAS(t, account, "/data/hello.txt?"+signed.Encode())
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "KeyBasedAuthenticationNotPermitted", resp.Header.Get("x-ms-error-code"))

	principal := newTestServicePrincipal(t, "rbac-no-shared-key")
	assignRole(t, accountID, storageBlobDataReaderRoleID, principal.objectID)
	got, err := rbacDownload(rbacBlobClient(t, account, principal.credential()))
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

// storageRawRequestSAS sends an unsigned GET carrying a signature in its
// query to an account's Blob service.
func storageRawRequestSAS(t *testing.T, account, target string) *http.Response {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	_, port, _ := strings.Cut(u.Host, ":")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+target, nil)
	require.NoError(t, err)
	req.Host = fmt.Sprintf("%s.blob.localhost:%s", account, port)
	req.Header.Set("x-ms-version", "2021-12-02")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestRBAC_UserDelegationSASIsVerifiedAgainstItsKey proves a user delegation
// signature authorizes only when the key it names is one the account issued
// and still holds, its signature is that key's, and the key's owner still
// holds the operation's data action.
func TestRBAC_UserDelegationSASIsVerifiedAgainstItsKey(t *testing.T) {
	t.Parallel()
	const group, account = "rbac-storage-rg", "rbacudsas"
	accountID := rbacBlobAccount(t, group, account, true)
	principal := newTestServicePrincipal(t, "rbac-ud-sas")
	assignment := assignRole(t, accountID, storageBlobDataReaderRoleID, principal.objectID)

	svc, err := service.NewClient(storageSDKURL(t, account, "blob"), principal.credential(), &service.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport:                       storageSDKTransport{},
		InsecureAllowCredentialWithHTTP: true,
	}})
	require.NoError(t, err)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	info := service.KeyInfo{
		Start:  to.Ptr(start.Format(sas.TimeFormat)),
		Expiry: to.Ptr(start.Add(2 * time.Hour).Format(sas.TimeFormat)),
	}
	udc, err := svc.GetUserDelegationCredential(ctx, info, nil)
	require.NoError(t, err)

	values := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPSandHTTP,
		StartTime:     start,
		ExpiryTime:    start.Add(time.Hour),
		Permissions:   (&sas.BlobPermissions{Read: true}).String(),
		ContainerName: "data",
		BlobName:      "hello.txt",
	}
	signed, err := values.SignWithUserDelegation(udc)
	require.NoError(t, err)
	resp := storageRawRequestSAS(t, account, "/data/hello.txt?"+signed.Encode())
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Equal(t, "hello", string(body))

	// A signature under another key value than the one issued.
	valid := signed.Encode()
	query, err := url.ParseQuery(valid)
	require.NoError(t, err)
	query.Set("sig", "AAAA"+query.Get("sig")[4:])
	resp = storageRawRequestSAS(t, account, "/data/hello.txt?"+query.Encode())
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "AuthenticationFailed", resp.Header.Get("x-ms-error-code"))

	// A key the signature names but the account never issued.
	query, err = url.ParseQuery(valid)
	require.NoError(t, err)
	query.Set("ske", start.Add(3*time.Hour).Format(sas.TimeFormat))
	resp = storageRawRequestSAS(t, account, "/data/hello.txt?"+query.Encode())
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "AuthenticationFailed", resp.Header.Get("x-ms-error-code"))

	// The key's owner loses its data action: the signature stops reading.
	assignments, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = assignments.DeleteByID(ctx, assignment, nil)
	require.NoError(t, err)
	resp = storageRawRequestSAS(t, account, "/data/hello.txt?"+valid)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "AuthorizationPermissionMismatch", resp.Header.Get("x-ms-error-code"))

	// Revoking the account's user delegation keys invalidates every signature.
	assignRole(t, accountID, storageBlobDataReaderRoleID, principal.objectID)
	resp = storageRawRequestSAS(t, account, "/data/hello.txt?"+valid)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = accounts.RevokeUserDelegationKeys(ctx, group, account, nil)
	require.NoError(t, err)
	resp = storageRawRequestSAS(t, account, "/data/hello.txt?"+valid)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "AuthenticationFailed", resp.Header.Get("x-ms-error-code"))
}
