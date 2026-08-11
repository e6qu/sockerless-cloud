import { Link } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import {
  fetchCloudRunJobsReal,
  fetchCloudFunctions,
  fetchARRepos,
  fetchGCSBuckets,
  fetchServiceAccounts,
  fetchLogEntries,
} from "../api.js";
import { useProject } from "../console/project.js";

// The overview counts each resource from its real cloud API — the same list
// the resource page reads — rather than a sockerless-invented summary endpoint.
const RESOURCES = [
  { label: "Cloud Run jobs", to: "/ui/cloudrun", queryKey: ["cloudrun-jobs-real"], queryFn: fetchCloudRunJobsReal },
  { label: "Cloud Run functions", to: "/ui/functions", queryKey: ["cloud-functions-real"], queryFn: fetchCloudFunctions },
  { label: "Artifact Registry repositories", to: "/ui/ar", queryKey: ["ar-repos-real"], queryFn: fetchARRepos },
  { label: "Cloud Storage buckets", to: "/ui/gcs", queryKey: ["gcs-buckets-real"], queryFn: fetchGCSBuckets },
  { label: "Service accounts", to: "/ui/serviceaccounts", queryKey: ["iam-service-accounts"], queryFn: fetchServiceAccounts },
  { label: "Log entries", to: "/ui/logging", queryKey: ["log-entries-real"], queryFn: fetchLogEntries },
] as const;

export function OverviewPage() {
  const { project } = useProject();
  const results = useQueries({
    queries: RESOURCES.map((resource) => ({
      queryKey: [...resource.queryKey, project],
      queryFn: () => resource.queryFn(project),
    })),
  });

  const failed = results.some((result) => result.isError);
  const loading = results.some((result) => result.isLoading);
  const refetching = results.some((result) => result.isFetching);

  return (
    <>
      <GcpPageHeader
        title="Overview"
        description="Resources across this project."
        onRefresh={() => results.forEach((result) => void result.refetch())}
        refreshing={refetching}
      />
      <div style={{ marginBottom: 16 }}>
        {/* A failed read must not render an all-zeros board that reads as
         * healthy; status reflects whether the real APIs answered. */}
        {failed ? (
          <GcpStatus status="Unavailable" kind="error" />
        ) : loading ? (
          <GcpStatus status="Loading" kind="warning" />
        ) : (
          <GcpStatus status="All services responding" />
        )}
      </div>
      {failed ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load the project overview.</strong>{" "}
          {String(results.find((result) => result.error)?.error)}
        </div>
      ) : (
        <div className="gc-cards">
          {RESOURCES.map((resource, index) => (
            <div className="gc-card" key={resource.label}>
              <h2>{resource.label}</h2>
              <Link to={resource.to}>{loading ? "—" : (results[index].data?.length ?? 0)}</Link>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
