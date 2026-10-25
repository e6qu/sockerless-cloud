import { Link } from "react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { CopyButton, GcpPageHeader } from "../console/GcpConsole.js";
import { Icon } from "../console/icons.js";
import { CATEGORIES, PRODUCTS, findProduct, productHome, type ConsoleProduct } from "../console/catalog.js";
import { PinButton } from "../console/GcpNavDrawer.js";
import { usePinnedProducts } from "../console/pins.js";
import { useProject, useProjectPath } from "../console/project.js";
import { LoadError, Loading } from "../console/GcpResourceDetail.js";
import { ResourceTableView, type GcpColumn } from "../console/GcpTable.js";
import { formatTimestamp, shortName } from "../console/format.js";
import {
  crmProjectNumber,
  fetchARRepos,
  fetchActivity,
  fetchBigQueryDatasets,
  fetchCloudFunctions,
  fetchCloudRunJobsReal,
  fetchCloudRunServices,
  fetchComputeInstances,
  fetchGCSBuckets,
  fetchProject,
  fetchPubSubTopics,
  fetchSecrets,
  fetchSqlInstances,
  type LogEntry,
} from "../api.js";

function useCurrentProject() {
  const { project } = useProject();
  return useQuery({ queryKey: ["crm-project", project], queryFn: () => fetchProject(project) });
}

function ProjectFacts() {
  const query = useCurrentProject();
  if (query.isError) return <LoadError error={query.error} noun="the project" />;
  if (query.isLoading || !query.data) return <Loading noun="project" />;
  const project = query.data;
  return (
    <dl className="gc-facts">
      <div>
        <dt>Project name</dt>
        <dd>{project.displayName || project.projectId}</dd>
      </div>
      <div>
        <dt>Project number</dt>
        <dd className="gc-copyable">
          {crmProjectNumber(project)}
          <CopyButton value={crmProjectNumber(project)} label="Copy project number" />
        </dd>
      </div>
      <div>
        <dt>Project ID</dt>
        <dd className="gc-copyable">
          {project.projectId}
          <CopyButton value={project.projectId} label="Copy project ID" />
        </dd>
      </div>
    </dl>
  );
}

function ProductTile({ product }: { product: ConsoleProduct }) {
  const path = useProjectPath();
  return (
    <Link className="gc-product-tile" to={path(productHome(product))}>
      <Icon name={product.icon} size="1.5em" />
      <span>{product.name}</span>
    </Link>
  );
}

export function WelcomePage() {
  const { pinned } = usePinnedProducts();
  const path = useProjectPath();
  const query = useCurrentProject();
  return (
    <div className="gc-page-narrow">
      <h1 className="gc-welcome-title">Welcome</h1>
      <section className="gc-card" aria-labelledby="welcome-working-in">
        <h2 id="welcome-working-in" className="gc-card-title">
          You're working in{" "}
          <strong>{query.data?.displayName || query.data?.projectId || "your project"}</strong>
        </h2>
        <ProjectFacts />
        <div className="gc-card-links">
          <Link to={path("/ui/home/dashboard")}>Dashboard</Link>
          <Link to={path("/ui/home/activity")}>Activity</Link>
          <Link to={path("/ui/cloud-resource-manager")}>Manage resources</Link>
        </div>
      </section>
      <section aria-labelledby="welcome-quick-access">
        <h2 id="welcome-quick-access" className="gc-section-title">Quick access</h2>
        <div className="gc-product-tiles">
          {pinned
            .map(findProduct)
            .filter((product): product is ConsoleProduct => Boolean(product))
            .map((product) => (
              <ProductTile key={product.id} product={product} />
            ))}
          <Link className="gc-product-tile gc-product-tile-all" to={path("/ui/products")}>
            <Icon name="apps" size="1.5em" />
            <span>View all products</span>
          </Link>
        </div>
      </section>
    </div>
  );
}

// The Resources card counts each product's resources with the same list call
// the product's own page makes.
const RESOURCES: {
  product: string;
  label?: string;
  noun: [string, string];
  path: string;
  queryKey: string;
  fetch: (project: string) => Promise<unknown[]>;
}[] = [
  { product: "compute", noun: ["VM instance", "VM instances"], path: "/ui/compute/instances", queryKey: "compute-instances", fetch: fetchComputeInstances },
  { product: "storage", noun: ["bucket", "buckets"], path: "/ui/storage/browser", queryKey: "gcs-buckets-real", fetch: fetchGCSBuckets },
  { product: "run", label: "Cloud Run services", noun: ["service", "services"], path: "/ui/run", queryKey: "cloudrun-services", fetch: fetchCloudRunServices },
  { product: "run", label: "Cloud Run jobs", noun: ["job", "jobs"], path: "/ui/run/jobs", queryKey: "cloudrun-jobs-real", fetch: fetchCloudRunJobsReal },
  { product: "functions", noun: ["function", "functions"], path: "/ui/functions/list", queryKey: "cloud-functions-real", fetch: fetchCloudFunctions },
  { product: "sql", noun: ["instance", "instances"], path: "/ui/sql/instances", queryKey: "sql-instances", fetch: fetchSqlInstances },
  { product: "bigquery", noun: ["dataset", "datasets"], path: "/ui/bigquery", queryKey: "bigquery-datasets", fetch: fetchBigQueryDatasets },
  { product: "cloudpubsub", noun: ["topic", "topics"], path: "/ui/cloudpubsub/topic/list", queryKey: "pubsub-topics", fetch: fetchPubSubTopics },
  { product: "artifacts", noun: ["repository", "repositories"], path: "/ui/artifacts", queryKey: "ar-repos-real", fetch: fetchARRepos },
  { product: "security", label: "Secret Manager", noun: ["secret", "secrets"], path: "/ui/security/secret-manager", queryKey: "secrets", fetch: fetchSecrets },
];

