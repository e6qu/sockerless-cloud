import { SERVICE_BLADES, browseRoute, bladeType, type ServiceBlade } from "./services.js";
import type { AzureIconName } from "./portal/icons.js";

// The services the console serves, as the portal's All services page,
// portal menu and global search list them: each with its All services
// category, its description, its icon, and the route that opens it. Every
// entry opens a page backed by real Azure APIs; a service the console has no
// page for is not listed.

export const CATEGORIES = [
  "General",
  "Compute",
  "Containers",
  "Databases",
  "Identity",
  "Integration",
  "Management and governance",
  "Monitor",
  "Networking",
  "Security",
  "Storage",
  "Web",
] as const;

export type Category = (typeof CATEGORIES)[number];

export interface PortalService {
  id: string;
  label: string;
  description: string;
  category: Category;
  icon: AzureIconName;
  color: string;
  route: string;
  /** The ARM resource type the service browses, when it browses one. */
  type?: string;
  keywords: string[];
}

interface Presentation {
  description: string;
  category: Category;
  icon: AzureIconName;
  color: string;
  keywords?: string[];
}

const BLADE_PRESENTATION: Record<string, Presentation> = {
  "resource-groups": { description: "Organize and manage Azure resources as a group.", category: "General", icon: "resource_group", color: "#0f6cbd", keywords: ["rg"] },
  "virtual-machines": { description: "Create Linux and Windows virtual machines in seconds.", category: "Compute", icon: "virtual_machine", color: "#0f6cbd", keywords: ["vm", "compute"] },
  "app-service": { description: "Build, deploy and scale web apps and APIs.", category: "Web", icon: "app_service", color: "#0f6cbd", keywords: ["web app", "website"] },
  "app-service-plans": { description: "Define the compute that runs your App Service apps.", category: "Web", icon: "app_service_plan", color: "#5c2e91" },
  "function-apps": { description: "Run event-driven code without managing infrastructure.", category: "Compute", icon: "function_app", color: "#a4262c", keywords: ["functions", "serverless"] },
  "container-instances": { description: "Run containers on demand in a serverless environment.", category: "Containers", icon: "container_instance", color: "#005b70", keywords: ["aci"] },
  containerapps: { description: "Run serverless containerized applications and microservices.", category: "Containers", icon: "container_app", color: "#5c2e91", keywords: ["aca"] },
  "container-app-jobs": { description: "Run containerized tasks that start, run for a while, and exit.", category: "Containers", icon: "container_app", color: "#8764b8", keywords: ["jobs"] },
  "container-app-environments": { description: "Host container apps and jobs in a secure boundary.", category: "Containers", icon: "environment", color: "#4f6bed" },
  "container-registries": { description: "Store and manage container images and artifacts.", category: "Containers", icon: "container_registry", color: "#0078d4", keywords: ["acr", "docker"] },
  "cosmos-db": { description: "A globally distributed, multi-model database service.", category: "Databases", icon: "cosmos", color: "#005b70", keywords: ["cosmos", "nosql"] },
  postgresql: { description: "A fully managed PostgreSQL database service.", category: "Databases", icon: "database", color: "#0f548c", keywords: ["postgres"] },
  redis: { description: "A fully managed in-memory cache built on Redis.", category: "Databases", icon: "database", color: "#a4262c", keywords: ["cache"] },
  "virtual-networks": { description: "Create private networks for your Azure resources.", category: "Networking", icon: "virtual_network", color: "#107c10", keywords: ["vnet"] },
  "load-balancers": { description: "Distribute traffic across your applications.", category: "Networking", icon: "load_balancer", color: "#107c10", keywords: ["lb"] },
  "network-security-groups": { description: "Filter network traffic to and from Azure resources.", category: "Networking", icon: "nsg", color: "#0f6cbd", keywords: ["nsg", "firewall"] },
  "public-ip-addresses": { description: "Reserve public IP addresses for your resources.", category: "Networking", icon: "public_ip", color: "#107c10", keywords: ["pip"] },
  "network-interfaces": { description: "Connect virtual machines to virtual networks.", category: "Networking", icon: "network_interface", color: "#107c10", keywords: ["nic"] },
  "route-tables": { description: "Control how network traffic is routed.", category: "Networking", icon: "virtual_network", color: "#0b6a0b" },
  "nat-gateways": { description: "Give private subnets outbound internet connectivity.", category: "Networking", icon: "public_ip", color: "#0b6a0b", keywords: ["nat"] },
  "dns-zones": { description: "Host your DNS domains in Azure.", category: "Networking", icon: "dns", color: "#005b70", keywords: ["dns"] },
  "private-dns-zones": { description: "Resolve names inside your virtual networks.", category: "Networking", icon: "dns", color: "#004e8c", keywords: ["dns"] },
  "key-vaults": { description: "Safeguard cryptographic keys and other secrets.", category: "Security", icon: "key_vault", color: "#986f0b", keywords: ["kv", "secrets"] },
  "service-bus": { description: "Connect applications with reliable cloud messaging.", category: "Integration", icon: "service_bus", color: "#0f6cbd", keywords: ["queue", "topic", "messaging"] },
  "event-hubs": { description: "Stream millions of events per second.", category: "Integration", icon: "event_hubs", color: "#0f548c", keywords: ["streaming", "kafka"] },
  "event-grid-topics": { description: "Route events from custom sources with Event Grid.", category: "Integration", icon: "event_grid", color: "#0078d4", keywords: ["event grid"] },
  "event-grid-domains": { description: "Manage many Event Grid topics as one domain.", category: "Integration", icon: "event_grid", color: "#005a9e", keywords: ["event grid"] },
  "event-grid-system-topics": { description: "Subscribe to events Azure services publish.", category: "Integration", icon: "event_grid", color: "#004578", keywords: ["event grid"] },
  "api-management": { description: "Publish, secure and analyze APIs.", category: "Integration", icon: "api_management", color: "#5c2e91", keywords: ["apim"] },
  "logic-apps": { description: "Automate workflows that connect apps and data.", category: "Integration", icon: "logic_app", color: "#0f6cbd", keywords: ["workflow"] },
  "application-insights": { description: "Monitor the performance and usage of your apps.", category: "Monitor", icon: "monitor", color: "#5c2e91", keywords: ["app insights", "apm"] },
  "log-analytics": { description: "Collect and analyze log data with Kusto queries.", category: "Monitor", icon: "monitor", color: "#0f548c", keywords: ["workspace", "logs"] },
  "managed-identities": { description: "Give Azure resources an identity in Microsoft Entra ID.", category: "Identity", icon: "identity", color: "#986f0b", keywords: ["msi", "identity"] },
  "storage-accounts": { description: "Durable, highly available and scalable cloud storage.", category: "Storage", icon: "storage", color: "#0f548c", keywords: ["blob", "file", "storage"] },
};

