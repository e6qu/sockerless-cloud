package azure_tf_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/stretchr/testify/require"
)

// TestTerraformApplyDestroy provisions a foundation set of Azure
// resources against the Azure simulator using the `azurerm` provider
// (the sim ships /metadata/endpoints + OAuth2 token endpoint + JWKS so
// azurerm can bootstrap its cloud config + auth without ever reaching
// real Azure). Then asserts canonical resource-id paths round-trip and
// terraform destroy cleans up.
//
// Slices exercised against the simulator:
//   - Microsoft.Resources/resourceGroups
//   - Microsoft.Network/virtualNetworks
//   - Microsoft.Network/virtualNetworks/subnets
//   - Microsoft.Network/networkSecurityGroups
//   - Microsoft.Network/networkSecurityGroups/securityRules
//   - Microsoft.Storage/storageAccounts
//   - Microsoft.KeyVault/vaults
//   - Microsoft.ContainerRegistry/registries
//   - Microsoft.Cache/Redis + firewallRules
//   - Microsoft.ManagedIdentity/userAssignedIdentities
//   - Microsoft.Network/publicIPAddresses + publicIPPrefixes + natGateways +
//     subnet NAT associations + loadBalancers
//   - Microsoft.Network/privateDnsZones
//   - Microsoft.Network/dnsZones + dnsZones/A
//   - Microsoft.ServiceBus/namespaces + queues
//   - Microsoft.EventGrid/topics
//   - Microsoft.DocumentDB/databaseAccounts + sqlDatabases + containers
//   - Microsoft.OperationalInsights/workspaces
//   - Microsoft.Insights/components
//   - Microsoft.DBforPostgreSQL/flexibleServers + databases + firewallRules
//   - Microsoft.App/managedEnvironments + containerApps + jobs
//   - Microsoft.Web/serverfarms + sites (Function App)
//   - Microsoft.Web/sites/networkConfig/virtualNetwork (App Service regional
//     VNet "swift" integration) + a Microsoft.Web/serverFarms-delegated subnet
//   - Microsoft.Network/applicationSecurityGroups
//   - Microsoft.Network/privateLinkServices + privateEndpoints (both halves of
//     Azure Private Link, including the endpoint's application-security-group
//     association and a Key Vault-targeted endpoint whose private DNS zone
//     group publishes into a Microsoft.Network/privateDnsZones zone)
//   - Microsoft.Network/networkProfiles
//   - Microsoft.Network/serviceEndpointPolicies
//
// The Microsoft.Subscription alias slice runs separately in
// TestTerraformSubscriptionApplyDestroy (its own CI shard): each
// azurerm_subscription create and cancel sits through the provider's fixed
// StateChangeConf settle delays, too slow for this stack's CI budget.
func TestTerraformApplyDestroy(t *testing.T) {
	requireTerraformNetworkHost(t)
	dir := tfStackWorkspace(t)
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// Idempotency: a second plan must show no drift. -detailed-exitcode makes
	// terraform exit 2 (non-zero) on any drift, which runTimed surfaces as an
	// error — so a clean plan (exit 0) is the only pass.
	out, err = runTimed(t, "terraform plan", terraformCmd(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "terraform plan showed drift after apply (not idempotent):\n%s", out)

	outputs := readOutputs(t, dir)

	rgID := outputs.must(t, "resource_group_id")
	require.True(t, strings.HasSuffix(rgID, "/resourceGroups/tf-test-rg"),
		"resource group id must end with /resourceGroups/{name}; got %s", rgID)

	vnetID := outputs.must(t, "vnet_id")
	require.Contains(t, vnetID, "/resourceGroups/tf-test-rg/providers/Microsoft.Network/virtualNetworks/tf-test-vnet",
		"vnet id must include the canonical ARM path; got %s", vnetID)

	subnetID := outputs.must(t, "subnet_id")
	require.Contains(t, subnetID, "/virtualNetworks/tf-test-vnet/subnets/tf-test-subnet",
		"subnet id must include the canonical ARM path; got %s", subnetID)

	nsgID := outputs.must(t, "nsg_id")
	require.Contains(t, nsgID, "/networkSecurityGroups/tf-test-nsg",
		"nsg id must include the canonical ARM path; got %s", nsgID)

	nsgRuleID := outputs.must(t, "nsg_rule_id")
	require.Contains(t, nsgRuleID, "/networkSecurityGroups/tf-test-nsg/securityRules/allow-ssh",
		"nsg rule id must include the canonical ARM path; got %s", nsgRuleID)

	storageID := outputs.must(t, "storage_account_id")
	require.Contains(t, storageID, "/providers/Microsoft.Storage/storageAccounts/tftestsa12345",
		"storage account id must include the canonical ARM path; got %s", storageID)

	blobEndpoint := outputs.must(t, "storage_account_blob_endpoint")
	require.True(t, strings.Contains(blobEndpoint, "tftestsa12345.blob."),
		"blob endpoint must include account subdomain (azurerm storage SDK parses URLs this way); got %s", blobEndpoint)

	kvID := outputs.must(t, "key_vault_id")
	require.Contains(t, kvID, "/providers/Microsoft.KeyVault/vaults/tf-test-kv",
		"key vault id must include the canonical ARM path; got %s", kvID)

	kvURI := outputs.must(t, "key_vault_uri")
	require.True(t, strings.Contains(kvURI, "tf-test-kv.vault."),
		"vault uri must include vault subdomain (azurerm/keyvault SDK parses URLs this way); got %s", kvURI)

	// Each canonical ARM path is asserted so a future provider/SDK
	// upgrade that mangles the URL surfaces in CI.
	azrmRG := outputs.must(t, "azrm_resource_group_id")
	require.True(t, strings.HasSuffix(azrmRG, "/resourceGroups/tf-azrm-rg"),
		"azurerm RG id must end with /resourceGroups/{name}; got %s", azrmRG)

	azrmPG := outputs.must(t, "azrm_postgresql_flexible_server_id")
	require.Contains(t, azrmPG, "/providers/Microsoft.DBforPostgreSQL/flexibleServers/tf-azrm-pg",
		"Azure Database for PostgreSQL flexible server id must include canonical ARM path; got %s", azrmPG)
	azrmPGDatabase := outputs.must(t, "azrm_postgresql_flexible_server_database_id")
	require.Contains(t, azrmPGDatabase, "/flexibleServers/tf-azrm-pg/databases/tfapp",
		"Azure Database for PostgreSQL database id must include canonical ARM path; got %s", azrmPGDatabase)
	azrmPGFirewallRule := outputs.must(t, "azrm_postgresql_flexible_server_firewall_rule_id")
	require.Contains(t, azrmPGFirewallRule, "/flexibleServers/tf-azrm-pg/firewallRules/allow-ci",
		"Azure Database for PostgreSQL firewall rule id must include canonical ARM path; got %s", azrmPGFirewallRule)

	azrmACR := outputs.must(t, "azrm_acr_id")
	require.Contains(t, azrmACR, "/providers/Microsoft.ContainerRegistry/registries/tfazrmacr",
		"azurerm ACR id must include canonical ARM path; got %s", azrmACR)

	// The registry's data plane authenticates the admin credential the
	// provider surfaces: an unauthenticated read is refused with the Docker
	// Registry v2 Bearer challenge naming this registry's token service, and
	// the admin_username/admin_password pair is accepted.
	assertACRDataPlaneAuthenticates(t,
		outputs.must(t, "azrm_acr_login_server"),
		outputs.must(t, "azrm_acr_admin_username"),
		outputs.must(t, "azrm_acr_admin_password"))

	azrmRedisHost := outputs.must(t, "azrm_redis_cache_hostname")
	require.Contains(t, azrmRedisHost, "tfazrmredis.redis.cache.",
		"azurerm Redis hostname must include Azure Cache for Redis host shape; got %s", azrmRedisHost)

	azrmRedisFW := outputs.must(t, "azrm_redis_firewall_rule_id")
	require.Contains(t, strings.ToLower(azrmRedisFW), "/providers/microsoft.cache/redis/tfazrmredis/firewallrules/allow_ci",
		"azurerm Redis firewall rule id must include canonical ARM path; got %s", azrmRedisFW)

	azrmUAI := outputs.must(t, "azrm_uai_id")
	require.Contains(t, azrmUAI, "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/tf-azrm-uai",
		"azurerm managed identity id must include canonical ARM path; got %s", azrmUAI)

	azrmLB := outputs.must(t, "azrm_lb_id")
	require.Contains(t, azrmLB, "/providers/Microsoft.Network/loadBalancers/tf-azrm-lb",
		"azurerm Load Balancer id must include canonical ARM path; got %s", azrmLB)

	azrmLBBackend := outputs.must(t, "azrm_lb_backend_pool_id")
	require.Contains(t, azrmLBBackend, "/loadBalancers/tf-azrm-lb/backendAddressPools/backend",
		"azurerm Load Balancer backend pool id must include child ARM path; got %s", azrmLBBackend)

	azrmLBProbe := outputs.must(t, "azrm_lb_probe_id")
	require.Contains(t, azrmLBProbe, "/loadBalancers/tf-azrm-lb/probes/tcp-probe",
		"azurerm Load Balancer probe id must include child ARM path; got %s", azrmLBProbe)

	azrmLBRule := outputs.must(t, "azrm_lb_rule_id")
	require.Contains(t, azrmLBRule, "/loadBalancers/tf-azrm-lb/loadBalancingRules/http-rule",
		"azurerm Load Balancer rule id must include child ARM path; got %s", azrmLBRule)

	azrmNAT := outputs.must(t, "azrm_nat_gateway_id")
	require.Contains(t, azrmNAT, "/providers/Microsoft.Network/natGateways/tf-azrm-nat",
		"azurerm NAT Gateway id must include canonical ARM path; got %s", azrmNAT)

	azrmNATPrefix := outputs.must(t, "azrm_nat_public_ip_prefix_id")
	require.Contains(t, azrmNATPrefix, "/providers/Microsoft.Network/publicIPPrefixes/tf-azrm-nat-prefix",
		"azurerm Public IP Prefix id must include canonical ARM path; got %s", azrmNATPrefix)

	azrmNATSubnet := outputs.must(t, "azrm_nat_subnet_id")
	require.Contains(t, azrmNATSubnet, "/virtualNetworks/tf-azrm-nat-vnet/subnets/tf-azrm-nat-subnet",
		"azurerm subnet NAT association must keep the associated subnet id; got %s", azrmNATSubnet)

	azrmDNS := outputs.must(t, "azrm_private_dns_zone_id")
	require.Contains(t, azrmDNS, "/providers/Microsoft.Network/privateDnsZones/tf-azrm.internal",
		"azurerm private DNS zone id must include canonical ARM path; got %s", azrmDNS)

	azrmPublicDNS := outputs.must(t, "azrm_public_dns_zone_id")
	require.Contains(t, azrmPublicDNS, "/providers/Microsoft.Network/dnsZones/tf-azrm.example.com",
		"azurerm public DNS zone id must include canonical ARM path; got %s", azrmPublicDNS)

	azrmPublicDNSA := outputs.must(t, "azrm_public_dns_a_record_id")
	require.Contains(t, azrmPublicDNSA, "/providers/Microsoft.Network/dnsZones/tf-azrm.example.com/A/www",
		"azurerm public DNS A record id must include canonical ARM path; got %s", azrmPublicDNSA)

	azrmServiceBusNamespace := outputs.must(t, "azrm_servicebus_namespace_id")
	require.Contains(t, azrmServiceBusNamespace, "/providers/Microsoft.ServiceBus/namespaces/tfazrmsbns",
		"azurerm Service Bus namespace id must include canonical ARM path; got %s", azrmServiceBusNamespace)

	azrmServiceBusQueue := outputs.must(t, "azrm_servicebus_queue_id")
	require.Contains(t, azrmServiceBusQueue, "/providers/Microsoft.ServiceBus/namespaces/tfazrmsbns/queues/tfazrmsbqueue",
		"azurerm Service Bus queue id must include canonical ARM path; got %s", azrmServiceBusQueue)

	azrmEventGridEndpoint := outputs.must(t, "azrm_eventgrid_topic_endpoint")
	require.Contains(t, azrmEventGridEndpoint, "/api/events",
		"azurerm Event Grid topic endpoint must be a publish endpoint; got %s", azrmEventGridEndpoint)

	azrmCosmosEndpoint := outputs.must(t, "azrm_cosmosdb_account_endpoint")
	require.Contains(t, azrmCosmosEndpoint, "tfazrmcosmos.documents.",
		"azurerm Cosmos DB endpoint must include account documents host; got %s", azrmCosmosEndpoint)

	azrmCosmosContainer := outputs.must(t, "azrm_cosmosdb_sql_container_id")
	require.Contains(t, azrmCosmosContainer, "/providers/Microsoft.DocumentDB/databaseAccounts/tfazrmcosmos/sqlDatabases/tfappdb/containers/users",
		"azurerm Cosmos SQL container id must include canonical ARM path; got %s", azrmCosmosContainer)

	azrmCosmosTable := outputs.must(t, "azrm_cosmosdb_table_id")
	require.Contains(t, azrmCosmosTable, "/providers/Microsoft.DocumentDB/databaseAccounts/tfazrmcosmos/tables/tfcosmostable",
		"azurerm Cosmos table id must include canonical ARM path; got %s", azrmCosmosTable)

	azrmKVPolicy := outputs.must(t, "azrm_key_vault_access_policy_id")
	require.Contains(t, strings.ToLower(azrmKVPolicy), "/providers/microsoft.keyvault/vaults/tf-azrm-kv",
		"azurerm Key Vault access policy id must include vault ARM path; got %s", azrmKVPolicy)

	azrmKVKey := outputs.must(t, "azrm_key_vault_key_id")
	require.Contains(t, azrmKVKey, "tf-azrm-kv.vault.",
		"azurerm Key Vault key id must be data-plane URL-shaped; got %s", azrmKVKey)
	require.Contains(t, azrmKVKey, "/keys/tf-azrm-key/",
		"azurerm Key Vault key id must include key name and version; got %s", azrmKVKey)

	azrmKVCert := outputs.must(t, "azrm_key_vault_certificate_id")
	require.Contains(t, azrmKVCert, "tf-azrm-kv.vault.",
		"azurerm Key Vault certificate id must be data-plane URL-shaped; got %s", azrmKVCert)
	require.Contains(t, azrmKVCert, "/certificates/tf-azrm-cert/",
		"azurerm Key Vault certificate id must include certificate name and version; got %s", azrmKVCert)

	azrmLAW := outputs.must(t, "azrm_law_id")
	require.Contains(t, azrmLAW, "/providers/Microsoft.OperationalInsights/workspaces/tf-azrm-law",
		"azurerm Log Analytics workspace id must include canonical ARM path; got %s", azrmLAW)

	azrmAI := outputs.must(t, "azrm_appins_id")
	require.Contains(t, azrmAI, "/providers/Microsoft.Insights/components/tf-azrm-ai",
		"azurerm Application Insights id must include canonical ARM path; got %s", azrmAI)

	azrmCAE := outputs.must(t, "azrm_container_app_env_id")
	require.Contains(t, azrmCAE, "/providers/Microsoft.App/managedEnvironments/tf-azrm-cae",
		"azurerm Container App Environment id must include canonical ARM path; got %s", azrmCAE)

	azrmCA := outputs.must(t, "azrm_container_app_id")
	require.Contains(t, azrmCA, "/providers/Microsoft.App/containerApps/tf-azrm-ca",
		"azurerm Container App id must include canonical ARM path; got %s", azrmCA)

	azrmCAJ := outputs.must(t, "azrm_container_app_job_id")
	require.Contains(t, azrmCAJ, "/providers/Microsoft.App/jobs/tf-azrm-caj",
		"azurerm Container App Job id must include canonical ARM path; got %s", azrmCAJ)

	azrmCAEStorage := outputs.must(t, "azrm_container_app_env_storage_id")
	require.Contains(t, azrmCAEStorage,
		"/providers/Microsoft.App/managedEnvironments/tf-azrm-cae/storages/tfazrmcaestorage",
		"azurerm Container App Environment storage id must include canonical ARM path; got %s", azrmCAEStorage)

	azrmLogic := outputs.must(t, "azrm_logic_app_workflow_id")
	require.Contains(t, azrmLogic, "/providers/Microsoft.Logic/workflows/tf-azrm-logic",
		"azurerm Logic App workflow id must include canonical ARM path; got %s", azrmLogic)

	azrmACI := outputs.must(t, "azrm_container_group_id")
	require.Contains(t, azrmACI, "/providers/Microsoft.ContainerInstance/containerGroups/tf-azrm-aci",
		"azurerm container group id must include canonical ARM path; got %s", azrmACI)

	azrmSP := outputs.must(t, "azrm_service_plan_id")
	// terraform-provider-azurerm normalizes Microsoft.Web/serverfarms
	// to the SDK-canonical `serverFarms` (camelCase) in state. The
	// sim emits lowercase in its ARM responses; the provider's ID
	// parser uppercases the F before storing. Match the provider's
	// canonical form here.
	require.Contains(t, strings.ToLower(azrmSP), "/providers/microsoft.web/serverfarms/tf-azrm-sp",
		"azurerm Service Plan id must include canonical ARM path (case-insensitive); got %s", azrmSP)

	azrmST := outputs.must(t, "azrm_storage_account_id")
	require.Contains(t, azrmST, "/providers/Microsoft.Storage/storageAccounts/tfazrmst12345",
		"azurerm storage account id must include canonical ARM path; got %s", azrmST)

	azrmStorageContainer := outputs.must(t, "azrm_storage_container_id")
	require.Contains(t, azrmStorageContainer,
		"/providers/Microsoft.Storage/storageAccounts/tfazrmst12345/blobServices/default/containers/tfazrmcontainer",
		"azurerm storage container id must include the canonical ARM path; got %s", azrmStorageContainer)

	azrmStorageTable := outputs.must(t, "azrm_storage_table_id")
	require.Contains(t, azrmStorageTable,
		"/providers/Microsoft.Storage/storageAccounts/tfazrmst12345/tableServices/default/tables/tfazrmstable",
		"azurerm storage table id must include the canonical ARM path; got %s", azrmStorageTable)

	azrmStorageShare := outputs.must(t, "azrm_storage_share_id")
	require.Contains(t, azrmStorageShare,
		"/providers/Microsoft.Storage/storageAccounts/tfazrmst12345/fileServices/default/shares/tfazrmshare",
		"azurerm storage share id must include the canonical ARM path; got %s", azrmStorageShare)

	// The nested directory the Files data plane created: the provider addresses
	// it inside the share it was created in, and reads it back on every
	// refresh — so the clean idempotency plan above also proves Get Directory
	// Properties answers for a directory below the share root.
	azrmShareDirectory := outputs.must(t, "azrm_storage_share_directory_id")
	require.Contains(t, azrmShareDirectory, "tfazrmshare",
		"azurerm storage share directory id must address the share it lives in; got %s", azrmShareDirectory)
	require.Contains(t, azrmShareDirectory, "artifacts",
		"azurerm storage share directory id must name the nested directory; got %s", azrmShareDirectory)
	require.Equal(t, "build/artifacts", outputs.must(t, "azrm_storage_share_directory_name"),
		"the nested directory must be created below the share root, not at it")

	azrmFA := outputs.must(t, "azrm_function_app_id")
	require.Contains(t, azrmFA, "/providers/Microsoft.Web/sites/tf-azrm-fa",
		"azurerm Function App id must include canonical ARM path; got %s", azrmFA)

	azrmSwiftSubnet := outputs.must(t, "azrm_swift_subnet_id")
	require.Contains(t, azrmSwiftSubnet, "/virtualNetworks/tf-azrm-swift-vnet/subnets/tf-azrm-swift-subnet",
		"azurerm App Service swift subnet id must include the canonical ARM path; got %s", azrmSwiftSubnet)

	// The site reports its regional integration back as
	// virtual_network_subnet_id, which is what makes the modern spelling
	// idempotent — and what made the deprecated standalone swift-connection
	// resource conflict with the app that owns the site.
	azrmSwiftIntegration := outputs.must(t, "azrm_swift_integration_subnet_id")
	require.Equal(t, azrmSwiftSubnet, azrmSwiftIntegration,
		"the site must report the subnet its regional integration uses; got %s", azrmSwiftIntegration)

	azrmAPIM := outputs.must(t, "azrm_apim_id")
	require.Contains(t, azrmAPIM, "/providers/Microsoft.ApiManagement/service/tf-azrm-apim",
		"azurerm API Management id must include canonical ARM path; got %s", azrmAPIM)

	azrmAPIMGateway := outputs.must(t, "azrm_apim_gateway_url")
	require.Contains(t, azrmAPIMGateway, "tf-azrm-apim",
		"APIM gateway_url must reference the service name (round-trips properties.gatewayUrl); got %s", azrmAPIMGateway)

	azrmAPIMApi := outputs.must(t, "azrm_apim_api_id")
	require.Contains(t, azrmAPIMApi, "/service/tf-azrm-apim/apis/tf-azrm-api",
		"APIM API id must include the service+api path; got %s", azrmAPIMApi)

	azrmAPIMProduct := outputs.must(t, "azrm_apim_product_id")
	require.Contains(t, azrmAPIMProduct, "/service/tf-azrm-apim/products/tf-azrm-product",
		"APIM product id must include the service+product path; got %s", azrmAPIMProduct)

	azrmAPIMSub := outputs.must(t, "azrm_apim_subscription_id")
	require.Contains(t, azrmAPIMSub, "/service/tf-azrm-apim/subscriptions/",
		"APIM subscription id must include the service+subscription path; got %s", azrmAPIMSub)

	azrmASG := outputs.must(t, "azrm_application_security_group_id")
	require.Contains(t, azrmASG, "/providers/Microsoft.Network/applicationSecurityGroups/tf-azrm-asg",
		"application security group id must include canonical ARM path; got %s", azrmASG)

	azrmPLS := outputs.must(t, "azrm_private_link_service_id")
	require.Contains(t, azrmPLS, "/providers/Microsoft.Network/privateLinkServices/tf-azrm-pls",
		"private link service id must include canonical ARM path; got %s", azrmPLS)

	azrmPLSAlias := outputs.must(t, "azrm_private_link_service_alias")
	require.Contains(t, azrmPLSAlias, "tf-azrm-pls.",
		"private link service alias must be minted from the service name; got %s", azrmPLSAlias)

	azrmPE := outputs.must(t, "azrm_private_endpoint_id")
	require.Contains(t, azrmPE, "/providers/Microsoft.Network/privateEndpoints/tf-azrm-pe",
		"private endpoint id must include canonical ARM path; got %s", azrmPE)

	// The address the endpoint reports is the one its subnet allocated, so it
	// has to fall inside that subnet's prefix rather than be an invented value.
	azrmPEIP := outputs.must(t, "azrm_private_endpoint_private_ip")
	require.True(t, strings.HasPrefix(azrmPEIP, "10.95.2."),
		"private endpoint address must come from its own subnet 10.95.2.0/24; got %s", azrmPEIP)

	azrmPENIC := outputs.must(t, "azrm_private_endpoint_nic_id")
	require.Contains(t, azrmPENIC, "/providers/Microsoft.Network/networkInterfaces/",
		"private endpoint must own an ordinary network interface; got %s", azrmPENIC)

	azrmKVPE := outputs.must(t, "azrm_kv_private_endpoint_id")
	require.Contains(t, azrmKVPE, "/providers/Microsoft.Network/privateEndpoints/tf-azrm-kv-pe",
		"Key Vault private endpoint id must include canonical ARM path; got %s", azrmKVPE)

	azrmKVPEFqdn := outputs.must(t, "azrm_kv_private_endpoint_custom_dns_fqdn")
	require.Equal(t, "tf-azrm-kv.vault.azure.net", azrmKVPEFqdn,
		"the endpoint must publish the vault's own fully-qualified name; got %s", azrmKVPEFqdn)

	azrmKVPEZoneGroup := outputs.must(t, "azrm_kv_private_endpoint_dns_zone_group_id")
	require.Contains(t, azrmKVPEZoneGroup, "/privateEndpoints/tf-azrm-kv-pe/privateDnsZoneGroups/default",
		"private DNS zone group id must include the child ARM path; got %s", azrmKVPEZoneGroup)

	azrmNetworkProfile := outputs.must(t, "azrm_network_profile_id")
	require.Contains(t, azrmNetworkProfile, "/providers/Microsoft.Network/networkProfiles/tf-azrm-np",
		"network profile id must include canonical ARM path; got %s", azrmNetworkProfile)

	azrmSEP := outputs.must(t, "azrm_subnet_service_endpoint_storage_policy_id")
	require.Contains(t, azrmSEP, "/providers/Microsoft.Network/serviceEndpointPolicies/tf-azrm-sep",
		"service endpoint policy id must include canonical ARM path; got %s", azrmSEP)

	azrmAppGateway := outputs.must(t, "azrm_application_gateway_id")
	require.Contains(t, azrmAppGateway, "/providers/Microsoft.Network/applicationGateways/tf-azrm-appgw",
		"application gateway id must include canonical ARM path; got %s", azrmAppGateway)
	// The gateway's child collections are addressed under the gateway itself,
	// which is what the sibling references inside its own configuration resolve
	// against — the provider reads those ids straight back out of the create.
	for _, poolID := range outputs.mustList(t, "azrm_application_gateway_backend_pool_ids") {
		require.Contains(t, poolID, azrmAppGateway+"/backendAddressPools/",
			"backend pool id must be addressed under the gateway; got %s", poolID)
	}
	for _, listenerID := range outputs.mustList(t, "azrm_application_gateway_listener_ids") {
		require.Contains(t, listenerID, azrmAppGateway+"/httpListeners/",
			"listener id must be addressed under the gateway; got %s", listenerID)
	}

	azrmNetworkWatcher := outputs.must(t, "azrm_network_watcher_id")
	require.Contains(t, azrmNetworkWatcher, "/providers/Microsoft.Network/networkWatchers/tf-azrm-network-watcher",
		"network watcher id must include canonical ARM path; got %s", azrmNetworkWatcher)

	azrmNetworkManager := outputs.must(t, "azrm_network_manager_id")
	require.Contains(t, azrmNetworkManager, "/providers/Microsoft.Network/networkManagers/tf-azrm-network-manager",
		"network manager id must include canonical ARM path; got %s", azrmNetworkManager)
	require.ElementsMatch(t, []string{"Connectivity", "SecurityAdmin"},
		outputs.mustList(t, "azrm_network_manager_scope_accesses"),
		"the manager must report the configuration kinds it was given access to")

	out, err = runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

// assertACRDataPlaneAuthenticates drives the container registry's own data
// plane at the login server the provider recorded. The registry answers an
// unauthenticated request with the Docker Registry v2 Bearer challenge and
// serves the same request once it carries the admin credential the provider
// exposes as admin_username / admin_password.
func assertACRDataPlaneAuthenticates(t *testing.T, loginServer, username, password string) {
	t.Helper()
	require.Equal(t, "tfazrmacr", username, "the admin username is the registry name")
	client, err := trustedHTTPClient(caCertFile)
	require.NoError(t, err)

	get := func(authorization string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, "https://"+loginServer+"/v2/", nil)
		require.NoError(t, err)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := client.Do(req)
		require.NoError(t, err, "GET the registry data plane at %s", loginServer)
		return resp
	}

	unauthenticated := get("")
	body, _ := io.ReadAll(unauthenticated.Body)
	unauthenticated.Body.Close()
	require.Equal(t, http.StatusUnauthorized, unauthenticated.StatusCode,
		"an unauthenticated registry read must be refused; body: %s", body)
	require.Contains(t, unauthenticated.Header.Get("Www-Authenticate"),
		`service="`+loginServer+`"`, "the challenge must name this registry")

	authenticated := get("Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	authenticated.Body.Close()
	require.Equal(t, http.StatusOK, authenticated.StatusCode,
		"the admin credential the provider surfaced must authenticate the data plane")

	wrong := get("Basic " + base64.StdEncoding.EncodeToString([]byte(username+":not-the-password")))
	wrong.Body.Close()
	require.Equal(t, http.StatusUnauthorized, wrong.StatusCode,
		"a wrong password must be refused")
}

func requireTerraformNetworkHost(t *testing.T) {
	t.Helper()
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Skipf("skipping: Terraform Network coverage requires host capabilities the simulator cannot provide here: %v", err)
	}
}

// tfStackWorkspace copies the checked-in stack configuration into a
// per-test directory. Running terraform in the checked-in directory made two
// concurrent runs share one state file and one provider lock: whichever
// started second wiped the first run's state mid-apply, and the first then
// re-planned resources it had just created — indistinguishable from a
// simulator refresh defect. A private workspace per run also leaves the tree
// free of state, lock, and crash artifacts.
func tfStackWorkspace(t *testing.T) string {
	t.Helper()
	src := tfStackDir()
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	require.NoError(t, err, "read the stack configuration at %s", src)
	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		require.NoError(t, err, "read %s", entry.Name())
		require.NoError(t, os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o600), "write %s", entry.Name())
		copied++
	}
	require.NotZero(t, copied, "no .tf files found in the stack configuration at %s", src)
	return dst
}

type tfOutputs map[string]struct {
	Sensitive bool        `json:"sensitive"`
	Type      interface{} `json:"type"`
	Value     interface{} `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

// mustList reads a list-of-strings output, which the application gateway's
// child-id outputs and the network manager's scope accesses are.
func (o tfOutputs) mustList(t *testing.T, key string) []string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	raw, ok := v.Value.([]any)
	require.True(t, ok, "output %q is not a list (got %T)", key, v.Value)
	values := make([]string, 0, len(raw))
	for _, entry := range raw {
		s, ok := entry.(string)
		require.True(t, ok, "output %q holds a non-string entry (got %T)", key, entry)
		values = append(values, s)
	}
	require.NotEmpty(t, values, "output %q is empty", key)
	return values
}

func readOutputs(t *testing.T, dir string) tfOutputs {
	t.Helper()
	out, err := terraformCmd(dir, "output", "-json").CombinedOutput()
	require.NoError(t, err, "terraform output failed:\n%s", out)
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(out, &outputs))
	return outputs
}
