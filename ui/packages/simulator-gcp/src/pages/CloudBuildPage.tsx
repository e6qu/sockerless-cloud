import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  cancelCloudBuild,
  fetchCloudBuild,
  fetchCloudBuildTriggers,
  fetchCloudBuilds,
  retryCloudBuild,
  waitArOperation,
  type CloudBuild,
  type CloudBuildStep,
  type CloudBuildTrigger,
} from "../api.js";
import { useProject } from "../console/project.js";

// A build is cancellable only while it has not reached a terminal Status.
const TERMINAL_STATUSES = new Set(["SUCCESS", "FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED"]);

// The build's elapsed time, from the two RFC 3339 stamps the API returns.
export function buildDuration(build: CloudBuild): string {
  if (!build.startTime || !build.finishTime) return "—";
  const seconds = (new Date(build.finishTime).getTime() - new Date(build.startTime).getTime()) / 1000;
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const minutes = Math.floor(seconds / 60);
  return minutes > 0 ? `${minutes}m ${Math.round(seconds % 60)}s` : `${Math.round(seconds)}s`;
}

const columns: GcpColumn<CloudBuild>[] = [
  {
    id: "id",
    header: "Build",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/cloudbuild/${row.id}`}>
        {row.id}
      </Link>
    ),
    value: (row) => row.id,
  },
  {
    id: "status",
    header: "Status",
    cell: (row) => <GcpStatus status={row.status ?? "Unknown"} />,
    value: (row) => row.status ?? "",
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime ?? ""),
    value: (row) => row.createTime ?? "",
  },
  { id: "duration", header: "Duration", cell: buildDuration, value: buildDuration },
  {
    id: "steps",
    header: "Steps",
    cell: (row) => row.steps?.length ?? 0,
    value: (row) => String(row.steps?.length ?? 0),
  },
];

// BuildActionDialog runs the real projects.builds.cancel (synchronous, returns
// the Build) or projects.builds.retry (a long-running operation the console
// polls to done through the v1 operations.get poll).
export function BuildActionDialog({
  id,
  action,
  onClose,
  onDone,
}: {
  id: string;
  action: "cancel" | "retry";
  onClose: () => void;
  onDone: () => void;
}) {
  const { project } = useProject();
  const run = useMutation({
    mutationFn: async () =>
      action === "cancel" ? cancelCloudBuild(project, id) : waitArOperation(await retryCloudBuild(project, id)),
    onSuccess: onDone,
  });
  const cancelling = action === "cancel";
  return (
    <GcpDialog
      title={cancelling ? "Cancel build?" : "Retry build?"}
      testId={`cloudbuild-${action}-dialog`}
      onClose={onClose}
    >
      <p>
        {cancelling ? "Cancelling" : "Retrying"} <strong>{id}</strong>{" "}
        {cancelling
          ? "stops the build; steps that have not run are skipped."
          : "starts a new build from the same configuration and source."}
      </p>
      {run.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't {action} the build.</strong>{" "}
          {run.error instanceof Error ? run.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Close</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid={`cloudbuild-${action}-confirm`}
          disabled={run.isPending}
          onClick={() => run.mutate()}
        >
          {run.isPending ? "Working…" : cancelling ? "Cancel build" : "Retry"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudBuildPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<{ id: string; action: "cancel" | "retry" } | null>(null);
  const triggers = useQuery({
    queryKey: ["cloudbuild-triggers", project],
    queryFn: () => fetchCloudBuildTriggers(project),
  });

  const columnsWithActions: GcpColumn<CloudBuild>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => (
        <span className="gc-row-actions">
          {TERMINAL_STATUSES.has(row.status ?? "") ? (
            <button
              type="button"
              className="gc-button-text"
              data-testid={`cloudbuild-retry-${row.id}`}
              aria-label={`Retry ${row.id}`}
              onClick={() => setPending({ id: row.id, action: "retry" })}
            >
              Retry
            </button>
          ) : (
            <button
              type="button"
              className="gc-button-text"
              data-testid={`cloudbuild-cancel-${row.id}`}
              aria-label={`Cancel ${row.id}`}
              onClick={() => setPending({ id: row.id, action: "cancel" })}
            >
              Cancel
            </button>
          )}
        </span>
      ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<CloudBuild>
        title="Cloud Build history"
        description="Cloud Build executes your builds on Google Cloud, running each step in its own container."
        columns={columnsWithActions}
        queryKey={["cloudbuild-builds", project]}
        queryFn={() => fetchCloudBuilds(project)}
        filterPlaceholder="Filter builds"
        resourceNoun="builds"
        empty={{
          headline: "Run a build to see its history here",
          description: "Builds submitted by a trigger, gcloud or the API appear in this list.",
          primaryLabel: "Run a build",
        }}
        rowKey={(row) => row.id}
      />
      <h2 className="gc-detail-heading">Triggers</h2>
      <SubResourceTable<CloudBuildTrigger>
        query={triggers}
        testId="cloudbuild-triggers-table"
        noun="triggers"
        emptyHeadline="This project has no build triggers"
        emptyDescription="A trigger starts a build when your source repository changes."
        rowKey={(row) => row.id}
        columns={[
          { header: "Name", cell: (row) => row.name ?? row.id },
          { header: "Build configuration", cell: (row) => row.filename ?? "—" },
          { header: "Repository", cell: (row) => row.triggerTemplate?.repoName ?? "—" },
          {
            header: "Event",
            cell: (row) =>
              row.triggerTemplate?.branchName
                ? `Push to branch ${row.triggerTemplate.branchName}`
                : row.triggerTemplate?.tagName
                  ? `Push to tag ${row.triggerTemplate.tagName}`
                  : "—",
          },
          { header: "Status", cell: (row) => <GcpStatus status={row.disabled ? "Disabled" : "Enabled"} /> },
        ]}
      />
      {pending ? (
        <BuildActionDialog
          id={pending.id}
          action={pending.action}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            void queryClient.invalidateQueries({ queryKey: ["cloudbuild-builds", project] });
          }}
        />
      ) : null}
    </>
  );
}

// BuildStepsTable renders the steps the loaded Build carries. Unlike the
// sub-resources other detail pages read with their own list call, a build's
// steps arrive inside the Build itself, so this is presentational rather than
// query-backed.
export function BuildStepsTable({ steps }: { steps: CloudBuildStep[] }) {
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="cloudbuild-steps-table">
        <thead>
          <tr>
            <th>Step</th>
            <th>Image</th>
            <th>Arguments</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {steps.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">This build has no steps</p>
                  <p className="gc-empty-description">The steps the build configuration declares appear here.</p>
                </div>
              </td>
            </tr>
          ) : (
            steps.map((step, index) => (
              <tr key={step.id ?? `${step.name}-${index}`}>
                <td>{step.id ?? `Step ${index + 1}`}</td>
                <td>{step.name ?? "—"}</td>
                <td>{(step.args ?? []).join(" ") || "—"}</td>
                <td>
                  <GcpStatus status={step.status ?? "Unknown"} />
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

export function CloudBuildDetailPage() {
  const { id = "" } = useParams();
  const { project } = useProject();
  const build = useQuery({ queryKey: ["cloudbuild-build", project, id], queryFn: () => fetchCloudBuild(project, id) });
  const data = build.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/cloudbuild">‹ Cloud Build history</Link>
      </div>
      <GcpPageHeader
        title={id}
        description="Cloud Build build"
        onRefresh={() => void build.refetch()}
        refreshing={build.isFetching}
      />
      {build.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this build.</strong>{" "}
          {build.error instanceof Error ? build.error.message : "The simulator did not respond."}
        </div>
      ) : build.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading build…</div>
      ) : (
        <GcpTabs
          label="Build detail"
          tabs={[
            { id: "steps", label: "Build steps", content: <BuildStepsTable steps={data.steps ?? []} /> },
            {
              id: "details",
              label: "Build info",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Status", value: <GcpStatus status={data.status ?? "Unknown"} /> },
                    { label: "Status detail", value: data.statusDetail || "—" },
                    { label: "Created", value: formatTimestamp(data.createTime ?? "") },
                    { label: "Started", value: formatTimestamp(data.startTime ?? "") },
                    { label: "Finished", value: formatTimestamp(data.finishTime ?? "") },
                    { label: "Duration", value: buildDuration(data) },
                    { label: "Images", value: (data.images ?? []).join(", ") || "—" },
                    { label: "Tags", value: (data.tags ?? []).join(", ") || "—" },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
          ]}
        />
      )}
    </>
  );
}
