import { BLADE_API_VERSION, API_VERSION, type ArmGenericResource } from "./api.js";
import { armText } from "./portal/format.js";
import type { AzureIconName } from "./portal/icons.js";

// The service blades this portal serves, each one named by the real Azure
// Resource Manager operation behind it.
//
// A blade is a description, not an abstraction over Azure: `provider` +
// `apiVersion` are the literal ARM list path and api-version
// (`GET /subscriptions/{id}/providers/{provider}?api-version=…`), the column
// and Essentials `path` values are the exact property names the resource's
// Azure REST specification documents, and every `subResources` entry is a real
// child-collection List operation ARM nests under the parent's resource ID.
// One list-blade and one detail-blade implementation render them all, so a
// service reaches the portal by describing the operations it already serves
// rather than by hand-copying a page.

export interface BladeColumn {
  id: string;
  header: string;
  /** Where the value lives. A `properties.*` path reads the resource's own
   *  ARM properties; the envelope fields ARM puts beside them are named
   *  directly. */
  read: (resource: ArmGenericResource) => string;
}

export interface BladeSubResource {
  /** The child collection segment ARM appends to the parent's resource ID —
   *  a real sub-resource List operation (`subnets`, `queues`,
   *  `sqlDatabases`). Omitted when the children are an inline array on the
   *  parent's own properties, which is how ARM models a container group's
   *  containers or a load balancer's rule collections. */
  path?: string;
  /** Reads children that ARM returns inline on the parent resource rather
   *  than as their own collection endpoint. */
  inline?: (resource: ArmGenericResource) => ArmGenericResource[];
  title: string;
  /** Defaults to the blade's own api-version. */
  apiVersion?: string;
  columns: BladeColumn[];
  emptyTitle: string;
  emptyDescription?: string;
}

export interface BladeAction {
  label: string;
  icon: AzureIconName;
  /** The ARM action segment: `POST {resourceId}/{action}?api-version=…`. */
  action: string;
}

export interface ServiceBlade {
  /** The route slug under /ui/ — the blade lists at `/ui/{slug}` and opens a
   *  resource at `/ui/{slug}/{name}`. */
  slug: string;
  /** The service-menu label and the pane title. */
  label: string;
  /** The ARM resource-type display name Essentials and the pane lead with. */
  kind: string;
  /** The singular resource-title noun the detail pane shows. */
  detailKind: string;
  /** The plural lowercase noun the table's empty/counter copy uses. */
  noun: string;
  /** The provider path segment of the real subscription-wide List operation.
   *  Empty for resource groups, which ARM serves at their own path. */
  provider: string;
  apiVersion: string;
  /** The data-testid prefix every command and pane on this blade carries. */
  testid: string;
  columns: BladeColumn[];
  essentials: BladeColumn[];
  subResources?: BladeSubResource[];
  actions?: BladeAction[];
  /** The consequence copy the delete-confirm dialog shows. */
  deleteWarning: string;
  /** Narrows the subscription-wide List the way the real portal's own blade
   *  does when two blades share one ARM resource type (App Service and
   *  Function Apps both read Microsoft.Web/sites and split it on `kind`). */
  filter?: (resource: ArmGenericResource) => boolean;
}

/** Names a path inside the resource's own ARM `properties` object — the exact
 *  property name the resource's Azure REST specification documents. */
const prop =
  (path: string) =>
  (resource: ArmGenericResource): string =>
    armText(resource.properties, path);

const NAME: BladeColumn = { id: "name", header: "Name", read: (resource) => resource.name };
const LOCATION: BladeColumn = { id: "location", header: "Location", read: (resource) => resource.location };
const PROVISIONING: BladeColumn = {
  id: "provisioningState",
  header: "Provisioning state",
  read: prop("provisioningState"),
};
const SKU: BladeColumn = { id: "sku", header: "Pricing tier", read: (resource) => resource.sku.name ?? "" };
const TYPE: BladeColumn = { id: "type", header: "Type", read: (resource) => resource.type };