function ResourcesCard() {
  const { project } = useProject();
  const path = useProjectPath();
  const results = useQueries({
    queries: RESOURCES.map((resource) => ({
      queryKey: [resource.queryKey, project],
      queryFn: () => resource.fetch(project),
    })),
  });
  return (
    <section className="gc-card" aria-labelledby="dashboard-resources">
      <h2 id="dashboard-resources" className="gc-card-title">
        <Icon name="format_list_bulleted" size="1.25em" />
        Resources
      </h2>
      <ul className="gc-resource-list">
        {RESOURCES.map((resource, index) => {
          const product = findProduct(resource.product)!;
          const result = results[index];
          return (
            <li key={resource.path}>
              <Icon name={product.icon} size="1.25em" />
              <Link to={path(resource.path)}>{resource.label ?? product.name}</Link>
              <span className="gc-resource-count">
                {result.isError ? (
                  <span className="gc-status-error">Couldn't load {resource.noun[1]}</span>
                ) : result.isLoading ? (
                  "…"
                ) : (
                  `${result.data?.length ?? 0} ${resource.noun[result.data?.length === 1 ? 0 : 1]}`
                )}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const activityColumns: GcpColumn<LogEntry>[] = [
  {
    id: "time",
    header: "Time",
    cell: (row) => formatTimestamp(row.timestamp),
    value: (row) => row.timestamp,
  },
  {
    id: "activity",
    header: "Activity",
    cell: (row) => <code className="gc-code-inline">{row.protoPayload?.methodName ?? "—"}</code>,
    value: (row) => row.protoPayload?.methodName ?? "",
  },
  {
    id: "resource",
    header: "Resource",
    cell: (row) => row.protoPayload?.resourceName ?? "—",
    value: (row) => row.protoPayload?.resourceName ?? "",
  },
  {
    id: "principal",
    header: "Principal",
    cell: (row) => row.protoPayload?.authenticationInfo?.principalEmail ?? row.protoPayload?.authenticationInfo?.principalSubject ?? "—",
    value: (row) => row.protoPayload?.authenticationInfo?.principalEmail ?? row.protoPayload?.authenticationInfo?.principalSubject ?? "",
  },
  {
    id: "service",
    header: "Service",
    cell: (row) => row.protoPayload?.serviceName ?? shortName(row.logName),
    value: (row) => row.protoPayload?.serviceName ?? "",
    defaultHidden: true,
  },
];

function ActivityStream() {
  const { project } = useProject();
  const query = useQuery({ queryKey: ["activity", project], queryFn: () => fetchActivity(project) });
  return (
    <ResourceTableView<LogEntry>
      rows={query.data}
      isLoading={query.isLoading}
      error={query.error}
      columns={activityColumns}
      rowKey={(row) => row.insertId ?? `${row.timestamp}-${row.protoPayload?.methodName}`}
      resourceNoun="activity"
      tableId="activity-table"
      empty={{
        headline: "No activity",
        description: "Admin Activity audit log entries for this project appear here.",
      }}
    />
  );
}

export function DashboardPage({ tab }: { tab: "dashboard" | "activity" }) {
  const path = useProjectPath();
  return (
    <>
      <GcpPageHeader title={tab === "dashboard" ? "Dashboard" : "Activity"} />
      <nav className="gc-page-tabs" aria-label="Cloud overview">
        <Link to={path("/ui/home/dashboard")} aria-current={tab === "dashboard" ? "page" : undefined}>
          Dashboard
        </Link>
        <Link to={path("/ui/home/activity")} aria-current={tab === "activity" ? "page" : undefined}>
          Activity
        </Link>
      </nav>
      {tab === "dashboard" ? (
        <div className="gc-dashboard">
          <section className="gc-card" aria-labelledby="dashboard-project-info">
            <h2 id="dashboard-project-info" className="gc-card-title">
              <Icon name="info" size="1.25em" />
              Project info
            </h2>
            <ProjectFacts />
            <div className="gc-card-links">
              <Link to={path("/ui/cloud-resource-manager")}>Manage resources</Link>
              <Link to={path("/ui/iam-admin/iam")}>Add people to this project</Link>
            </div>
          </section>
          <ResourcesCard />
        </div>
      ) : (
        <ActivityStream />
      )}
    </>
  );
}

export function AllProductsPage() {
  const path = useProjectPath();
  return (
    <>
      <GcpPageHeader title="All products" />
      <div className="gc-all-products">
        {CATEGORIES.map((category) => {
          const products = PRODUCTS.filter((product) => product.category === category);
          if (products.length === 0) return null;
          return (
            <section key={category} aria-label={category}>
              <h2 className="gc-section-title">{category}</h2>
              <ul>
                {products.map((product) => (
                  <li key={product.id} className="gc-all-products-row">
                    <Icon name={product.icon} size="1.5em" />
                    <div>
                      <Link to={path(productHome(product))}>{product.name}</Link>
                      <p>{product.description}</p>
                    </div>
                    <PinButton product={product} />
                  </li>
                ))}
              </ul>
            </section>
          );
        })}
      </div>
    </>
  );
}
