import type { IconName } from "./icons.js";

// The console's product catalog: every product this console has pages for,
// grouped into the categories the real console's "View all products" page
// uses, each with the side navigation the product shows and the documentation
// its help menu links to. A product appears here only when the APIs its pages
// read are served; there are no placeholder entries.
//
// Paths follow the real console's URL structure under the /ui/ base
// (console.cloud.google.com/compute/instances → /ui/compute/instances); the
// selected project rides on every URL as ?project=, as it does there.

export interface NavItem {
  label: string;
  to: string;
  /** Further path prefixes that mark this item current (detail and create pages). */
  match?: string[];
}

export interface NavSection {
  heading?: string;
  items: NavItem[];
}

export interface ConsoleProduct {
  id: string;
  name: string;
  icon: IconName;
  category: string;
  description: string;
  docs: string;
  nav: NavSection[];
}

export const CATEGORIES = [
  "Compute",
  "Serverless",
  "Storage",
  "Databases",
  "Networking",
  "Operations",
  "Analytics",
  "CI/CD",
  "Integration services",
  "Security",
  "Management",
] as const;

export const PRODUCTS: ConsoleProduct[] = [
  {
    id: "compute",
    name: "Compute Engine",
    icon: "computer",
    category: "Compute",
    description: "Virtual machines running in Google's data centers",
    docs: "https://cloud.google.com/compute/docs",
    nav: [
      {
        heading: "Virtual machines",
        items: [
          {
            label: "VM instances",
            to: "/ui/compute/instances",
            match: ["/ui/compute/instancesAdd", "/ui/compute/instancesDetail"],
          },
        ],
      },
      { heading: "Storage", items: [{ label: "Disks", to: "/ui/compute/disks" }] },
    ],
  },
  {
    id: "run",
    name: "Cloud Run",
    icon: "deployed_code",
    category: "Serverless",
    description: "Run containers on a fully managed platform",
    docs: "https://cloud.google.com/run/docs",
    nav: [
      {
        items: [
          { label: "Services", to: "/ui/run", match: ["/ui/run/detail", "/ui/run/create"] },
          { label: "Jobs", to: "/ui/run/jobs" },
        ],
      },
    ],
  },
  {
    id: "functions",
    name: "Cloud Run functions",
    icon: "function",
    category: "Serverless",
    description: "Event-driven functions that run on Cloud Run",
    docs: "https://cloud.google.com/functions/docs",
    nav: [{ items: [{ label: "Functions", to: "/ui/functions/list", match: ["/ui/functions/details"] }] }],
  },
  {
    id: "storage",
    name: "Cloud Storage",
    icon: "storage",
    category: "Storage",
    description: "Object storage for companies of all sizes",
    docs: "https://cloud.google.com/storage/docs",
    nav: [
      {
        items: [{ label: "Buckets", to: "/ui/storage/browser", match: ["/ui/storage/create-bucket"] }],
      },
    ],
  },
  {
    id: "sql",
    name: "SQL",
    icon: "database",
    category: "Databases",
    description: "Managed MySQL, PostgreSQL and SQL Server",
    docs: "https://cloud.google.com/sql/docs",
    nav: [{ items: [{ label: "Instances", to: "/ui/sql/instances" }] }],
  },
  {
    id: "firestore",
    name: "Firestore",
    icon: "data_object",
    category: "Databases",
    description: "Serverless document database",
    docs: "https://cloud.google.com/firestore/docs",
    nav: [{ items: [{ label: "Databases", to: "/ui/firestore/databases" }] }],
  },
  {
    id: "spanner",
    name: "Spanner",
    icon: "table_chart",
    category: "Databases",
    description: "Globally distributed relational database",
    docs: "https://cloud.google.com/spanner/docs",
    nav: [{ items: [{ label: "Instances", to: "/ui/spanner/instances" }] }],
  },
  {
    id: "bigtable",
    name: "Bigtable",
    icon: "grid_view",
    category: "Databases",
    description: "Wide-column NoSQL database for large analytical and operational workloads",
    docs: "https://cloud.google.com/bigtable/docs",
    nav: [{ items: [{ label: "Instances", to: "/ui/bigtable/instances" }] }],
  },
  {
    id: "memorystore",
    name: "Memorystore",
    icon: "memory",
    category: "Databases",
    description: "Managed Redis for in-memory caching",
    docs: "https://cloud.google.com/memorystore/docs/redis",
    nav: [{ heading: "Redis", items: [{ label: "Instances", to: "/ui/memorystore/redis/instances" }] }],
  },
  {
    id: "networking",
    name: "VPC network",
    icon: "lan",
    category: "Networking",
    description: "Virtual private networks, subnets and firewall rules",
    docs: "https://cloud.google.com/vpc/docs",
    nav: [
      {
        items: [
          { label: "VPC networks", to: "/ui/networking/networks/list", match: ["/ui/networking/networks"] },
          { label: "Serverless VPC access", to: "/ui/networking/connectors/list" },
        ],
      },
    ],
  },
  {
    id: "net-services",
    name: "Network services",
    icon: "router",
    category: "Networking",
    description: "Load balancing and Cloud DNS",
    docs: "https://cloud.google.com/load-balancing/docs",
    nav: [
      {
        items: [
          { label: "Load balancing", to: "/ui/net-services/loadbalancing/list/loadBalancers" },
          { label: "Cloud DNS", to: "/ui/net-services/dns/zones", match: ["/ui/net-services/dns"] },
        ],
      },
    ],
  },
  {
    id: "logs",
    name: "Logging",
    icon: "article",
    category: "Operations",
    description: "Store, search and analyze log data",
    docs: "https://cloud.google.com/logging/docs",
    nav: [{ items: [{ label: "Logs Explorer", to: "/ui/logs/query" }] }],
  },
  {
    id: "bigquery",
    name: "BigQuery",
    icon: "query_stats",
    category: "Analytics",
    description: "Serverless data warehouse",
    docs: "https://cloud.google.com/bigquery/docs",
    nav: [{ items: [{ label: "Datasets", to: "/ui/bigquery", match: ["/ui/bigquery/datasets"] }] }],
  },
  {
    id: "cloudpubsub",
    name: "Pub/Sub",
    icon: "send",
    category: "Analytics",
    description: "Asynchronous messaging between services",
    docs: "https://cloud.google.com/pubsub/docs",
    nav: [
      {
        items: [
          { label: "Topics", to: "/ui/cloudpubsub/topic/list", match: ["/ui/cloudpubsub/topic"] },
          {
            label: "Subscriptions",
            to: "/ui/cloudpubsub/subscription/list",
            match: ["/ui/cloudpubsub/subscription"],
          },
        ],
      },
    ],
  },
  {
    id: "dataflow",
    name: "Dataflow",
    icon: "integration_instructions",
    category: "Analytics",
    description: "Stream and batch data processing",
    docs: "https://cloud.google.com/dataflow/docs",
    nav: [{ items: [{ label: "Jobs", to: "/ui/dataflow/jobs", match: ["/ui/dataflow/jobs"] }] }],
  },
  {
    id: "artifacts",
    name: "Artifact Registry",
    icon: "inventory_2",
    category: "CI/CD",
    description: "Store and manage container images and language packages",
    docs: "https://cloud.google.com/artifact-registry/docs",
    nav: [{ items: [{ label: "Repositories", to: "/ui/artifacts", match: ["/ui/artifacts"] }] }],
  },
  {
    id: "cloud-build",
    name: "Cloud Build",
    icon: "build",
    category: "CI/CD",
    description: "Build, test and deploy on serverless infrastructure",
    docs: "https://cloud.google.com/build/docs",
    nav: [{ items: [{ label: "History", to: "/ui/cloud-build/builds", match: ["/ui/cloud-build/builds"] }] }],
  },
  {
    id: "eventarc",
    name: "Eventarc",
    icon: "webhook",
    category: "Integration services",
    description: "Route events from Google Cloud sources to destinations",
    docs: "https://cloud.google.com/eventarc/docs",
    nav: [{ items: [{ label: "Triggers", to: "/ui/eventarc/triggers", match: ["/ui/eventarc/triggers"] }] }],
  },
  {
    id: "api-gateway",
    name: "API Gateway",
    icon: "api",
    category: "Integration services",
    description: "Create, secure and monitor APIs for serverless backends",
    docs: "https://cloud.google.com/api-gateway/docs",
    nav: [{ items: [{ label: "APIs", to: "/ui/api-gateway" }] }],
  },
  {
    id: "security",
    name: "Security",
    icon: "shield",
    category: "Security",
    description: "Secret Manager and Key Management",
    docs: "https://cloud.google.com/secret-manager/docs",
    nav: [
      {
        heading: "Data protection",
        items: [
          { label: "Secret Manager", to: "/ui/security/secret-manager", match: ["/ui/security/secret-manager"] },
          {
            label: "Key Management",
            to: "/ui/security/kms/keyrings",
            match: ["/ui/security/kms"],
          },
        ],
      },
    ],
  },
  {
    id: "iam-admin",
    name: "IAM & Admin",
    icon: "security",
    category: "Management",
    description: "Permissions, service accounts, roles and identity federation",
    docs: "https://cloud.google.com/iam/docs",
    nav: [
      {
        items: [
          { label: "IAM", to: "/ui/iam-admin/iam" },
          { label: "Manage resources", to: "/ui/cloud-resource-manager" },
          { label: "Workforce Identity Federation", to: "/ui/iam-admin/workforce-identity-pools" },
          {
            label: "Service accounts",
            to: "/ui/iam-admin/serviceaccounts",
            match: ["/ui/iam-admin/serviceaccounts"],
          },
          { label: "Roles", to: "/ui/iam-admin/roles" },
        ],
      },
    ],
  },
  {
    id: "apis",
    name: "APIs & Services",
    icon: "api",
    category: "Management",
    description: "Enable and manage the APIs a project uses",
    docs: "https://cloud.google.com/service-usage/docs",
    nav: [{ items: [{ label: "Enabled APIs & services", to: "/ui/apis/dashboard" }] }],
  },
];