export const SERVICE_BLADES: ServiceBlade[] = [
  {
    slug: "resource-groups",
    label: "Resource groups",
    kind: "Resource group",
    detailKind: "Resource group",
    noun: "resource groups",
    // ResourceGroups_List is served at /subscriptions/{id}/resourceGroups, not
    // under /providers — the list blade reads it through its own function.
    provider: "",
    apiVersion: API_VERSION.resourceGroups,
    testid: "rg",
    columns: [NAME, LOCATION, PROVISIONING],
    essentials: [
      { id: "id", header: "Resource ID", read: (resource) => resource.id },
      LOCATION,
      PROVISIONING,
    ],
    subResources: [
      {
        path: "resources",
        title: "Resources",
        apiVersion: API_VERSION.resourceGroups,
        columns: [NAME, TYPE, LOCATION],
        emptyTitle: "No resources to display",
        emptyDescription: "Resources created in this resource group appear here.",
      },
    ],
    deleteWarning:
      "Deleting a resource group is permanent and deletes every resource inside it. This action can't be undone.",
  },
  {
    slug: "virtual-machines",
    label: "Virtual machines",
    kind: "Virtual machine",
    detailKind: "Virtual machine",
    noun: "virtual machines",
    provider: "Microsoft.Compute/virtualMachines",
    apiVersion: BLADE_API_VERSION.virtualMachines,
    testid: "vm",
    columns: [
      NAME,
      LOCATION,
      { id: "vmSize", header: "Size", read: prop("hardwareProfile.vmSize") },
      PROVISIONING,
    ],
    essentials: [
      { id: "vmId", header: "VM ID", read: prop("vmId") },
      { id: "vmSize", header: "Size", read: prop("hardwareProfile.vmSize") },
      { id: "computerName", header: "Computer name", read: prop("osProfile.computerName") },
      { id: "adminUsername", header: "Administrator", read: prop("osProfile.adminUsername") },
      { id: "image", header: "Image", read: prop("storageProfile.imageReference.offer") },
      { id: "osDisk", header: "OS disk", read: prop("storageProfile.osDisk.name") },
      LOCATION,
      PROVISIONING,
    ],
    actions: [
      { label: "Start", icon: "play", action: "start" },
      { label: "Restart", icon: "refresh", action: "restart" },
      { label: "Stop", icon: "stop", action: "powerOff" },
      { label: "Deallocate", icon: "stop", action: "deallocate" },
    ],
    deleteWarning:
      "Deleting a virtual machine is permanent and releases its compute. Its disks and network interfaces are separate resources and are not deleted with it. This action can't be undone.",
  },
  {
    slug: "app-service",
    label: "App Service",
    kind: "App Service (Web App)",
    detailKind: "App Service",
    noun: "app services",
    provider: "Microsoft.Web/sites",
    apiVersion: API_VERSION.sites,
    testid: "webapp",
    // Microsoft.Web/sites carries both web apps and function apps; the real
    // portal splits the one ARM list on `kind` between its App Services and
    // Function App blades, and the Function Apps blade in this portal already
    // covers the other half.
    filter: (resource) => !resource.kind.toLowerCase().includes("functionapp"),
    columns: [
      NAME,
      LOCATION,
      { id: "state", header: "Status", read: prop("state") },
      { id: "hostName", header: "Default domain", read: prop("defaultHostName") },
    ],
    essentials: [
      { id: "state", header: "Status", read: prop("state") },
      { id: "hostName", header: "Default domain", read: prop("defaultHostName") },
      { id: "serverFarmId", header: "App Service plan", read: prop("serverFarmId") },
      { id: "httpsOnly", header: "HTTPS only", read: prop("httpsOnly") },
      { id: "enabled", header: "Enabled", read: prop("enabled") },
      LOCATION,
    ],
    subResources: [
      {
        path: "hostNameBindings",
        title: "Custom domains",
        columns: [NAME, { id: "hostNameType", header: "Host name type", read: prop("hostNameType") }],
        emptyTitle: "No custom domains to display",
      },
      {
        path: "deployments",
        title: "Deployment history",
        columns: [
          NAME,
          { id: "status", header: "Status", read: prop("status") },
          { id: "message", header: "Message", read: prop("message") },
        ],
        emptyTitle: "No deployments to display",
      },
      {
        path: "slots",
        title: "Deployment slots",
        columns: [NAME, { id: "state", header: "Status", read: prop("state") }],
        emptyTitle: "No deployment slots to display",
      },
      {
        path: "sitecontainers",
        title: "Site containers",
        columns: [
          NAME,
          { id: "image", header: "Image", read: prop("image") },
          { id: "isMain", header: "Main container", read: prop("isMain") },
        ],
        emptyTitle: "No site containers to display",
      },
    ],
    actions: [
      { label: "Start", icon: "play", action: "start" },
      { label: "Restart", icon: "refresh", action: "restart" },
      { label: "Stop", icon: "stop", action: "stop" },
    ],
    deleteWarning:
      "Deleting an app is permanent and removes its deployments, custom domains, and deployment slots. This action can't be undone.",
  },
  {
    slug: "app-service-plans",
    label: "App Service plans",
    kind: "App Service plan",
    detailKind: "App Service plan",
    noun: "App Service plans",
    provider: "Microsoft.Web/serverfarms",
    apiVersion: API_VERSION.sites,
    testid: "plan",
    columns: [
      NAME,
      LOCATION,
      SKU,
      { id: "status", header: "Status", read: prop("status") },
    ],
    essentials: [
      SKU,
      { id: "tier", header: "Tier", read: (resource) => resource.sku.tier ?? "" },
      { id: "capacity", header: "Instance count", read: (resource) => String(resource.sku.capacity ?? "") },
      { id: "numberOfSites", header: "Apps", read: prop("numberOfSites") },
      { id: "status", header: "Status", read: prop("status") },
      LOCATION,
    ],
    subResources: [
      {
        path: "sites",
        title: "Apps",
        columns: [NAME, { id: "state", header: "Status", read: prop("state") }, LOCATION],
        emptyTitle: "No apps to display",
        emptyDescription: "Apps assigned to this plan appear here.",
      },
    ],
    deleteWarning:
      "Deleting an App Service plan is permanent. Azure rejects the delete while apps are still assigned to it. This action can't be undone.",
  },
  {
    slug: "container-instances",
    label: "Container instances",
    kind: "Container group",
    detailKind: "Container group",
    noun: "container groups",
    provider: "Microsoft.ContainerInstance/containerGroups",
    apiVersion: BLADE_API_VERSION.containerInstance,
    testid: "aci",
    columns: [
      NAME,
      LOCATION,
      { id: "osType", header: "OS type", read: prop("osType") },
      PROVISIONING,
    ],
    essentials: [
      { id: "osType", header: "OS type", read: prop("osType") },
      { id: "ip", header: "IP address", read: prop("ipAddress.ip") },
      { id: "fqdn", header: "FQDN", read: prop("ipAddress.fqdn") },
      { id: "restartPolicy", header: "Restart policy", read: prop("restartPolicy") },
      { id: "state", header: "Container group state", read: prop("instanceView.state") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        // ARM returns a container group's containers inline on the group
        // itself — there is no per-container collection endpoint.
        inline: (resource) => inlineChildren(resource, "containers"),
        title: "Containers",
        columns: [
          NAME,
          { id: "image", header: "Image", read: prop("image") },
          { id: "state", header: "State", read: prop("instanceView.currentState.state") },
          { id: "restarts", header: "Restarts", read: prop("instanceView.restartCount") },
        ],
        emptyTitle: "No containers to display",
      },
    ],
    actions: [
      { label: "Start", icon: "play", action: "start" },
      { label: "Restart", icon: "refresh", action: "restart" },
      { label: "Stop", icon: "stop", action: "stop" },
    ],
    deleteWarning:
      "Deleting a container group is permanent and stops every container in it. This action can't be undone.",
  },
  {
    slug: "containerapps",
    label: "Container Apps",
    kind: "Container App",
    detailKind: "Container App",
    noun: "container apps",
    provider: "Microsoft.App/containerApps",
    apiVersion: API_VERSION.jobs,
    testid: "capp",
    columns: [
      NAME,
      LOCATION,
      { id: "environment", header: "Environment", read: containerAppEnvironmentName },
      PROVISIONING,
    ],
    essentials: [
      { id: "environment", header: "Container Apps environment", read: containerAppEnvironmentName },
      { id: "workloadProfileName", header: "Workload profile", read: prop("workloadProfileName") },
      { id: "revisionMode", header: "Revision mode", read: prop("configuration.activeRevisionsMode") },
      { id: "latestRevisionName", header: "Latest revision", read: prop("latestRevisionName") },
      { id: "latestReadyRevisionName", header: "Latest ready revision", read: prop("latestReadyRevisionName") },
      { id: "fqdn", header: "Application Url", read: prop("latestRevisionFqdn") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        // ARM returns a container app's containers inline on the resource's
        // own template.containers, the same way it returns a container
        // group's containers inline — there is no per-container collection
        // endpoint.
        inline: (resource) => containerAppContainers(resource),
        title: "Containers",
        columns: [NAME, { id: "image", header: "Image", read: prop("image") }],
        emptyTitle: "No containers to display",
      },
    ],
    actions: [
      { label: "Start", icon: "play", action: "start" },
      { label: "Stop", icon: "stop", action: "stop" },
    ],
    deleteWarning:
      "Deleting a container app is permanent and stops every revision running under it. This action can't be undone.",
  },
  {
    slug: "container-app-environments",
    label: "Container Apps environments",
    kind: "Container Apps environment",
    detailKind: "Container Apps environment",
    noun: "environments",
    provider: "Microsoft.App/managedEnvironments",
    apiVersion: API_VERSION.jobs,
    testid: "cae",
    columns: [NAME, LOCATION, PROVISIONING],
    essentials: [
      { id: "defaultDomain", header: "Default domain", read: prop("defaultDomain") },
      { id: "staticIp", header: "Static IP", read: prop("staticIp") },
      { id: "workspace", header: "Log Analytics customer ID", read: prop("appLogsConfiguration.logAnalyticsConfiguration.customerId") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "storages",
        title: "Storages",
        columns: [
          NAME,
          { id: "shareName", header: "File share", read: prop("azureFile.shareName") },
          { id: "accessMode", header: "Access mode", read: prop("azureFile.accessMode") },
        ],
        emptyTitle: "No storages to display",
      },
    ],
    deleteWarning:
      "Deleting a Container Apps environment is permanent. Azure rejects the delete while apps or jobs still run in it. This action can't be undone.",
  },
  {
    slug: "cosmos-db",
    label: "Azure Cosmos DB",
    kind: "Cosmos DB account",
    detailKind: "Cosmos DB account",
    noun: "Cosmos DB accounts",
    provider: "Microsoft.DocumentDB/databaseAccounts",
    apiVersion: BLADE_API_VERSION.cosmos,
    testid: "cosmos",
    columns: [
      NAME,
      LOCATION,
      { id: "kind", header: "API", read: (resource) => resource.kind },
      PROVISIONING,
    ],
    essentials: [
      { id: "documentEndpoint", header: "URI", read: prop("documentEndpoint") },
      { id: "offerType", header: "Offer type", read: prop("databaseAccountOfferType") },
      { id: "consistency", header: "Default consistency", read: prop("consistencyPolicy.defaultConsistencyLevel") },
      { id: "kind", header: "API", read: (resource) => resource.kind },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "sqlDatabases",
        title: "SQL databases",
        columns: [NAME, { id: "rid", header: "Resource ID", read: prop("resource._rid") }],
        emptyTitle: "No SQL databases to display",
      },
      {
        path: "mongodbDatabases",
        title: "MongoDB databases",
        columns: [NAME],
        emptyTitle: "No MongoDB databases to display",
      },
      {
        path: "tables",
        title: "Tables",
        columns: [NAME],
        emptyTitle: "No tables to display",
      },
      {
        path: "cassandraKeyspaces",
        title: "Cassandra keyspaces",
        columns: [NAME],
        emptyTitle: "No Cassandra keyspaces to display",
      },
      {
        path: "gremlinDatabases",
        title: "Gremlin databases",
        columns: [NAME],
        emptyTitle: "No Gremlin databases to display",
      },
    ],
    deleteWarning:
      "Deleting a Cosmos DB account is permanent and removes every database, container, and item it holds. This action can't be undone.",
  },
  {
    slug: "postgresql",
    label: "Azure Database for PostgreSQL",
    kind: "PostgreSQL flexible server",
    detailKind: "PostgreSQL flexible server",
    noun: "flexible servers",
    provider: "Microsoft.DBforPostgreSQL/flexibleServers",
    apiVersion: BLADE_API_VERSION.postgresql,
    testid: "pg",
    columns: [
      NAME,
      LOCATION,
      { id: "version", header: "Version", read: prop("version") },
      { id: "state", header: "Status", read: prop("state") },
    ],
    essentials: [
      { id: "fqdn", header: "Server name", read: prop("fullyQualifiedDomainName") },
      { id: "version", header: "PostgreSQL version", read: prop("version") },
      { id: "adminLogin", header: "Administrator login", read: prop("administratorLogin") },
      { id: "storage", header: "Storage (GiB)", read: prop("storage.storageSizeGB") },
      { id: "backupDays", header: "Backup retention (days)", read: prop("backup.backupRetentionDays") },
      { id: "state", header: "Status", read: prop("state") },
      LOCATION,
    ],
    subResources: [
      {
        path: "databases",
        title: "Databases",
        columns: [
          NAME,
          { id: "charset", header: "Charset", read: prop("charset") },
          { id: "collation", header: "Collation", read: prop("collation") },
        ],
        emptyTitle: "No databases to display",
      },
      {
        path: "firewallRules",
        title: "Firewall rules",
        columns: [
          NAME,
          { id: "start", header: "Start IP", read: prop("startIpAddress") },
          { id: "end", header: "End IP", read: prop("endIpAddress") },
        ],
        emptyTitle: "No firewall rules to display",
      },
      {
        path: "configurations",
        title: "Server parameters",
        columns: [
          NAME,
          { id: "value", header: "Value", read: prop("value") },
          { id: "default", header: "Default", read: prop("defaultValue") },
        ],
        emptyTitle: "No server parameters to display",
      },
    ],
    actions: [
      { label: "Start", icon: "play", action: "start" },
      { label: "Restart", icon: "refresh", action: "restart" },
      { label: "Stop", icon: "stop", action: "stop" },
    ],
    deleteWarning:
      "Deleting a flexible server is permanent and removes every database and backup it holds. This action can't be undone.",
  },
  {
    slug: "redis",
    label: "Azure Cache for Redis",
    kind: "Azure Cache for Redis",
    detailKind: "Azure Cache for Redis",
    noun: "caches",
    provider: "Microsoft.Cache/Redis",
    apiVersion: BLADE_API_VERSION.redis,
    testid: "redis",
    columns: [
      NAME,
      LOCATION,
      { id: "sku", header: "Pricing tier", read: prop("sku.name") },
      PROVISIONING,
    ],
    essentials: [
      { id: "hostName", header: "Host name", read: prop("hostName") },
      { id: "sslPort", header: "SSL port", read: prop("sslPort") },
      { id: "redisVersion", header: "Redis version", read: prop("redisVersion") },
      { id: "sku", header: "Pricing tier", read: prop("sku.name") },
      { id: "nonSslPort", header: "Non-SSL port enabled", read: prop("enableNonSslPort") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "firewallRules",
        title: "Firewall rules",
        columns: [
          NAME,
          { id: "start", header: "Start IP", read: prop("startIP") },
          { id: "end", header: "End IP", read: prop("endIP") },
        ],
        emptyTitle: "No firewall rules to display",
      },
      {
        path: "patchSchedules",
        title: "Schedule updates",
        columns: [NAME, { id: "entries", header: "Windows", read: prop("scheduleEntries") }],
        emptyTitle: "No maintenance windows to display",
      },
      {
        path: "linkedServers",
        title: "Geo-replication",
        columns: [
          NAME,
          { id: "role", header: "Role", read: prop("serverRole") },
          { id: "region", header: "Region", read: prop("linkedRedisCacheLocation") },
        ],
        emptyTitle: "No linked servers to display",
      },
    ],
    deleteWarning:
      "Deleting a cache is permanent and discards everything it holds. This action can't be undone.",
  },
  {
    slug: "virtual-networks",
    label: "Virtual networks",
    kind: "Virtual network",
    detailKind: "Virtual network",
    noun: "virtual networks",
    provider: "Microsoft.Network/virtualNetworks",
    apiVersion: BLADE_API_VERSION.network,
    testid: "vnet",
    columns: [
      NAME,
      LOCATION,
      { id: "addressSpace", header: "Address space", read: prop("addressSpace.addressPrefixes") },
      PROVISIONING,
    ],
    essentials: [
      { id: "addressSpace", header: "Address space", read: prop("addressSpace.addressPrefixes") },
      { id: "dnsServers", header: "DNS servers", read: prop("dhcpOptions.dnsServers") },
      { id: "guid", header: "Resource GUID", read: prop("resourceGuid") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "subnets",
        title: "Subnets",
        columns: [
          NAME,
          { id: "prefix", header: "Address range", read: prop("addressPrefix") },
          { id: "nsg", header: "Network security group", read: prop("networkSecurityGroup.id") },
        ],
        emptyTitle: "No subnets to display",
      },
      {
        path: "virtualNetworkPeerings",
        title: "Peerings",
        columns: [
          NAME,
          { id: "peeringState", header: "Peering state", read: prop("peeringState") },
          { id: "remote", header: "Remote virtual network", read: prop("remoteVirtualNetwork.id") },
        ],
        emptyTitle: "No peerings to display",
      },
    ],
    deleteWarning:
      "Deleting a virtual network is permanent and removes its subnets and peerings. Azure rejects the delete while resources are still attached to it. This action can't be undone.",
  },
  {
    slug: "load-balancers",
    label: "Load balancers",
    kind: "Load balancer",
    detailKind: "Load balancer",
    noun: "load balancers",
    provider: "Microsoft.Network/loadBalancers",
    apiVersion: BLADE_API_VERSION.network,
    testid: "lb",
    columns: [NAME, LOCATION, SKU, PROVISIONING],
    essentials: [SKU, { id: "tier", header: "Tier", read: (resource) => resource.sku.tier ?? "" }, PROVISIONING, LOCATION],
    subResources: [
      {
        path: "frontendIPConfigurations",
        title: "Frontend IP configuration",
        columns: [
          NAME,
          { id: "privateIP", header: "Private IP", read: prop("privateIPAddress") },
          { id: "publicIP", header: "Public IP", read: prop("publicIPAddress.id") },
        ],
        emptyTitle: "No frontend IP configurations to display",
      },
      {
        path: "backendAddressPools",
        title: "Backend pools",
        columns: [NAME, { id: "provisioning", header: "Provisioning state", read: prop("provisioningState") }],
        emptyTitle: "No backend pools to display",
      },
      {
        path: "loadBalancingRules",
        title: "Load balancing rules",
        columns: [
          NAME,
          { id: "protocol", header: "Protocol", read: prop("protocol") },
          { id: "frontendPort", header: "Frontend port", read: prop("frontendPort") },
          { id: "backendPort", header: "Backend port", read: prop("backendPort") },
        ],
        emptyTitle: "No load balancing rules to display",
      },
      {
        path: "probes",
        title: "Health probes",
        columns: [
          NAME,
          { id: "protocol", header: "Protocol", read: prop("protocol") },
          { id: "port", header: "Port", read: prop("port") },
        ],
        emptyTitle: "No health probes to display",
      },
      {
        path: "inboundNatRules",
        title: "Inbound NAT rules",
        columns: [
          NAME,
          { id: "protocol", header: "Protocol", read: prop("protocol") },
          { id: "frontendPort", header: "Frontend port", read: prop("frontendPort") },
        ],
        emptyTitle: "No inbound NAT rules to display",
      },
    ],
    deleteWarning:
      "Deleting a load balancer is permanent and removes its rules, pools, and probes. This action can't be undone.",
  },
  {
    slug: "network-security-groups",
    label: "Network security groups",
    kind: "Network security group",
    detailKind: "Network security group",
    noun: "network security groups",
    provider: "Microsoft.Network/networkSecurityGroups",
    apiVersion: BLADE_API_VERSION.network,
    testid: "nsg",
    columns: [NAME, LOCATION, PROVISIONING],
    essentials: [
      { id: "guid", header: "Resource GUID", read: prop("resourceGuid") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "securityRules",
        title: "Inbound and outbound security rules",
        columns: [
          NAME,
          { id: "priority", header: "Priority", read: prop("priority") },
          { id: "direction", header: "Direction", read: prop("direction") },
          { id: "access", header: "Action", read: prop("access") },
          { id: "protocol", header: "Protocol", read: prop("protocol") },
        ],
        emptyTitle: "No security rules to display",
      },
      {
        path: "defaultSecurityRules",
        title: "Default security rules",
        columns: [
          NAME,
          { id: "priority", header: "Priority", read: prop("priority") },
          { id: "direction", header: "Direction", read: prop("direction") },
          { id: "access", header: "Action", read: prop("access") },
        ],
        emptyTitle: "No default security rules to display",
      },
    ],
    deleteWarning:
      "Deleting a network security group is permanent and removes its rules. Azure rejects the delete while it is still attached to a subnet or network interface. This action can't be undone.",
  },
  {
    slug: "public-ip-addresses",
    label: "Public IP addresses",
    kind: "Public IP address",
    detailKind: "Public IP address",
    noun: "public IP addresses",
    provider: "Microsoft.Network/publicIPAddresses",
    apiVersion: BLADE_API_VERSION.network,
    testid: "pip",
    columns: [
      NAME,
      LOCATION,
      { id: "ip", header: "IP address", read: prop("ipAddress") },
      { id: "allocation", header: "Allocation", read: prop("publicIPAllocationMethod") },
    ],
    essentials: [
      { id: "ip", header: "IP address", read: prop("ipAddress") },
      { id: "allocation", header: "Allocation", read: prop("publicIPAllocationMethod") },
      { id: "version", header: "IP version", read: prop("publicIPAddressVersion") },
      { id: "idleTimeout", header: "Idle timeout (minutes)", read: prop("idleTimeoutInMinutes") },
      SKU,
      PROVISIONING,
      LOCATION,
    ],
    deleteWarning:
      "Deleting a public IP address is permanent and releases the address. Azure rejects the delete while it is still associated with a resource. This action can't be undone.",
  },
  {
    slug: "network-interfaces",
    label: "Network interfaces",
    kind: "Network interface",
    detailKind: "Network interface",
    noun: "network interfaces",
    provider: "Microsoft.Network/networkInterfaces",
    apiVersion: BLADE_API_VERSION.network,
    testid: "nic",
    columns: [
      NAME,
      LOCATION,
      { id: "mac", header: "MAC address", read: prop("macAddress") },
      PROVISIONING,
    ],
    essentials: [
      { id: "mac", header: "MAC address", read: prop("macAddress") },
      { id: "vm", header: "Attached to", read: prop("virtualMachine.id") },
      { id: "nsg", header: "Network security group", read: prop("networkSecurityGroup.id") },
      { id: "accelerated", header: "Accelerated networking", read: prop("enableAcceleratedNetworking") },
      { id: "ipForwarding", header: "IP forwarding", read: prop("enableIPForwarding") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "ipConfigurations",
        title: "IP configurations",
        columns: [
          NAME,
          { id: "privateIP", header: "Private IP", read: prop("privateIPAddress") },
          { id: "allocation", header: "Allocation", read: prop("privateIPAllocationMethod") },
          { id: "subnet", header: "Subnet", read: prop("subnet.id") },
        ],
        emptyTitle: "No IP configurations to display",
      },
    ],
    deleteWarning:
      "Deleting a network interface is permanent. Azure rejects the delete while it is still attached to a virtual machine. This action can't be undone.",
  },
  {
    slug: "route-tables",
    label: "Route tables",
    kind: "Route table",
    detailKind: "Route table",
    noun: "route tables",
    provider: "Microsoft.Network/routeTables",
    apiVersion: BLADE_API_VERSION.network,
    testid: "rt",
    columns: [NAME, LOCATION, PROVISIONING],
    essentials: [
      { id: "bgp", header: "BGP route propagation", read: prop("disableBgpRoutePropagation") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "routes",
        title: "Routes",
        columns: [
          NAME,
          { id: "prefix", header: "Address prefix", read: prop("addressPrefix") },
          { id: "nextHopType", header: "Next hop type", read: prop("nextHopType") },
          { id: "nextHop", header: "Next hop address", read: prop("nextHopIpAddress") },
        ],
        emptyTitle: "No routes to display",
      },
    ],
    deleteWarning:
      "Deleting a route table is permanent and removes its routes. Azure rejects the delete while it is still associated with a subnet. This action can't be undone.",
  },
  {
    slug: "nat-gateways",
    label: "NAT gateways",
    kind: "NAT gateway",
    detailKind: "NAT gateway",
    noun: "NAT gateways",
    provider: "Microsoft.Network/natGateways",
    apiVersion: BLADE_API_VERSION.network,
    testid: "nat",
    columns: [NAME, LOCATION, SKU, PROVISIONING],
    essentials: [
      SKU,
      { id: "idleTimeout", header: "Idle timeout (minutes)", read: prop("idleTimeoutInMinutes") },
      { id: "publicIps", header: "Public IP addresses", read: prop("publicIpAddresses") },
      PROVISIONING,
      LOCATION,
    ],
    deleteWarning:
      "Deleting a NAT gateway is permanent and removes outbound connectivity from the subnets it serves. This action can't be undone.",
  },
  {
    slug: "dns-zones",
    label: "DNS zones",
    kind: "DNS zone",
    detailKind: "DNS zone",
    noun: "DNS zones",
    // Azure DNS spells the subscription-wide Zones_List path lowercase
    // ("dnszones"), unlike the resource-group-scoped list.
    provider: "Microsoft.Network/dnszones",
    apiVersion: BLADE_API_VERSION.dns,
    testid: "dns",
    columns: [
      NAME,
      { id: "recordSets", header: "Record sets", read: prop("numberOfRecordSets") },
      { id: "zoneType", header: "Zone type", read: prop("zoneType") },
      LOCATION,
    ],
    essentials: [
      { id: "nameServers", header: "Name servers", read: prop("nameServers") },
      { id: "recordSets", header: "Record sets", read: prop("numberOfRecordSets") },
      { id: "maxRecordSets", header: "Maximum record sets", read: prop("maxNumberOfRecordSets") },
      { id: "zoneType", header: "Zone type", read: prop("zoneType") },
      LOCATION,
    ],
    subResources: [
      {
        path: "recordsets",
        title: "Record sets",
        columns: [
          NAME,
          TYPE,
          { id: "ttl", header: "TTL", read: prop("TTL") },
          { id: "a", header: "A records", read: prop("ARecords") },
        ],
        emptyTitle: "No record sets to display",
      },
    ],
    deleteWarning:
      "Deleting a DNS zone is permanent and removes every record set in it. This action can't be undone.",
  },
  {
    slug: "private-dns-zones",
    label: "Private DNS zones",
    kind: "Private DNS zone",
    detailKind: "Private DNS zone",
    noun: "private DNS zones",
    provider: "Microsoft.Network/privateDnsZones",
    apiVersion: BLADE_API_VERSION.privateDns,
    testid: "pdns",
    columns: [
      NAME,
      { id: "recordSets", header: "Record sets", read: prop("numberOfRecordSets") },
      { id: "links", header: "Virtual network links", read: prop("numberOfVirtualNetworkLinks") },
      PROVISIONING,
    ],
    essentials: [
      { id: "recordSets", header: "Record sets", read: prop("numberOfRecordSets") },
      { id: "links", header: "Virtual network links", read: prop("numberOfVirtualNetworkLinks") },
      { id: "autoLinks", header: "Auto-registration links", read: prop("numberOfVirtualNetworkLinksWithRegistration") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "virtualNetworkLinks",
        title: "Virtual network links",
        columns: [
          NAME,
          { id: "vnet", header: "Virtual network", read: prop("virtualNetwork.id") },
          { id: "registration", header: "Auto-registration", read: prop("registrationEnabled") },
          { id: "linkState", header: "Link state", read: prop("virtualNetworkLinkState") },
        ],
        emptyTitle: "No virtual network links to display",
      },
      {
        path: "ALL",
        title: "Record sets",
        columns: [
          NAME,
          TYPE,
          { id: "ttl", header: "TTL", read: prop("ttl") },
          { id: "a", header: "A records", read: prop("aRecords") },
        ],
        emptyTitle: "No record sets to display",
      },
    ],
    deleteWarning:
      "Deleting a private DNS zone is permanent and removes its record sets and virtual network links. This action can't be undone.",
  },
  {
    slug: "key-vaults",
    label: "Key vaults",
    kind: "Key vault",
    detailKind: "Key vault",
    noun: "key vaults",
    provider: "Microsoft.KeyVault/vaults",
    apiVersion: BLADE_API_VERSION.keyVault,
    testid: "kv",
    columns: [
      NAME,
      LOCATION,
      { id: "vaultUri", header: "Vault URI", read: prop("vaultUri") },
      { id: "sku", header: "Pricing tier", read: prop("sku.name") },
    ],
    essentials: [
      { id: "vaultUri", header: "Vault URI", read: prop("vaultUri") },
      { id: "tenantId", header: "Directory ID", read: prop("tenantId") },
      { id: "sku", header: "Pricing tier", read: prop("sku.name") },
      { id: "rbac", header: "Permission model", read: prop("enableRbacAuthorization") },
      { id: "softDelete", header: "Soft-delete", read: prop("enableSoftDelete") },
      { id: "purgeProtection", header: "Purge protection", read: prop("enablePurgeProtection") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "privateEndpointConnections",
        title: "Private endpoint connections",
        columns: [
          NAME,
          { id: "status", header: "Connection state", read: prop("privateLinkServiceConnectionState.status") },
          PROVISIONING,
        ],
        emptyTitle: "No private endpoint connections to display",
      },
    ],
    deleteWarning:
      "Deleting a key vault removes it from the directory. With soft-delete enabled the vault and its secrets stay recoverable until the retention period lapses or the vault is purged.",
  },
  {
    slug: "service-bus",
    label: "Service Bus",
    kind: "Service Bus namespace",
    detailKind: "Service Bus namespace",
    noun: "namespaces",
    provider: "Microsoft.ServiceBus/namespaces",
    apiVersion: BLADE_API_VERSION.serviceBusNamespaces,
    testid: "sb",
    columns: [NAME, LOCATION, SKU, { id: "status", header: "Status", read: prop("status") }],
    essentials: [
      { id: "endpoint", header: "Service Bus endpoint", read: prop("serviceBusEndpoint") },
      SKU,
      { id: "tier", header: "Tier", read: (resource) => resource.sku.tier ?? "" },
      { id: "status", header: "Status", read: prop("status") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "queues",
        title: "Queues",
        apiVersion: BLADE_API_VERSION.serviceBus,
        columns: [
          NAME,
          { id: "status", header: "Status", read: prop("status") },
          { id: "active", header: "Active messages", read: prop("countDetails.activeMessageCount") },
          { id: "dlq", header: "Dead-letter messages", read: prop("countDetails.deadLetterMessageCount") },
        ],
        emptyTitle: "No queues to display",
      },
      {
        path: "topics",
        title: "Topics",
        apiVersion: BLADE_API_VERSION.serviceBus,
        columns: [
          NAME,
          { id: "status", header: "Status", read: prop("status") },
          { id: "subscriptions", header: "Subscriptions", read: prop("subscriptionCount") },
        ],
        emptyTitle: "No topics to display",
      },
      {
        path: "authorizationRules",
        title: "Shared access policies",
        apiVersion: BLADE_API_VERSION.serviceBus,
        columns: [NAME, { id: "rights", header: "Claims", read: prop("rights") }],
        emptyTitle: "No shared access policies to display",
      },
      {
        path: "disasterRecoveryConfigs",
        title: "Geo-recovery",
        apiVersion: BLADE_API_VERSION.serviceBus,
        columns: [
          NAME,
          { id: "role", header: "Role", read: prop("role") },
          { id: "partner", header: "Partner namespace", read: prop("partnerNamespace") },
        ],
        emptyTitle: "No geo-recovery aliases to display",
      },
    ],
    deleteWarning:
      "Deleting a Service Bus namespace is permanent and removes every queue, topic, and subscription in it, along with any messages they hold. This action can't be undone.",
  },
  {
    slug: "event-hubs",
    label: "Event Hubs",
    kind: "Event Hubs namespace",
    detailKind: "Event Hubs namespace",
    noun: "namespaces",
    provider: "Microsoft.EventHub/namespaces",
    apiVersion: BLADE_API_VERSION.eventHub,
    testid: "eh",
    columns: [NAME, LOCATION, SKU, { id: "status", header: "Status", read: prop("status") }],
    essentials: [
      { id: "endpoint", header: "Service Bus endpoint", read: prop("serviceBusEndpoint") },
      SKU,
      { id: "capacity", header: "Throughput units", read: (resource) => String(resource.sku.capacity ?? "") },
      { id: "autoInflate", header: "Auto-inflate", read: prop("isAutoInflateEnabled") },
      { id: "status", header: "Status", read: prop("status") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "eventhubs",
        title: "Event hubs",
        columns: [
          NAME,
          { id: "partitions", header: "Partitions", read: prop("partitionCount") },
          { id: "retention", header: "Retention (hours)", read: prop("messageRetentionInDays") },
          { id: "status", header: "Status", read: prop("status") },
        ],
        emptyTitle: "No event hubs to display",
      },
      {
        path: "authorizationRules",
        title: "Shared access policies",
        columns: [NAME, { id: "rights", header: "Claims", read: prop("rights") }],
        emptyTitle: "No shared access policies to display",
      },
    ],
    deleteWarning:
      "Deleting an Event Hubs namespace is permanent and removes every event hub and consumer group in it. This action can't be undone.",
  },
  {
    slug: "event-grid-topics",
    label: "Event Grid topics",
    kind: "Event Grid topic",
    detailKind: "Event Grid topic",
    noun: "topics",
    provider: "Microsoft.EventGrid/topics",
    apiVersion: BLADE_API_VERSION.eventGrid,
    testid: "egt",
    columns: [NAME, LOCATION, { id: "endpoint", header: "Endpoint", read: prop("endpoint") }, PROVISIONING],
    essentials: [
      { id: "endpoint", header: "Topic endpoint", read: prop("endpoint") },
      { id: "inputSchema", header: "Input schema", read: prop("inputSchema") },
      { id: "publicAccess", header: "Public network access", read: prop("publicNetworkAccess") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "eventSubscriptions",
        title: "Event subscriptions",
        columns: [
          NAME,
          { id: "endpointType", header: "Endpoint type", read: prop("destination.endpointType") },
          PROVISIONING,
        ],
        emptyTitle: "No event subscriptions to display",
      },
    ],
    deleteWarning:
      "Deleting an Event Grid topic is permanent and removes every event subscription on it. This action can't be undone.",
  },
  {
    slug: "event-grid-domains",
    label: "Event Grid domains",
    kind: "Event Grid domain",
    detailKind: "Event Grid domain",
    noun: "domains",
    provider: "Microsoft.EventGrid/domains",
    apiVersion: BLADE_API_VERSION.eventGrid,
    testid: "egd",
    columns: [NAME, LOCATION, { id: "endpoint", header: "Endpoint", read: prop("endpoint") }, PROVISIONING],
    essentials: [
      { id: "endpoint", header: "Domain endpoint", read: prop("endpoint") },
      { id: "inputSchema", header: "Input schema", read: prop("inputSchema") },
      { id: "autoCreate", header: "Auto-create topics", read: prop("autoCreateTopicWithFirstSubscription") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "topics",
        title: "Domain topics",
        columns: [NAME, PROVISIONING],
        emptyTitle: "No domain topics to display",
      },
      {
        path: "eventSubscriptions",
        title: "Event subscriptions",
        columns: [
          NAME,
          { id: "endpointType", header: "Endpoint type", read: prop("destination.endpointType") },
          PROVISIONING,
        ],
        emptyTitle: "No event subscriptions to display",
      },
    ],
    deleteWarning:
      "Deleting an Event Grid domain is permanent and removes every domain topic and event subscription under it. This action can't be undone.",
  },
  {
    slug: "event-grid-system-topics",
    label: "Event Grid system topics",
    kind: "Event Grid system topic",
    detailKind: "Event Grid system topic",
    noun: "system topics",
    provider: "Microsoft.EventGrid/systemTopics",
    apiVersion: BLADE_API_VERSION.eventGrid,
    testid: "egs",
    columns: [
      NAME,
      LOCATION,
      { id: "topicType", header: "Topic type", read: prop("topicType") },
      PROVISIONING,
    ],
    essentials: [
      { id: "source", header: "Source", read: prop("source") },
      { id: "topicType", header: "Topic type", read: prop("topicType") },
      { id: "metricResourceId", header: "Metric resource ID", read: prop("metricResourceId") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "eventSubscriptions",
        title: "Event subscriptions",
        columns: [
          NAME,
          { id: "endpointType", header: "Endpoint type", read: prop("destination.endpointType") },
          PROVISIONING,
        ],
        emptyTitle: "No event subscriptions to display",
      },
    ],
    deleteWarning:
      "Deleting a system topic is permanent and removes every event subscription on it. This action can't be undone.",
  },
  {
    slug: "api-management",
    label: "API Management services",
    kind: "API Management service",
    detailKind: "API Management service",
    noun: "API Management services",
    provider: "Microsoft.ApiManagement/service",
    apiVersion: BLADE_API_VERSION.apiManagement,
    testid: "apim",
    columns: [NAME, LOCATION, SKU, PROVISIONING],
    essentials: [
      { id: "gatewayUrl", header: "Gateway URL", read: prop("gatewayUrl") },
      { id: "portalUrl", header: "Developer portal URL", read: prop("developerPortalUrl") },
      { id: "publisherEmail", header: "Publisher email", read: prop("publisherEmail") },
      SKU,
      { id: "capacity", header: "Units", read: (resource) => String(resource.sku.capacity ?? "") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "apis",
        title: "APIs",
        columns: [
          NAME,
          { id: "displayName", header: "Display name", read: prop("displayName") },
          { id: "path", header: "Path", read: prop("path") },
          { id: "protocols", header: "Protocols", read: prop("protocols") },
        ],
        emptyTitle: "No APIs to display",
      },
      {
        path: "products",
        title: "Products",
        columns: [
          NAME,
          { id: "displayName", header: "Display name", read: prop("displayName") },
          { id: "state", header: "State", read: prop("state") },
          { id: "subscriptionRequired", header: "Requires subscription", read: prop("subscriptionRequired") },
        ],
        emptyTitle: "No products to display",
      },
      {
        path: "backends",
        title: "Backends",
        columns: [
          NAME,
          { id: "url", header: "Runtime URL", read: prop("url") },
          { id: "protocol", header: "Protocol", read: prop("protocol") },
        ],
        emptyTitle: "No backends to display",
      },
      {
        path: "namedValues",
        title: "Named values",
        columns: [
          NAME,
          { id: "displayName", header: "Display name", read: prop("displayName") },
          { id: "secret", header: "Secret", read: prop("secret") },
        ],
        emptyTitle: "No named values to display",
      },
      {
        path: "subscriptions",
        title: "Subscriptions",
        columns: [
          NAME,
          { id: "displayName", header: "Display name", read: prop("displayName") },
          { id: "state", header: "State", read: prop("state") },
          { id: "scope", header: "Scope", read: prop("scope") },
        ],
        emptyTitle: "No subscriptions to display",
      },
    ],
    deleteWarning:
      "Deleting an API Management service is permanent and removes every API, product, and subscription it holds. This action can't be undone.",
  },
  {
    slug: "logic-apps",
    label: "Logic apps",
    kind: "Logic app",
    detailKind: "Logic app",
    noun: "logic apps",
    provider: "Microsoft.Logic/workflows",
    apiVersion: BLADE_API_VERSION.logic,
    testid: "logic",
    columns: [
      NAME,
      LOCATION,
      { id: "state", header: "Status", read: prop("state") },
      PROVISIONING,
    ],
    essentials: [
      { id: "state", header: "Status", read: prop("state") },
      { id: "accessEndpoint", header: "Workflow URL", read: prop("accessEndpoint") },
      { id: "version", header: "Version", read: prop("version") },
      { id: "createdTime", header: "Created", read: prop("createdTime") },
      PROVISIONING,
      LOCATION,
    ],
    subResources: [
      {
        path: "triggers",
        title: "Triggers",
        columns: [
          NAME,
          { id: "state", header: "State", read: prop("state") },
          { id: "status", header: "Last run status", read: prop("status") },
        ],
        emptyTitle: "No triggers to display",
      },
      {
        path: "runs",
        title: "Runs history",
        columns: [
          NAME,
          { id: "status", header: "Status", read: prop("status") },
          { id: "startTime", header: "Start time", read: prop("startTime") },
          { id: "endTime", header: "End time", read: prop("endTime") },
        ],
        emptyTitle: "No runs to display",
      },
      {
        path: "versions",
        title: "Versions",
        columns: [NAME, { id: "createdTime", header: "Created", read: prop("createdTime") }],
        emptyTitle: "No versions to display",
      },
    ],
    deleteWarning:
      "Deleting a logic app is permanent and removes its run history. This action can't be undone.",
  },
  {
    slug: "application-insights",
    label: "Application Insights",
    kind: "Application Insights resource",
    detailKind: "Application Insights resource",
    noun: "Application Insights resources",
    provider: "Microsoft.Insights/components",
    apiVersion: BLADE_API_VERSION.applicationInsights,
    testid: "appi",
    columns: [
      NAME,
      LOCATION,
      { id: "appType", header: "Application type", read: prop("Application_Type") },
      { id: "appId", header: "Application ID", read: prop("AppId") },
    ],
    essentials: [
      { id: "appId", header: "Application ID", read: prop("AppId") },
      { id: "instrumentationKey", header: "Instrumentation key", read: prop("InstrumentationKey") },
      { id: "connectionString", header: "Connection string", read: prop("ConnectionString") },
      { id: "appType", header: "Application type", read: prop("Application_Type") },
      { id: "workspace", header: "Workspace", read: prop("WorkspaceResourceId") },
      LOCATION,
    ],
    deleteWarning:
      "Deleting an Application Insights resource is permanent and removes the telemetry it holds. This action can't be undone.",
  },
  {
    slug: "log-analytics",
    label: "Log Analytics workspaces",
    kind: "Log Analytics workspace",
    detailKind: "Log Analytics workspace",
    noun: "workspaces",
    provider: "Microsoft.OperationalInsights/workspaces",
    apiVersion: API_VERSION.workspaces,
    testid: "law",
    columns: [
      NAME,
      LOCATION,
      { id: "customerId", header: "Workspace ID", read: prop("customerId") },
      PROVISIONING,
    ],
    essentials: [
      { id: "customerId", header: "Workspace ID", read: prop("customerId") },
      { id: "sku", header: "Pricing tier", read: prop("sku.name") },
      { id: "retention", header: "Data retention (days)", read: prop("retentionInDays") },
      { id: "publicIngestion", header: "Public ingestion", read: prop("publicNetworkAccessForIngestion") },
      PROVISIONING,
      LOCATION,
    ],
    deleteWarning:
      "Deleting a Log Analytics workspace removes the logs it holds. A soft-deleted workspace stays recoverable for the retention period before it is purged.",
  },
  {
    slug: "managed-identities",
    label: "Managed Identities",
    kind: "User-assigned managed identity",
    detailKind: "User-assigned managed identity",
    noun: "managed identities",
    provider: "Microsoft.ManagedIdentity/userAssignedIdentities",
    apiVersion: BLADE_API_VERSION.managedIdentity,
    testid: "msi",
    columns: [
      NAME,
      LOCATION,
      { id: "clientId", header: "Client ID", read: prop("clientId") },
      { id: "principalId", header: "Object (principal) ID", read: prop("principalId") },
    ],
    essentials: [
      { id: "clientId", header: "Client ID", read: prop("clientId") },
      { id: "principalId", header: "Object (principal) ID", read: prop("principalId") },
      { id: "tenantId", header: "Directory (tenant) ID", read: prop("tenantId") },
      LOCATION,
    ],
    deleteWarning:
      "Deleting a user-assigned managed identity is permanent and revokes every role assignment granted to it. This action can't be undone.",
  },
];

