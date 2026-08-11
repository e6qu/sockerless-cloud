import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { fetchCloudRunJob, fetchCloudRunJobExecutions, type CloudRunExecution, type CloudRunJob } from "../api.js";
import { DeleteJobDialog, EditJobDialog, RunJobDialog } from "./CloudRunJobsPage.js";
import { useProject } from "../console/project.js";

// jobState reads the Job resource's own terminal condition rather than
// inventing a status field — the same derivation the jobs list uses.
function jobState(job: CloudRunJob): string {
  const condition = job.terminalCondition;
  if (!condition) return job.reconciling ? "Reconciling" : "Unknown";
  if (condition.state === "CONDITION_SUCCEEDED") return "Ready";
  if (condition.state === "CONDITION_FAILED") return condition.message ? `Failed: ${condition.message}` : "Failed";
  return condition.state ?? "Unknown";
}

// executionState reads an Execution's own conditions the way the real
// console's Executions tab does — the "Completed" condition's state, since
// Cloud Run Executions don't carry a single terminalCondition field the way
// Jobs do. Falls back to the task counts when no condition has landed yet
// (e.g. an execution the sim just created).
export function executionState(execution: CloudRunExecution): string {
  const completed = execution.conditions?.find((condition) => condition.type === "Completed");
  if (completed) {
    if (completed.state === "CONDITION_SUCCEEDED") return "Succeeded";
    if (completed.state === "CONDITION_FAILED") return completed.message ? `Failed: ${completed.message}` : "Failed";
    return completed.state ?? "Unknown";
  }
  if ((execution.runningCount ?? 0) > 0) return "Running";
  if ((execution.cancelledCount ?? 0) > 0) return "Cancelled";
  if ((execution.failedCount ?? 0) > 0) return "Failed";
  if ((execution.succeededCount ?? 0) > 0 && execution.succeededCount === execution.taskCount) return "Succeeded";
  return execution.completionTime ? "Succeeded" : "Running";
}

