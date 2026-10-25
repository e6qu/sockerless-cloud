import { API_VERSION, BLADE_API_VERSION } from "../../api.js";
import { RESOURCE_GROUP_TYPE } from "../../services.js";
import { createRoute } from "../../portal/routes.js";

// The create experiences the console offers, each the portal's wizard for
// that resource: the Basics tab (project and instance details), optional
// Networking and Advanced tabs, Tags, and Review + create. A spec names the
// ARM PUT the wizard deploys.

export type CreateTab = "Basics" | "Networking" | "Advanced";

export interface CreateField {
  id: string;
  label: string;
  tab: CreateTab;
  section: string;
  /** Choices render as a dropdown, or as radios when `radio` is set. */
  options?: { value: string; label: string }[];
  radio?: boolean;
  checkbox?: boolean;
  defaultValue: string;
  required?: boolean;
  pattern?: RegExp;
  rule?: string;
  info?: string;
}

export interface CreateContext {
  subscriptionId: string;
  tenantId: string;
  resourceGroup: string;
  location: string;
  name: string;
  tags: Record<string, string>;
  values: Record<string, string>;
}

export interface CreateSpec {
  id: string;
  /** The ARM resource type the wizard creates. */
  type: string;
  kind?: string;
  serviceId: string;
  title: string;
  /** The resource the portal names its deployment after, as in
   *  "Microsoft.StorageAccount-20261025120000". */
  deploymentPrefix: string;
  nameLabel: string;
  namePattern: RegExp;
  nameRule: string;
  /** ARM's checkNameAvailability for globally unique names. */
  checkName?: { provider: string; apiVersion: string; type: string };
  apiVersion: string;
  fields: CreateField[];
  body: (context: CreateContext) => unknown;
  route: string;
}

const yes = { value: "true", label: "Enabled" };
const no = { value: "false", label: "Disabled" };

function spec(definition: Omit<CreateSpec, "route">): CreateSpec {
  return { ...definition, route: createRoute(definition.kind ? `${definition.type}/kind/${definition.kind}` : definition.type) };
}

