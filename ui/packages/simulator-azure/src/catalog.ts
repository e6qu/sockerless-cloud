import { SERVICE_BLADES } from "./services.js";

// The service catalog the portal's left-hand service menu is built from —
// grouped the way the real Azure portal's "All services" page groups its
// catalog (General, Compute, Containers, Storage, Databases, Networking,
// Integration, Monitoring + management, Identity, Microsoft Entra ID,
// Security, DevOps). Every group and every service name is a real Azure
// category and a real Azure service; what differs from the real portal is
// coverage, not naming.
//
// `supported` is a claim about this simulator, so it is derived rather than
// asserted: a service is supported exactly when the simulator serves the
// Azure Resource Manager operations a blade needs — a subscription-wide List
// to populate the blade, a Get to open a resource, and (where the real portal
// offers them) the sub-resource Lists, lifecycle action POSTs, tags PATCH and
// DELETE. Those services carry a blade descriptor in services.ts and this
// catalog takes their entries straight from it, so the menu can never claim a
// service the portal doesn't actually serve, nor mark one unsupported that it
// does. A service marked `supported: false` is one the simulator genuinely
// does not implement — no resource provider, no routes — and it routes to the
// shared "not implemented" page at a slug derived from its name, so the item
// stays a genuine, keyboard-reachable link rather than a dead end that looks
// clickable but isn't.

export interface CatalogItem {
  label: string;
  to: string;
  /** The Azure Resource Manager resource-type display name Essentials leads
   *  with — "Virtual machine", "Storage account", and so on. */
  kind: string;
  supported: boolean;
}

export interface CatalogGroup {
  label: string;
  items: CatalogItem[];
}

function slug(label: string): string {
  return label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/(^-|-$)/g, "");
}

function unsupported(label: string, kind: string): CatalogItem {
  return { label, to: `/ui/not-supported/${slug(label)}`, kind, supported: false };
}

// The blades whose pages are hand-written rather than descriptor-driven —
// each reads its own real ARM (or Microsoft Graph, or Log Analytics) surface
// and carries flows a generic blade has no shape for: the subscription alias
// API, the Container Apps job run history, the Function App settings and
// deployed functions, the Container Registry data-plane catalog, the storage
// account's blob browser, the Kusto log query, and the app-registration
// credential minting.
const HAND_WRITTEN: Record<string, CatalogItem> = {
  Subscriptions: { label: "Subscriptions", to: "/ui/subscriptions", kind: "Subscription", supported: true },
  "Function Apps": { label: "Function Apps", to: "/ui/functions", kind: "Function App", supported: true },
  // Microsoft.App/jobs — Container Apps' run-to-completion resource type, the
  // real Azure name for it being "Container App job". The route stays
  // /ui/container-apps: it predates this label and other suites navigate to
  // it directly. Microsoft.App/containerApps (the always-on resource type
  // "Container Apps" itself names) has its own descriptor-driven blade below.
  "Container App jobs": {
    label: "Container App jobs",
    to: "/ui/container-apps",
    kind: "Container Apps job",
    supported: true,
  },
  "Container registries": {
    label: "Container registries",
    to: "/ui/acr",
    kind: "Container registry",
    supported: true,
  },
  "Storage accounts": { label: "Storage accounts", to: "/ui/storage", kind: "Storage account", supported: true },
  Logs: { label: "Logs", to: "/ui/monitor", kind: "Log Analytics workspace", supported: true },
  "App registrations": {
    label: "App registrations",
    to: "/ui/entra/app-registrations",
    kind: "Microsoft Entra ID",
    supported: true,
  },
};

// bladed pulls a descriptor-driven service into the catalog by its label, so
// the menu entry and the blade can never drift apart.
function bladed(label: string): CatalogItem {
  const blade = SERVICE_BLADES.find((candidate) => candidate.label === label);
  if (!blade) {
    throw new Error(`catalog references "${label}", which has no service blade in services.ts`);
  }
  return { label: blade.label, to: `/ui/${blade.slug}`, kind: blade.kind, supported: true };
}

function handWritten(label: string): CatalogItem {
  const item = HAND_WRITTEN[label];
  if (!item) {
    throw new Error(`catalog references "${label}", which has no hand-written page`);
  }
  return item;
}

export const SERVICE_CATALOG: CatalogGroup[] = [
  {
    label: "General",
    items: [
      handWritten("Subscriptions"),
      bladed("Resource groups"),
      // Microsoft.Management is served only far enough to register a resource
      // provider against a management group; the management-group hierarchy
      // itself (create, list, move a subscription) has no routes.
      unsupported("Management groups", "Management group"),
    ],
  },
  {
    label: "Compute",
    items: [
      bladed("Virtual machines"),
      bladed("App Service"),
      bladed("App Service plans"),
      handWritten("Function Apps"),
    ],
  },
  {
    label: "Containers",
    items: [
      bladed("Container Apps"),
      handWritten("Container App jobs"),
      bladed("Container Apps environments"),
      // Microsoft.ContainerService has no routes in this simulator.
      unsupported("Azure Kubernetes Service", "Azure Kubernetes Service (AKS) cluster"),
      bladed("Container instances"),
      handWritten("Container registries"),
    ],
  },
  {
    label: "Storage",
    items: [handWritten("Storage accounts")],
  },
  {
    label: "Databases",
    items: [
      // Microsoft.Sql has no routes in this simulator.
      unsupported("Azure SQL", "SQL database"),
      bladed("Azure Cosmos DB"),
      bladed("Azure Database for PostgreSQL"),
      bladed("Azure Cache for Redis"),
    ],
  },
  {
    label: "Networking",
    items: [
      bladed("Virtual networks"),
      bladed("Load balancers"),
      bladed("Network security groups"),
      bladed("Public IP addresses"),
      bladed("Network interfaces"),
      bladed("Route tables"),
      bladed("NAT gateways"),
      bladed("DNS zones"),
      bladed("Private DNS zones"),
      // Microsoft.Cdn has no routes in this simulator.
      unsupported("Front Door and CDN profiles", "Front Door and CDN profile"),
    ],
  },
  {
    label: "Integration",
    items: [
      bladed("Service Bus"),
      bladed("Event Hubs"),
      bladed("Event Grid topics"),
      bladed("Event Grid domains"),
      bladed("Event Grid system topics"),
      bladed("API Management services"),
      bladed("Logic apps"),
    ],
  },
  {
    label: "Monitoring + management",
    items: [
      handWritten("Logs"),
      bladed("Log Analytics workspaces"),
      bladed("Application Insights"),
      // Microsoft.CostManagement / Microsoft.Billing have no routes in this
      // simulator.
      unsupported("Cost Management + Billing", "Billing account"),
    ],
  },
  {
    label: "Identity",
    items: [bladed("Managed Identities")],
  },
  {
    label: "Microsoft Entra ID",
    items: [handWritten("App registrations")],
  },
  {
    label: "Security",
    items: [
      bladed("Key vaults"),
      // Microsoft.Security has no routes in this simulator.
      unsupported("Microsoft Defender for Cloud", "Security posture"),
    ],
  },
  {
    label: "DevOps",
    items: [
      // Azure DevOps is not an Azure Resource Manager service at all; it has
      // its own dev.azure.com API surface, none of which this simulator
      // serves.
      unsupported("Azure DevOps organizations", "Azure DevOps organization"),
    ],
  },
];

const BY_TO = new Map<string, CatalogItem>(SERVICE_CATALOG.flatMap((group) => group.items).map((item) => [item.to, item]));

export function catalogItemForPath(pathname: string): CatalogItem | undefined {
  return BY_TO.get(pathname);
}