// ExecutionsTable is the Executions tab's table, presentational so the
// row-derivation logic is testable apart from the queries that feed it.
export function ExecutionsTable({ executions }: { executions: CloudRunExecution[] }) {
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="cloudrun-executions-table">
        <thead>
          <tr>
            <th>Execution</th>
            <th>Status</th>
            <th>Tasks</th>
            <th>Created</th>
            <th>Completed</th>
          </tr>
        </thead>
        <tbody>
          {executions.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={5}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">No executions yet</p>
                  <p className="gc-empty-description">Runs of this job appear here.</p>
                </div>
              </td>
            </tr>
          ) : (
            executions.map((execution) => (
              <tr key={execution.name}>
                <td>{shortName(execution.name)}</td>
                <td><GcpStatus status={executionState(execution)} /></td>
                <td>
                  {execution.succeededCount ?? 0}/{execution.taskCount ?? 0} succeeded
                  {execution.failedCount ? `, ${execution.failedCount} failed` : ""}
                </td>
                <td>{formatTimestamp(execution.createTime ?? "")}</td>
                <td>{execution.completionTime ? formatTimestamp(execution.completionTime) : "—"}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

export function CloudRunJobDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [running, setRunning] = useState(false);
  const job = useQuery({ queryKey: ["cloudrun-job", project, name], queryFn: () => fetchCloudRunJob(project, name) });
  const executions = useQuery({
    queryKey: ["cloudrun-job-executions", project, name],
    queryFn: () => fetchCloudRunJobExecutions(project, name),
    enabled: Boolean(job.data),
  });

  // Run and Delete address the job by the id in the route, not the loaded
  // resource, so they are available (and testable) even before the read
  // settles — the same way the list page's "Create job" action doesn't wait
  // on a successful list read. Edit prefills the form from the loaded resource,
  // so it appears once the read succeeds.
  const runDeleteActions = [
    { label: "Run", icon: "play_arrow" as const, testId: "cloudrun-job-run", onSelect: () => setRunning(true) },
    { label: "Delete", testId: "cloudrun-job-delete", onSelect: () => setDeleting(true) },
  ];
  const headerActions = job.data
    ? [{ label: "Edit", icon: "edit" as const, testId: "cloudrun-job-edit", onSelect: () => setEditing(true) }, ...runDeleteActions]
    : runDeleteActions;

  if (job.isError) {
    return (
      <>
        <GcpPageHeader title={name} description="Cloud Run job" actions={runDeleteActions} />
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this job.</strong>{" "}
          {job.error instanceof Error ? job.error.message : "The simulator did not respond."}
        </div>
        {deleting ? (
          <DeleteJobDialog project={project} jobId={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/cloudrun")} />
        ) : null}
        {running ? (
          <RunJobDialog
            project={project}
            jobId={name}
            onClose={() => setRunning(false)}
            onRan={() => setRunning(false)}
          />
        ) : null}
      </>
    );
  }

  const data = job.data;
  const status = data ? jobState(data) : "Unknown";

  const properties: { label: string; value: React.ReactNode }[] = data
    ? [
        { label: "Status", value: <GcpStatus status={status} /> },
        { label: "Unique ID", value: data.uid ?? "—" },
        { label: "Executions", value: String(data.executionCount ?? 0) },
        { label: "Launch stage", value: data.launchStage ?? "—" },
        { label: "Created", value: formatTimestamp(data.createTime ?? "") },
        { label: "Updated", value: formatTimestamp(data.updateTime ?? "") },
        { label: "Reconciling", value: data.reconciling ? "Yes" : "No" },
      ]
    : [];
  const labels = Object.entries(data?.labels ?? {});

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/cloudrun">‹ Cloud Run jobs</Link>
      </div>
      <GcpPageHeader
        title={shortName(name)}
        description="Cloud Run job"
        actions={headerActions}
        onRefresh={() => { void job.refetch(); void executions.refetch(); }}
        refreshing={job.isFetching || executions.isFetching}
      />

      {job.isLoading ? (
        <div className="gc-loading" role="status">Loading job…</div>
      ) : (
        <GcpTabs
          label="Job detail"
          tabs={[
            {
              id: "details",
              label: "Details",
              content: (
                <>
                  <dl className="gc-detail-grid">
                    {properties.map((property) => (
                      <div className="gc-detail-pair" key={property.label}>
                        <dt>{property.label}</dt>
                        <dd>{property.value}</dd>
                      </div>
                    ))}
                  </dl>
                  {labels.length > 0 ? (
                    <>
                      <h2 className="gc-detail-heading">Labels</h2>
                      <dl className="gc-detail-grid">
                        {labels.map(([key, value]) => (
                          <div className="gc-detail-pair" key={key}>
                            <dt>{key}</dt>
                            <dd>{value}</dd>
                          </div>
                        ))}
                      </dl>
                    </>
                  ) : null}
                </>
              ),
            },
            {
              id: "executions",
              label: "Executions",
              content: executions.isError ? (
                <div className="gc-message gc-message-error" role="alert">
                  <strong>Couldn't load executions.</strong>{" "}
                  {executions.error instanceof Error ? executions.error.message : "The simulator did not respond."}
                </div>
              ) : executions.isLoading ? (
                <div className="gc-loading" role="status">Loading executions…</div>
              ) : (
                <ExecutionsTable executions={executions.data ?? []} />
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteJobDialog project={project} jobId={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/cloudrun")} />
      ) : null}
      {running ? (
        <RunJobDialog
          project={project}
          jobId={name}
          onClose={() => setRunning(false)}
          onRan={() => {
            setRunning(false);
            void job.refetch();
            void executions.refetch();
          }}
        />
      ) : null}
      {editing && data ? (
        <EditJobDialog
          project={project}
          job={data}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void job.refetch();
          }}
        />
      ) : null}
    </>
  );
}