export const CREATE_SPECS: CreateSpec[] = [
  spec({
    id: "resource-group",
    type: RESOURCE_GROUP_TYPE,
    serviceId: "resource-groups",
    title: "Create a resource group",
    deploymentPrefix: "Microsoft.ResourceGroup",
    nameLabel: "Resource group",
    namePattern: /^[-\w._()]{0,89}[-\w_()]$/u,
    nameRule: "Resource group names can be up to 90 characters: letters, digits, underscores, parentheses, hyphens and periods, and can't end in a period.",
    apiVersion: API_VERSION.resourceGroups,
    fields: [],
    body: ({ location, tags }) => ({ location, tags }),
  }),
  spec({
    id: "storage-account",
    type: "Microsoft.Storage/storageAccounts",
    serviceId: "storage-accounts",
    title: "Create a storage account",
    deploymentPrefix: "Microsoft.StorageAccount",
    nameLabel: "Storage account name",
    namePattern: /^[a-z0-9]{3,24}$/,
    nameRule: "The name must be 3 to 24 characters long and contain only lowercase letters and numbers.",
    checkName: { provider: "Microsoft.Storage", apiVersion: API_VERSION.storage, type: "Microsoft.Storage/storageAccounts" },
    apiVersion: API_VERSION.storage,
    fields: [
      {
        id: "performance",
        label: "Performance",
        tab: "Basics",
        section: "Instance details",
        radio: true,
        options: [
          { value: "Standard", label: "Standard: Recommended for most scenarios (general-purpose v2 account)" },
          { value: "Premium", label: "Premium: Recommended for scenarios that require low latency" },
        ],
        defaultValue: "Standard",
      },
      {
        id: "redundancy",
        label: "Redundancy",
        tab: "Basics",
        section: "Instance details",
        options: [
          { value: "LRS", label: "Locally-redundant storage (LRS)" },
          { value: "ZRS", label: "Zone-redundant storage (ZRS)" },
          { value: "GRS", label: "Geo-redundant storage (GRS)" },
          { value: "RAGRS", label: "Read-access geo-redundant storage (RA-GRS)" },
        ],
        defaultValue: "LRS",
      },
      { id: "httpsOnly", label: "Require secure transfer for REST API operations", tab: "Advanced", section: "Security", checkbox: true, defaultValue: "true" },
      { id: "allowBlobPublicAccess", label: "Allow enabling anonymous access on individual containers", tab: "Advanced", section: "Security", checkbox: true, defaultValue: "false" },
      {
        id: "minimumTlsVersion",
        label: "Minimum TLS version",
        tab: "Advanced",
        section: "Security",
        options: [
          { value: "TLS1_2", label: "Version 1.2" },
          { value: "TLS1_1", label: "Version 1.1" },
          { value: "TLS1_0", label: "Version 1.0" },
        ],
        defaultValue: "TLS1_2",
      },
      {
        id: "accessTier",
        label: "Access tier",
        tab: "Advanced",
        section: "Blob storage",
        radio: true,
        options: [
          { value: "Hot", label: "Hot: Optimized for frequently accessed data" },
          { value: "Cool", label: "Cool: Optimized for infrequently accessed data" },
        ],
        defaultValue: "Hot",
      },
      {
        id: "publicNetworkAccess",
        label: "Public network access",
        tab: "Networking",
        section: "Network connectivity",
        radio: true,
        options: [
          { value: "Enabled", label: "Enable public access from all networks" },
          { value: "Disabled", label: "Disable public access and use private access" },
        ],
        defaultValue: "Enabled",
      },
    ],
    body: ({ location, tags, values }) => ({
      location,
      tags,
      kind: values.performance === "Premium" ? "BlockBlobStorage" : "StorageV2",
      sku: { name: `${values.performance}_${values.redundancy}` },
      properties: {
        supportsHttpsTrafficOnly: values.httpsOnly === "true",
        allowBlobPublicAccess: values.allowBlobPublicAccess === "true",
        minimumTlsVersion: values.minimumTlsVersion,
        accessTier: values.accessTier,
        publicNetworkAccess: values.publicNetworkAccess,
      },
    }),
  }),
  spec({
    id: "key-vault",
    type: "Microsoft.KeyVault/vaults",
    serviceId: "key-vaults",
    title: "Create a key vault",
    deploymentPrefix: "Microsoft.KeyVault",
    nameLabel: "Key vault name",
    namePattern: /^(?!.*--)[a-zA-Z][a-zA-Z0-9-]{1,22}[a-zA-Z0-9]$/,
    nameRule: "The name must be 3 to 24 characters, start with a letter, end with a letter or digit, and contain only letters, digits and single hyphens.",
    checkName: { provider: "Microsoft.KeyVault", apiVersion: BLADE_API_VERSION.keyVault, type: "Microsoft.KeyVault/vaults" },
    apiVersion: BLADE_API_VERSION.keyVault,
    fields: [
      { id: "sku", label: "Pricing tier", tab: "Basics", section: "Instance details", options: [{ value: "standard", label: "Standard" }, { value: "premium", label: "Premium" }], defaultValue: "standard" },
      { id: "softDeleteRetentionInDays", label: "Days to retain deleted vaults", tab: "Basics", section: "Recovery options", defaultValue: "90", pattern: /^(?:[7-9]|[1-8][0-9]|90)$/, rule: "Enter a number of days from 7 to 90." },
      { id: "enablePurgeProtection", label: "Purge protection", tab: "Basics", section: "Recovery options", radio: true, options: [{ value: "false", label: "Disable purge protection" }, { value: "true", label: "Enable purge protection (enforce a mandatory retention period for deleted vaults and vault objects)" }], defaultValue: "false" },
      { id: "enableRbacAuthorization", label: "Permission model", tab: "Advanced", section: "Access configuration", radio: true, options: [{ value: "true", label: "Azure role-based access control (recommended)" }, { value: "false", label: "Vault access policy" }], defaultValue: "true" },
      { id: "publicNetworkAccess", label: "Enable public access", tab: "Networking", section: "Network connectivity", options: [yes, no].map((option) => ({ value: option.value === "true" ? "Enabled" : "Disabled", label: option.label })), defaultValue: "Enabled" },
    ],
    body: ({ location, tags, tenantId, values }) => ({
      location,
      tags,
      properties: {
        tenantId,
        sku: { family: "A", name: values.sku },
        enableSoftDelete: true,
        softDeleteRetentionInDays: Number(values.softDeleteRetentionInDays),
        ...(values.enablePurgeProtection === "true" ? { enablePurgeProtection: true } : {}),
        enableRbacAuthorization: values.enableRbacAuthorization === "true",
        accessPolicies: [],
        publicNetworkAccess: values.publicNetworkAccess,
      },
    }),
  }),
  spec({
    id: "service-bus",
    type: "Microsoft.ServiceBus/namespaces",
    serviceId: "service-bus",
    title: "Create namespace",
    deploymentPrefix: "Microsoft.ServiceBus",
    nameLabel: "Namespace name",
    namePattern: /^[a-zA-Z][a-zA-Z0-9-]{4,48}[a-zA-Z0-9]$/,
    nameRule: "The namespace name must be 6 to 50 characters, start with a letter, end with a letter or number, and contain only letters, numbers and hyphens.",
    apiVersion: BLADE_API_VERSION.serviceBusNamespaces,
    fields: [
      { id: "sku", label: "Pricing tier", tab: "Basics", section: "Instance details", options: ["Basic", "Standard", "Premium"].map((value) => ({ value, label: value })), defaultValue: "Standard" },
      { id: "minimumTlsVersion", label: "Minimum TLS version", tab: "Advanced", section: "Security", options: ["1.2", "1.1", "1.0"].map((value) => ({ value, label: value })), defaultValue: "1.2" },
      { id: "disableLocalAuth", label: "Local Authentication", tab: "Advanced", section: "Security", radio: true, options: [{ value: "false", label: "Enabled" }, { value: "true", label: "Disabled" }], defaultValue: "false" },
      { id: "publicNetworkAccess", label: "Connectivity method", tab: "Networking", section: "Network connectivity", radio: true, options: [{ value: "Enabled", label: "Public access" }, { value: "Disabled", label: "Private access" }], defaultValue: "Enabled" },
    ],
    body: ({ location, tags, values }) => ({
      location,
      tags,
      sku: { name: values.sku, tier: values.sku },
      properties: {
        minimumTlsVersion: values.minimumTlsVersion,
        disableLocalAuth: values.disableLocalAuth === "true",
        publicNetworkAccess: values.publicNetworkAccess,
      },
    }),
  }),
  spec({
    id: "virtual-network",
    type: "Microsoft.Network/virtualNetworks",
    serviceId: "virtual-networks",
    title: "Create virtual network",
    deploymentPrefix: "Microsoft.VirtualNetwork",
    nameLabel: "Virtual network name",
    namePattern: /^[a-zA-Z0-9](?:[a-zA-Z0-9._-]{0,62}[a-zA-Z0-9_])?$/,
    nameRule: "The name must begin with a letter or number, end with a letter, number or underscore, and may contain only letters, numbers, underscores, periods, or hyphens.",
    apiVersion: BLADE_API_VERSION.network,
    fields: [
      { id: "addressSpace", label: "IPv4 address space", tab: "Networking", section: "IP addresses", defaultValue: "10.0.0.0/16", required: true, pattern: /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/, rule: "Enter an address range in CIDR notation, such as 10.0.0.0/16." },
      { id: "subnetName", label: "Subnet name", tab: "Networking", section: "Subnets", defaultValue: "default", required: true },
      { id: "subnetPrefix", label: "Subnet address range", tab: "Networking", section: "Subnets", defaultValue: "10.0.0.0/24", required: true, pattern: /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/, rule: "Enter an address range in CIDR notation, such as 10.0.0.0/24." },
    ],
    body: ({ location, tags, values }) => ({
      location,
      tags,
      properties: {
        addressSpace: { addressPrefixes: [values.addressSpace] },
        subnets: [{ name: values.subnetName, properties: { addressPrefix: values.subnetPrefix } }],
      },
    }),
  }),
  spec({
    id: "container-registry",
    type: "Microsoft.ContainerRegistry/registries",
    serviceId: "container-registries",
    title: "Create container registry",
    deploymentPrefix: "Microsoft.ContainerRegistry",
    nameLabel: "Registry name",
    namePattern: /^[a-zA-Z0-9]{5,50}$/,
    nameRule: "Registry names must be 5 to 50 alphanumeric characters.",
    checkName: { provider: "Microsoft.ContainerRegistry", apiVersion: API_VERSION.registries, type: "Microsoft.ContainerRegistry/registries" },
    apiVersion: API_VERSION.registries,
    fields: [
      { id: "sku", label: "Pricing plan", tab: "Basics", section: "Instance details", options: ["Basic", "Standard", "Premium"].map((value) => ({ value, label: value })), defaultValue: "Standard" },
      { id: "adminUserEnabled", label: "Admin user", tab: "Advanced", section: "Authentication", checkbox: true, defaultValue: "false" },
    ],
    body: ({ location, tags, values }) => ({ location, tags, sku: { name: values.sku }, properties: { adminUserEnabled: values.adminUserEnabled === "true" } }),
  }),
  spec({
    id: "managed-identity",
    type: "Microsoft.ManagedIdentity/userAssignedIdentities",
    serviceId: "managed-identities",
    title: "Create User Assigned Managed Identity",
    deploymentPrefix: "Microsoft.ManagedIdentity",
    nameLabel: "Name",
    namePattern: /^[a-zA-Z0-9][a-zA-Z0-9_-]{2,127}$/,
    nameRule: "The name must be 3 to 128 characters, start with a letter or number, and contain only letters, numbers, hyphens and underscores.",
    apiVersion: BLADE_API_VERSION.managedIdentity,
    fields: [],
    body: ({ location, tags }) => ({ location, tags }),
  }),
  spec({
    id: "network-security-group",
    type: "Microsoft.Network/networkSecurityGroups",
    serviceId: "network-security-groups",
    title: "Create network security group",
    deploymentPrefix: "Microsoft.NetworkSecurityGroup",
    nameLabel: "Name",
    namePattern: /^[a-zA-Z0-9](?:[a-zA-Z0-9._-]{0,78}[a-zA-Z0-9_])?$/,
    nameRule: "The name must begin with a letter or number, end with a letter, number or underscore, and may contain only letters, numbers, underscores, periods, or hyphens.",
    apiVersion: BLADE_API_VERSION.network,
    fields: [],
    body: ({ location, tags }) => ({ location, tags, properties: {} }),
  }),
  spec({
    id: "public-ip",
    type: "Microsoft.Network/publicIPAddresses",
    serviceId: "public-ip-addresses",
    title: "Create public IP address",
    deploymentPrefix: "Microsoft.PublicIpAddress",
    nameLabel: "Name",
    namePattern: /^[a-zA-Z0-9](?:[a-zA-Z0-9._-]{0,78}[a-zA-Z0-9_])?$/,
    nameRule: "The name must begin with a letter or number, end with a letter, number or underscore, and may contain only letters, numbers, underscores, periods, or hyphens.",
    apiVersion: BLADE_API_VERSION.network,
    fields: [
      { id: "version", label: "IP version", tab: "Basics", section: "Configuration details", radio: true, options: [{ value: "IPv4", label: "IPv4" }, { value: "IPv6", label: "IPv6" }], defaultValue: "IPv4" },
      { id: "tier", label: "Tier", tab: "Basics", section: "Configuration details", radio: true, options: [{ value: "Regional", label: "Regional" }, { value: "Global", label: "Global" }], defaultValue: "Regional" },
      { id: "dns", label: "DNS name label", tab: "Basics", section: "Configuration details", defaultValue: "", pattern: /^$|^[a-z][a-z0-9-]{1,61}[a-z0-9]$/, rule: "Use 3 to 63 lowercase letters, numbers and hyphens, starting with a letter." },
    ],
    body: ({ location, tags, values }) => ({
      location,
      tags,
      sku: { name: "Standard", tier: values.tier },
      properties: {
        publicIPAddressVersion: values.version,
        publicIPAllocationMethod: "Static",
        ...(values.dns ? { dnsSettings: { domainNameLabel: values.dns } } : {}),
      },
    }),
  }),
  spec({
    id: "log-analytics",
    type: "Microsoft.OperationalInsights/workspaces",
    serviceId: "log-analytics",
    title: "Create Log Analytics workspace",
    deploymentPrefix: "Microsoft.LogAnalyticsOMS",
    nameLabel: "Name",
    namePattern: /^[a-zA-Z0-9][a-zA-Z0-9-]{2,61}[a-zA-Z0-9]$/,
    nameRule: "The name must be 4 to 63 characters and contain only letters, numbers and hyphens, starting and ending with a letter or number.",
    apiVersion: API_VERSION.workspaces,
    fields: [{ id: "retentionInDays", label: "Data retention (days)", tab: "Basics", section: "Instance details", defaultValue: "30", pattern: /^\d+$/, rule: "Enter a number of days." }],
    body: ({ location, tags, values }) => ({ location, tags, properties: { sku: { name: "PerGB2018" }, retentionInDays: Number(values.retentionInDays) } }),
  }),
];

export function specForRoute(type: string, kind = ""): CreateSpec | undefined {
  return CREATE_SPECS.find((entry) => entry.type.toLowerCase() === type.toLowerCase() && (entry.kind ?? "") === kind);
}
