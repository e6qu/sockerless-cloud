import type { IconName } from "./icons.js";

// The real console's hamburger menu opens a navigation drawer listing the
// Google Cloud product catalog, grouped the way the console groups it
// (Compute, Serverless, Storage, Databases, Networking, Operations,
// Analytics, Artificial Intelligence, Developer Tools, API Management,
// APIs & Services, IAM & Admin, Security). The simulator implements a
// substantial slice of Google Cloud, so the catalog carries the real
// information architecture truthfully: every product Google Cloud offers is
// listed, every product whose real API the simulator serves links to its
// page, and only the products with no simulated API at all are marked
// "Not supported".
//
// The "Not supported" chip is a claim about the simulator, so it is only
// correct where the simulator serves no API for the product. A product whose
// real REST surface the simulator answers — Compute Engine, Cloud SQL,
// Spanner, Bigtable, Firestore, Memorystore for Redis, Cloud DNS, Cloud Load
// Balancing, Serverless VPC Access, BigQuery, Pub/Sub, Dataflow, Cloud Build,
// Eventarc, API Gateway, Service Usage, Cloud KMS, Secret Manager and Cloud
// IAM — links to its own page instead.
//
// Icons are Material Symbols Outlined glyphs (the console's own icon set),
// not the trademarked per-product logos the real catalog uses — see
// PLAN.md § "Simulator Console Parity" ("without copying proprietary
// assets").

export interface CatalogItem {
  /** Route slug for the "not supported" page; also used as the React key. */
  id: string;
  /** The product's real Google Cloud name. */
  name: string;
  icon: IconName;
  /** Present, and `to` set, only for products the simulator implements. */
  to?: string;
}

export interface CatalogGroup {
  label: string;
  items: CatalogItem[];
}

export const CATALOG: CatalogGroup[] = [
  {
    label: "Compute",
    items: [
      { id: "compute-engine", name: "Compute Engine", icon: "computer", to: "/ui/compute" },
      { id: "kubernetes-engine", name: "Kubernetes Engine", icon: "hub" },
      { id: "cloud-run", name: "Cloud Run", icon: "deployed_code", to: "/ui/cloudrun" },
      { id: "app-engine", name: "App Engine", icon: "package_2" },
    ],
  },
  {
    label: "Serverless",
    items: [
      { id: "cloud-run-functions", name: "Cloud Run functions", icon: "function", to: "/ui/functions" },
      { id: "workflows", name: "Workflows", icon: "list_alt" },
      { id: "eventarc", name: "Eventarc", icon: "notifications", to: "/ui/eventarc" },
    ],
  },
  {
    label: "Storage",
    items: [
      { id: "cloud-storage", name: "Cloud Storage", icon: "database", to: "/ui/gcs" },
      { id: "filestore", name: "Filestore", icon: "inventory_2" },
    ],
  },
  {
    label: "Databases",
    items: [
      { id: "cloud-sql", name: "Cloud SQL", icon: "database", to: "/ui/sql" },
      { id: "firestore", name: "Firestore", icon: "database", to: "/ui/firestore" },
      { id: "spanner", name: "Spanner", icon: "database", to: "/ui/spanner" },
      { id: "bigtable", name: "Bigtable", icon: "database", to: "/ui/bigtable" },
      { id: "memorystore", name: "Memorystore for Redis", icon: "database", to: "/ui/memorystore" },
    ],
  },
  {
    label: "Networking",
    items: [
      { id: "vpc-network", name: "VPC network", icon: "lan", to: "/ui/vpc" },
      { id: "cloud-load-balancing", name: "Cloud Load Balancing", icon: "lan", to: "/ui/loadbalancing" },
      { id: "cloud-dns", name: "Cloud DNS", icon: "lan", to: "/ui/dns" },
      { id: "serverless-vpc-access", name: "Serverless VPC Access", icon: "lan", to: "/ui/vpcaccess" },
    ],
  },
  {
    label: "Operations",
    items: [
      { id: "logging", name: "Logging", icon: "monitoring", to: "/ui/logging" },
      { id: "monitoring", name: "Monitoring", icon: "query_stats" },
      { id: "error-reporting", name: "Error Reporting", icon: "help" },
    ],
  },
  {
    label: "Analytics",
    items: [
      { id: "bigquery", name: "BigQuery", icon: "query_stats", to: "/ui/bigquery" },
      { id: "pub-sub", name: "Pub/Sub", icon: "notifications", to: "/ui/pubsub" },
      { id: "dataflow", name: "Dataflow", icon: "integration_instructions", to: "/ui/dataflow" },
    ],
  },
  {
    label: "Artificial Intelligence",
    items: [{ id: "vertex-ai", name: "Vertex AI", icon: "smart_toy" }],
  },
  {
    label: "Developer Tools",
    items: [
      { id: "artifact-registry", name: "Artifact Registry", icon: "inventory_2", to: "/ui/ar" },
      { id: "cloud-build", name: "Cloud Build", icon: "integration_instructions", to: "/ui/cloudbuild" },
      { id: "cloud-source-repositories", name: "Cloud Source Repositories", icon: "list_alt" },
    ],
  },
  {
    label: "API Management",
    items: [{ id: "api-gateway", name: "API Gateway", icon: "lan", to: "/ui/apigateway" }],
  },
  {
    label: "APIs & Services",
    items: [{ id: "enabled-apis", name: "Enabled APIs & services", icon: "list_alt", to: "/ui/apis" }],
  },
  {
    label: "IAM & Admin",
    items: [
      { id: "service-accounts", name: "Service accounts", icon: "manage_accounts", to: "/ui/serviceaccounts" },
      { id: "manage-resources", name: "Manage resources", icon: "folder_data", to: "/ui/projects" },
      { id: "iam", name: "IAM", icon: "token", to: "/ui/iam" },
      { id: "organization-policies", name: "Organization policies", icon: "security" },
    ],
  },
  {
    label: "Security",
    items: [
      { id: "security-command-center", name: "Security Command Center", icon: "security" },
      { id: "cloud-kms", name: "Cloud KMS", icon: "token", to: "/ui/kms" },
      { id: "secret-manager", name: "Secret Manager", icon: "token", to: "/ui/secrets" },
    ],
  },
];

export function findCatalogItem(id: string | undefined): CatalogItem | undefined {
  if (!id) return undefined;
  for (const group of CATALOG) {
    const item = group.items.find((candidate) => candidate.id === id);
    if (item) return item;
  }
  return undefined;
}