// A container app's `environmentId` is the current field name; `managedEnvironmentId`
// is the deprecated alias the same v3 API still accepts and echoes back. Reading
// whichever the resource actually carries, then trailing off its resource-ID
// path, is the same short name the Container Apps job detail blade already
// shows for its own `environmentId`.
function containerAppEnvironmentName(resource: ArmGenericResource): string {
  const id = armText(resource.properties, "environmentId") || armText(resource.properties, "managedEnvironmentId");
  if (!id) return "";
  const segments = id.split("/");
  return segments[segments.length - 1] || "";
}

// containerAppContainers lifts a container app's `template.containers` — an
// ARM property array nested one level deeper than the container-group
// `containers` inlineChildren reads, and whose entries carry `image`,
// `command`, `env` etc. directly rather than under their own `properties`
// object — into the same envelope the sub-resource tables render.
function containerAppContainers(resource: ArmGenericResource): ArmGenericResource[] {
  const template = resource.properties["template"];
  const raw = template && typeof template === "object" ? (template as Record<string, unknown>)["containers"] : undefined;
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((entry): entry is Record<string, unknown> => entry !== null && typeof entry === "object")
    .map((entry) => ({
      id: `${resource.id}/containers/${String(entry.name ?? "")}`,
      name: String(entry.name ?? ""),
      type: "",
      location: "",
      kind: "",
      sku: {},
      tags: {},
      properties: entry,
    }));
}

// inlineChildren lifts an ARM property array that models child resources
// (a container group's `containers`) into the same envelope the sub-resource
// tables render, so one table implementation serves both a real child-
// collection List and the inline shape ARM uses where there is no collection
// endpoint.
function inlineChildren(resource: ArmGenericResource, path: string): ArmGenericResource[] {
  const raw = resource.properties[path];
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((entry): entry is Record<string, unknown> => entry !== null && typeof entry === "object")
    .map((entry) => ({
      id: `${resource.id}/${path}/${String(entry.name ?? "")}`,
      name: String(entry.name ?? ""),
      type: "",
      location: "",
      kind: "",
      sku: {},
      tags: {},
      properties: (entry.properties as Record<string, unknown> | undefined) ?? {},
    }));
}
