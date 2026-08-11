package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensureStorageARMResourceGroup creates the resource group used by the
// Microsoft.Storage management-plane tests through the same armresources client
// a real consumer uses.
func ensureStorageARMResourceGroup(t *testing.T, rg string) {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
}

// createStorageAccountForARM provisions a storage account via the official
// armstorage AccountsClient long-running create.
func createStorageAccountForARM(t *testing.T, rg, name string) *armstorage.AccountsClient {
	t.Helper()
	ensureStorageARMResourceGroup(t, rg)
	client, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rg, name, armstorage.AccountCreateParameters{
		Kind:     to.Ptr(armstorage.KindStorageV2),
		SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return client
}

// TestStorageARM_AccountActions exercises the storage-account action verbs and
// the subscription/provider-scoped catalog operations the armstorage SDK drives.
func TestStorageARM_AccountActions(t *testing.T) {
	rg := "storagearm-actions-rg"
	account := "storagearmactions"
	client := createStorageAccountForARM(t, rg, account)

	keys, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, keys.Keys)
	require.NotNil(t, keys.Keys[0].Value)

	regen, err := client.RegenerateKey(ctx, rg, account, armstorage.AccountRegenerateKeyParameters{
		KeyName: to.Ptr("key1"),
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, regen.Keys)

	accountSAS, err := client.ListAccountSAS(ctx, rg, account, armstorage.AccountSasParameters{
		Services:               to.Ptr(armstorage.ServicesB),
		ResourceTypes:          to.Ptr(armstorage.SignedResourceTypesS),
		Permissions:            to.Ptr(armstorage.PermissionsR),
		SharedAccessExpiryTime: to.Ptr(time.Now().Add(time.Hour)),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, accountSAS.AccountSasToken)
	assert.Contains(t, *accountSAS.AccountSasToken, "sig=")

	serviceSAS, err := client.ListServiceSAS(ctx, rg, account, armstorage.ServiceSasParameters{
		CanonicalizedResource: to.Ptr("/blob/" + account + "/container1"),
		Resource:              to.Ptr(armstorage.SignedResourceC),
		Permissions:           to.Ptr(armstorage.PermissionsR),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, serviceSAS.ServiceSasToken)

	_, err = client.RevokeUserDelegationKeys(ctx, rg, account, nil)
	require.NoError(t, err)

	failoverPoller, err := client.BeginFailover(ctx, rg, account, nil)
	require.NoError(t, err)
	_, err = failoverPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// ListByResourceGroup + subscription List.
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	page, err := rgPager.NextPage(ctx)
	require.NoError(t, err)
	found := false
	for _, a := range page.Value {
		if a.Name != nil && *a.Name == account {
			found = true
		}
	}
	assert.True(t, found, "account must appear in ListByResourceGroup")

	subPager := client.NewListPager(nil)
	_, err = subPager.NextPage(ctx)
	require.NoError(t, err)
}

func TestStorageARM_Catalog(t *testing.T) {
	opsClient, err := armstorage.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	opsPager := opsClient.NewListPager(nil)
	opsPage, err := opsPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, opsPage.Value)

	skuClient, err := armstorage.NewSKUsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	skuPager := skuClient.NewListPager(nil)
	skuPage, err := skuPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, skuPage.Value)
	require.NotNil(t, skuPage.Value[0].Name)

	usageClient, err := armstorage.NewUsagesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	usagePager := usageClient.NewListByLocationPager("eastus", nil)
	usagePage, err := usagePager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, usagePage.Value)

	acctClient, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	avail, err := acctClient.CheckNameAvailability(ctx, armstorage.AccountCheckNameAvailabilityParameters{
		Name: to.Ptr("brandnewuniquename123"),
		Type: to.Ptr("Microsoft.Storage/storageAccounts"),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, avail.NameAvailable)
	assert.True(t, *avail.NameAvailable)

	delClient, err := armstorage.NewDeletedAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	delPager := delClient.NewListPager(nil)
	_, err = delPager.NextPage(ctx)
	require.NoError(t, err)
}

// TestStorageARM_AccountChildResources exercises the per-account ARM child
// resources (managementPolicies, inventoryPolicies, privateEndpointConnections,
// privateLinkResources, objectReplicationPolicies, encryptionScopes, localUsers).
func TestStorageARM_AccountChildResources(t *testing.T) {
	rg := "storagearm-children-rg"
	account := "storagearmchildren"
	createStorageAccountForARM(t, rg, account)

	// managementPolicies (named "default").
	mpClient, err := armstorage.NewManagementPoliciesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = mpClient.CreateOrUpdate(ctx, rg, account, armstorage.ManagementPolicyNameDefault, armstorage.ManagementPolicy{
		Properties: &armstorage.ManagementPolicyProperties{
			Policy: &armstorage.ManagementPolicySchema{
				Rules: []*armstorage.ManagementPolicyRule{{
					Enabled: to.Ptr(true),
					Name:    to.Ptr("expire-old"),
					Type:    to.Ptr(armstorage.RuleTypeLifecycle),
					Definition: &armstorage.ManagementPolicyDefinition{
						Actions: &armstorage.ManagementPolicyAction{
							BaseBlob: &armstorage.ManagementPolicyBaseBlob{
								Delete: &armstorage.DateAfterModification{DaysAfterModificationGreaterThan: to.Ptr(float32(30))},
							},
						},
						Filters: &armstorage.ManagementPolicyFilter{BlobTypes: []*string{to.Ptr("blockBlob")}},
					},
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	mpGet, err := mpClient.Get(ctx, rg, account, armstorage.ManagementPolicyNameDefault, nil)
	require.NoError(t, err)
	require.NotNil(t, mpGet.Properties)
	require.NotNil(t, mpGet.Properties.Policy)
	require.Len(t, mpGet.Properties.Policy.Rules, 1)
	_, err = mpClient.Delete(ctx, rg, account, armstorage.ManagementPolicyNameDefault, nil)
	require.NoError(t, err)

	// inventoryPolicies.
	ipClient, err := armstorage.NewBlobInventoryPoliciesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = ipClient.CreateOrUpdate(ctx, rg, account, armstorage.BlobInventoryPolicyNameDefault, armstorage.BlobInventoryPolicy{
		Properties: &armstorage.BlobInventoryPolicyProperties{
			Policy: &armstorage.BlobInventoryPolicySchema{
				Enabled: to.Ptr(true),
				Type:    to.Ptr(armstorage.InventoryRuleTypeInventory),
				Rules: []*armstorage.BlobInventoryPolicyRule{{
					Enabled:     to.Ptr(true),
					Name:        to.Ptr("inv-rule"),
					Destination: to.Ptr("inventory-dest"),
					Definition: &armstorage.BlobInventoryPolicyDefinition{
						Format:       to.Ptr(armstorage.FormatCSV),
						ObjectType:   to.Ptr(armstorage.ObjectTypeBlob),
						Schedule:     to.Ptr(armstorage.ScheduleDaily),
						SchemaFields: []*string{to.Ptr("Name")},
					},
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = ipClient.Get(ctx, rg, account, armstorage.BlobInventoryPolicyNameDefault, nil)
	require.NoError(t, err)
	ipPager := ipClient.NewListPager(rg, account, nil)
	_, err = ipPager.NextPage(ctx)
	require.NoError(t, err)
	_, err = ipClient.Delete(ctx, rg, account, armstorage.BlobInventoryPolicyNameDefault, nil)
	require.NoError(t, err)

	// privateEndpointConnections + privateLinkResources.
	pecClient, err := armstorage.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = pecClient.Put(ctx, rg, account, "pec1", armstorage.PrivateEndpointConnection{
		Properties: &armstorage.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armstorage.PrivateLinkServiceConnectionState{
				Status:      to.Ptr(armstorage.PrivateEndpointServiceConnectionStatusApproved),
				Description: to.Ptr("approved by test"),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = pecClient.Get(ctx, rg, account, "pec1", nil)
	require.NoError(t, err)
	pecPager := pecClient.NewListPager(rg, account, nil)
	_, err = pecPager.NextPage(ctx)
	require.NoError(t, err)
	_, err = pecClient.Delete(ctx, rg, account, "pec1", nil)
	require.NoError(t, err)

	plrClient, err := armstorage.NewPrivateLinkResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	plr, err := plrClient.ListByStorageAccount(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, plr.Value)

	// objectReplicationPolicies.
	orpClient, err := armstorage.NewObjectReplicationPoliciesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	orp, err := orpClient.CreateOrUpdate(ctx, rg, account, "default", armstorage.ObjectReplicationPolicy{
		Properties: &armstorage.ObjectReplicationPolicyProperties{
			SourceAccount:      to.Ptr("srcaccount"),
			DestinationAccount: to.Ptr(account),
			Rules: []*armstorage.ObjectReplicationPolicyRule{{
				SourceContainer:      to.Ptr("src"),
				DestinationContainer: to.Ptr("dst"),
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, orp.Properties)
	require.NotNil(t, orp.Properties.PolicyID)
	orpPager := orpClient.NewListPager(rg, account, nil)
	_, err = orpPager.NextPage(ctx)
	require.NoError(t, err)
	_, err = orpClient.Get(ctx, rg, account, *orp.Name, nil)
	require.NoError(t, err)
	_, err = orpClient.Delete(ctx, rg, account, *orp.Name, nil)
	require.NoError(t, err)

	// encryptionScopes (PUT + PATCH + GET + List).
	esClient, err := armstorage.NewEncryptionScopesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = esClient.Put(ctx, rg, account, "scope1", armstorage.EncryptionScope{
		EncryptionScopeProperties: &armstorage.EncryptionScopeProperties{
			Source: to.Ptr(armstorage.EncryptionScopeSourceMicrosoftStorage),
			State:  to.Ptr(armstorage.EncryptionScopeStateEnabled),
		},
	}, nil)
	require.NoError(t, err)
	_, err = esClient.Patch(ctx, rg, account, "scope1", armstorage.EncryptionScope{
		EncryptionScopeProperties: &armstorage.EncryptionScopeProperties{
			State: to.Ptr(armstorage.EncryptionScopeStateEnabled),
		},
	}, nil)
	require.NoError(t, err)
	esGet, err := esClient.Get(ctx, rg, account, "scope1", nil)
	require.NoError(t, err)
	require.NotNil(t, esGet.EncryptionScopeProperties)
	esPager := esClient.NewListPager(rg, account, nil)
	_, err = esPager.NextPage(ctx)
	require.NoError(t, err)

	// localUsers (CreateOrUpdate + Get + List + ListKeys + RegeneratePassword + Delete).
	luClient, err := armstorage.NewLocalUsersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = luClient.CreateOrUpdate(ctx, rg, account, "localuser1", armstorage.LocalUser{
		Properties: &armstorage.LocalUserProperties{
			HomeDirectory: to.Ptr("home"),
			HasSharedKey:  to.Ptr(true),
			PermissionScopes: []*armstorage.PermissionScope{{
				Permissions:  to.Ptr("rwl"),
				Service:      to.Ptr("blob"),
				ResourceName: to.Ptr("container1"),
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = luClient.Get(ctx, rg, account, "localuser1", nil)
	require.NoError(t, err)
	luPager := luClient.NewListPager(rg, account, nil)
	_, err = luPager.NextPage(ctx)
	require.NoError(t, err)
	luKeys, err := luClient.ListKeys(ctx, rg, account, "localuser1", nil)
	require.NoError(t, err)
	require.NotNil(t, luKeys.SharedKey)
	luPass, err := luClient.RegeneratePassword(ctx, rg, account, "localuser1", nil)
	require.NoError(t, err)
	require.NotNil(t, luPass.SSHPassword)
	_, err = luClient.Delete(ctx, rg, account, "localuser1", nil)
	require.NoError(t, err)
}