/** The real console's default pins, among the products this console has. */
export const DEFAULT_PINNED = ["apis", "iam-admin", "compute", "storage", "bigquery", "networking", "run", "sql"];

export function productHome(product: ConsoleProduct): string {
  return product.nav[0].items[0].to;
}

function itemMatches(item: NavItem, pathname: string): boolean {
  return [item.to, ...(item.match ?? [])].some(
    (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
  );
}

/** The product whose pages the path belongs to, and the navigation item that is current. */
export function locate(pathname: string): { product?: ConsoleProduct; item?: NavItem } {
  let best: { product: ConsoleProduct; item: NavItem; length: number } | undefined;
  for (const product of PRODUCTS) {
    for (const section of product.nav) {
      for (const item of section.items) {
        if (!itemMatches(item, pathname)) continue;
        const length = Math.max(
          ...[item.to, ...(item.match ?? [])]
            .filter((prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`))
            .map((prefix) => prefix.length),
        );
        if (!best || length > best.length) best = { product, item, length };
      }
    }
  }
  return best ? { product: best.product, item: best.item } : {};
}

export function findProduct(id: string): ConsoleProduct | undefined {
  return PRODUCTS.find((product) => product.id === id);
}

export interface SearchEntry {
  label: string;
  detail: string;
  to: string;
  icon: IconName;
}

/** Products and their pages, as the search box offers them. */
export function searchEntries(): SearchEntry[] {
  const entries: SearchEntry[] = [];
  for (const product of PRODUCTS) {
    entries.push({ label: product.name, detail: product.description, to: productHome(product), icon: product.icon });
    for (const section of product.nav) {
      for (const item of section.items) {
        if (item.label === product.name) continue;
        entries.push({ label: item.label, detail: product.name, to: item.to, icon: product.icon });
      }
    }
  }
  return entries;
}

export function searchCatalog(query: string): SearchEntry[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [];
  const scored = searchEntries()
    .map((entry) => {
      const label = entry.label.toLowerCase();
      const detail = entry.detail.toLowerCase();
      const score = label.startsWith(needle)
        ? 0
        : label.split(/[^a-z0-9]+/).some((word) => word.startsWith(needle))
          ? 1
          : label.includes(needle)
            ? 2
            : detail.includes(needle)
              ? 3
              : -1;
      return { entry, score };
    })
    .filter(({ score }) => score >= 0);
  scored.sort((a, b) => a.score - b.score || a.entry.label.localeCompare(b.entry.label));
  return scored.map(({ entry }) => entry);
}
