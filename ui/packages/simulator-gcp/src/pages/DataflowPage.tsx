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
  CONSOLE_REGION,
  fetchDataflowJob,
  fetchDataflowJobMessages,
  fetchDataflowJobs,
  updateDataflowJobState,
  type DataflowJob,
  type DataflowJobMessage,
} from "../api.js";
import { useProject } from "../console/project.js";

// Dataflow reports state as the JobState enum ("JOB_STATE_RUNNING"); the real
// console shows the human form.
export function jobStateLabel(state: string | undefined): string {
  if (!state) return "Unknown";
  return state.replace(/^JOB_STATE_/, "").replace(/_/g, " ");
}

// A job is cancellable while it has not reached a terminal JobState.
const TERMINAL_STATES = new Set([
  "JOB_STATE_DONE",
  "JOB_STATE_FAILED",
  "JOB_STATE_CANCELLED",
  "JOB_STATE_UPDATED",
  "JOB_STATE_DRAINED",
]);

const columns: GcpColumn<DataflowJob>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/dataflow/${row.id}`}>
        {row.name ?? row.id}
      </Link>
    ),
    value: (row) => row.name ?? row.id,
  },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={jobStateLabel(row.currentState)} />,
    value: (row) => jobStateLabel(row.currentState),
  },
  {
    id: "type",
    header: "Type",
    cell: (row) => (row.type ?? "").replace(/^JOB_TYPE_/, "") || "—",
    value: (row) => row.type ?? "",
  },
  {
    id: "started",
    header: "Start time",
    cell: (row) => formatTimestamp(row.startTime ?? row.createTime ?? ""),
    value: (row) => row.startTime ?? row.createTime ?? "",
  },
  { id: "region", header: "Region", cell: (row) => row.location ?? "—", value: (row) => row.location ?? "" },
];

// CancelJobDialog runs the real projects.locations.jobs.update method with the
// requested terminal state — the same call `gcloud dataflow jobs cancel` and
// `… drain` make.
export function CancelJobDialog({
  id,
  requestedState,
  onClose,
  onDone,
}: {
  id: string;
  requestedState: "JOB_STATE_CANCELLED" | "JOB_STATE_DRAINED";
  onClose: () => void;
  onDone: () => void;
}) {
  const { project } = useProject();
  const draining = requestedState === "JOB_STATE_DRAINED";
  const run = useMutation({
    mutationFn: () => updateDataflowJobState(project, id, requestedState),
    onSuccess: onDone,
  });
  return (
    <GcpDialog
      title={draining ? "Drain job?" : "Cancel job?"}
      testId={draining ? "dataflow-drain-dialog" : "dataflow-cancel-dialog"}
      onClose={onClose}
    >
      <p>
        {draining ? "Draining" : "Cancelling"} <strong>{id}</strong>{" "}
        {draining
          ? "stops ingesting new data and finishes processing the work already buffered."
          : "stops the job immediately; buffered work is discarded."}
      </p>
      {run.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't update the job.</strong>{" "}
          {run.error instanceof Error ? run.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Close</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid={draining ? "dataflow-drain-confirm" : "dataflow-cancel-confirm"}
          disabled={run.isPending}
          onClick={() => run.mutate()}
        >
          {run.isPending ? "Working…" : draining ? "Drain" : "Cancel job"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DataflowPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<{ id: string; state: "JOB_STATE_CANCELLED" | "JOB_STATE_DRAINED" } | null>(
    null,
  );

  const columnsWithActions: GcpColumn<DataflowJob>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) =>
        TERMINAL_STATES.has(row.currentState ?? "") ? (
          <span className="gc-row-actions">—</span>
        ) : (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`dataflow-cancel-${row.id}`}
              aria-label={`Cancel ${row.name ?? row.id}`}
              onClick={() => setPending({ id: row.id, state: "JOB_STATE_CANCELLED" })}
            >
              Cancel
            </button>
            <button
              type="button"
              className="gc-button-text"
              data-testid={`dataflow-drain-${row.id}`}
              aria-label={`Drain ${row.name ?? row.id}`}
              onClick={() => setPending({ id: row.id, state: "JOB_STATE_DRAINED" })}
            >
              Drain
            </button>
          </span>
        ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<DataflowJob>
        title="Dataflow jobs"
        description={`Dataflow runs Apache Beam pipelines as fully managed batch and streaming jobs. Showing jobs in ${CONSOLE_REGION}.`}
        columns={columnsWithActions}
        queryKey={["dataflow-jobs", project]}
        queryFn={() => fetchDataflowJobs(project)}
        filterPlaceholder="Filter jobs"
        resourceNoun="jobs"
        empty={{
          headline: "Run a pipeline to see jobs here",
          description: "Dataflow jobs launched from a template or a Beam pipeline appear in this list.",
          primaryLabel: "Create job from template",
        }}
        rowKey={(row) => row.id}
      />
      {pending ? (
        <CancelJobDialog
          id={pending.id}
          requestedState={pending.state}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            void queryClient.invalidateQueries({ queryKey: ["dataflow-jobs", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function DataflowJobDetailPage() {
  const { id = "" } = useParams();
  const { project } = useProject();
  const job = useQuery({ queryKey: ["dataflow-job", project, id], queryFn: () => fetchDataflowJob(project, id) });
  const messages = useQuery({
    queryKey: ["dataflow-job-messages", project, id],
    queryFn: () => fetchDataflowJobMessages(project, id),
  });

  const data = job.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/dataflow">‹ Dataflow jobs</Link>
      </div>
      <GcpPageHeader
        title={data?.name ?? id}
        description="Dataflow job"
        onRefresh={() => {
          void job.refetch();
          void messages.refetch();
        }}
        refreshing={job.isFetching || messages.isFetching}
      />
      {job.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this job.</strong>{" "}
          {job.error instanceof Error ? job.error.message : "The simulator did not respond."}
        </div>
      ) : job.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading job…</div>
      ) : (
        <GcpTabs
          label="Job detail"
          tabs={[
            {
              id: "details",
              label: "Job info",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Job ID", value: data.id },
                    { label: "Status", value: <GcpStatus status={jobStateLabel(data.currentState)} /> },
                    { label: "Job type", value: (data.type ?? "").replace(/^JOB_TYPE_/, "") || "—" },
                    { label: "Region", value: data.location ?? "—" },
                    { label: "Created", value: formatTimestamp(data.createTime ?? "") },
                    { label: "Started", value: formatTimestamp(data.startTime ?? "") },
                    { label: "State last changed", value: formatTimestamp(data.currentStateTime ?? "") },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
            {
              id: "logs",
              label: "Job logs",
              content: (
                <SubResourceTable<DataflowJobMessage>
                  query={messages}
                  testId="dataflow-messages-table"
                  noun="job messages"
                  emptyHeadline="This job has no messages"
                  emptyDescription="Messages the Dataflow service emits for this job appear here."
                  rowKey={(row) => row.id ?? `${row.time}-${row.messageText}`}
                  columns={[
                    { header: "Time", cell: (row) => formatTimestamp(row.time ?? "") },
                    {
                      header: "Importance",
                      cell: (row) => (row.messageImportance ?? "").replace(/^JOB_MESSAGE_/, "") || "—",
                    },
                    { header: "Message", cell: (row) => row.messageText ?? "—" },
                  ]}
                />
              ),
            },
          ]}
        />
      )}
    </>
  );
}