function bladeService(blade: ServiceBlade): PortalService {
  const presentation = BLADE_PRESENTATION[blade.slug];
  if (!presentation) throw new Error(`no presentation for service blade ${blade.slug}`);
  return {
    id: blade.slug,
    label: blade.label,
    description: presentation.description,
    category: presentation.category,
    icon: presentation.icon,
    color: presentation.color,
    route: browseRoute(blade),
    type: bladeType(blade),
    keywords: presentation.keywords ?? [],
  };
}

const VIEW_SERVICES: PortalService[] = [
  {
    id: "subscriptions",
    label: "Subscriptions",
    description: "Manage the subscriptions your resources are billed to.",
    category: "General",
    icon: "subscription",
    color: "#986f0b",
    route: "/ui/browse/subscriptions",
    keywords: ["billing"],
  },
  {
    id: "all-resources",
    label: "All resources",
    description: "Browse every resource in the subscriptions you can reach.",
    category: "General",
    icon: "apps",
    color: "#107c10",
    route: "/ui/browse/all",
    keywords: ["resources"],
  },
  {
    id: "entra",
    label: "Microsoft Entra ID",
    description: "Manage your directory's users, groups and applications.",
    category: "Identity",
    icon: "entra",
    color: "#0078d4",
    route: "/ui/entra",
    keywords: ["aad", "azure active directory", "directory"],
  },
  {
    id: "app-registrations",
    label: "App registrations",
    description: "Register applications to sign in with Microsoft Entra ID.",
    category: "Identity",
    icon: "entra",
    color: "#005a9e",
    route: "/ui/entra/app-registrations",
    keywords: ["application", "client secret"],
  },
  {
    id: "monitor",
    label: "Monitor",
    description: "Collect and analyze telemetry from your resources.",
    category: "Monitor",
    icon: "monitor",
    color: "#0078d4",
    route: "/ui/monitor",
    keywords: ["azure monitor", "logs", "activity log"],
  },
];

export const SERVICES: PortalService[] = [...VIEW_SERVICES, ...SERVICE_BLADES.map(bladeService)];

export const SERVICE_BY_ID = new Map(SERVICES.map((service) => [service.id, service]));

export function serviceById(id: string): PortalService {
  const service = SERVICE_BY_ID.get(id);
  if (!service) throw new Error(`unknown service ${id}`);
  return service;
}

/** The services the portal pins under Favorites until the operator changes
 *  them, in the portal's own default order. */
export const DEFAULT_FAVORITES = [
  "all-resources",
  "resource-groups",
  "app-service",
  "function-apps",
  "cosmos-db",
  "virtual-machines",
  "load-balancers",
  "storage-accounts",
  "virtual-networks",
  "entra",
  "monitor",
];

/** The services on Home's Azure services row, after "Create a resource". */
export const HOME_SERVICES = [
  "virtual-machines",
  "storage-accounts",
  "app-service",
  "function-apps",
  "containerapps",
  "key-vaults",
  "service-bus",
  "resource-groups",
  "subscriptions",
];

export function matchesService(service: PortalService, needle: string): boolean {
  const query = needle.trim().toLowerCase();
  if (!query) return true;
  return [service.label, service.description, ...service.keywords].some((text) => text.toLowerCase().includes(query));
}
