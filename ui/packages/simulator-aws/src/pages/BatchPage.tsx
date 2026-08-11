import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  fetchBatchComputeEnvironments,
  fetchBatchJobDefinitions,
  fetchBatchJobQueues,
  fetchBatchJobs,
  terminateBatchJob,
  type BatchComputeEnvironment,
  type BatchJob,
  type BatchJobDefinition,
  type BatchJobQueue,
} from "../api.js";

const jobColumns: AwsColumn<BatchJob>[] = [
  { id: "name", header: "Job name", cell: (row) => row.jobName, value: (row) => row.jobName },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "queue", header: "Job queue", cell: (row) => row.jobQueue, value: (row) => row.jobQueue },
  {
    id: "definition",
    header: "Job definition",
    cell: (row) => row.jobDefinition,
    value: (row) => row.jobDefinition,
  },
  { id: "created", header: "Created", cell: (row) => formatEpoch(row.createdAt), value: (row) => String(row.createdAt) },
  {
    id: "runtime",
    header: "Started / stopped",
    cell: (row) => `${formatEpoch(row.startedAt)} / ${formatEpoch(row.stoppedAt)}`,
    value: (row) => `${row.startedAt} ${row.stoppedAt}`,
  },
  {
    id: "result",
    header: "Exit code / status reason",
    cell: (row) => `${row.exitCode ?? "–"}${row.statusReason ? ` — ${row.statusReason}` : ""}`,
    value: (row) => `${row.exitCode ?? ""} ${row.statusReason}`,
  },
];

const queueColumns: AwsColumn<BatchJobQueue>[] = [
  { id: "name", header: "Name", cell: (row) => row.jobQueueName, value: (row) => row.jobQueueName },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "priority", header: "Priority", cell: (row) => String(row.priority), value: (row) => String(row.priority) },
  {
    id: "computeEnvironments",
    header: "Compute environments",
    cell: (row) => row.computeEnvironments.join(", ") || "–",
    value: (row) => row.computeEnvironments.join(", "),
  },
];

const definitionColumns: AwsColumn<BatchJobDefinition>[] = [
  { id: "name", header: "Name", cell: (row) => row.jobDefinitionName, value: (row) => row.jobDefinitionName },
  { id: "revision", header: "Revision", cell: (row) => String(row.revision), value: (row) => String(row.revision) },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "image", header: "Container image", cell: (row) => row.image || "–", value: (row) => row.image },
  {
    id: "resources",
    header: "Resources",
    cell: (row) => `${row.vcpus || "–"} vCPU / ${row.memory || "–"} MiB`,
    value: (row) => `${row.vcpus} ${row.memory}`,
  },
];

const environmentColumns: AwsColumn<BatchComputeEnvironment>[] = [
  { id: "name", header: "Name", cell: (row) => row.computeEnvironmentName, value: (row) => row.computeEnvironmentName },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "serviceRole", header: "Service role", cell: (row) => row.serviceRole || "–", value: (row) => row.serviceRole },
];

function TerminateJobsModal({ jobs, onClose, clearSelection }: {
  jobs: BatchJob[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const terminate = useMutation({
    mutationFn: async () => {
      for (const job of jobs) {
        await terminateBatchJob(job.jobId, "Terminated from the AWS Batch console");
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["batch-jobs"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={jobs.length === 1 ? `Terminate ${jobs[0].jobName}?` : `Terminate ${jobs.length} jobs?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton variant="primary" disabled={terminate.isPending} onClick={() => terminate.mutate()}>
            {terminate.isPending ? "Terminating…" : "Terminate"}
          </AwsButton>
        </>
      }
    >
      <p>AWS Batch stops each selected job’s running container and records the supplied termination reason.</p>
      {terminate.isError && (
        <AwsErrorAlert>{terminate.error instanceof Error ? terminate.error.message : "Termination failed."}</AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function BatchPage() {
  const [terminating, setTerminating] = useState<{ jobs: BatchJob[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<BatchJob>
        title="Jobs"
        description="AWS Batch jobs across every job queue and lifecycle state in this account and Region."
        columns={jobColumns}
        queryKey={["batch-jobs"]}
        queryFn={fetchBatchJobs}
        refreshInterval={2_000}
        filterPlaceholder="Find jobs"
        emptyTitle="No jobs"
        emptyDescription="No AWS Batch jobs have been submitted in this account and Region."
        rowKey={(row) => row.jobId}
        tableTestId="batch-jobs-table"
        errorTestId="batch-jobs-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              disabled={selected.length === 0 || selected.every((job) => job.status === "SUCCEEDED" || job.status === "FAILED")}
              onClick={() => setTerminating({ jobs: selected, clearSelection })}
            >
              Terminate
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<BatchJobQueue>
        title="Job queues"
        headingVariant="h2"
        description="AWS Batch job queues in this account and Region."
        columns={queueColumns}
        queryKey={["batch-job-queues"]}
        queryFn={fetchBatchJobQueues}
        filterPlaceholder="Find job queues"
        emptyTitle="No job queues"
        emptyDescription="No AWS Batch job queues exist in this account and Region."
        rowKey={(row) => row.jobQueueArn || row.jobQueueName}
        tableTestId="batch-job-queues-table"
        errorTestId="batch-job-queues-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
        )}
      />
      <AwsResourceTable<BatchJobDefinition>
        title="Job definitions"
        headingVariant="h2"
        description="Registered AWS Batch job definitions and their container resources."
        columns={definitionColumns}
        queryKey={["batch-job-definitions"]}
        queryFn={fetchBatchJobDefinitions}
        filterPlaceholder="Find job definitions"
        emptyTitle="No job definitions"
        emptyDescription="No AWS Batch job definitions exist in this account and Region."
        rowKey={(row) => row.jobDefinitionArn}
        tableTestId="batch-job-definitions-table"
        errorTestId="batch-job-definitions-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
        )}
      />
      <AwsResourceTable<BatchComputeEnvironment>
        title="Compute environments"
        headingVariant="h2"
        description="The compute environments job queues dispatch to."
        columns={environmentColumns}
        queryKey={["batch-compute-environments"]}
        queryFn={fetchBatchComputeEnvironments}
        filterPlaceholder="Find compute environments"
        emptyTitle="No compute environments"
        emptyDescription="No AWS Batch compute environments exist in this account and Region."
        rowKey={(row) => row.computeEnvironmentArn || row.computeEnvironmentName}
        tableTestId="batch-compute-environments-table"
        errorTestId="batch-compute-environments-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
        )}
      />
      {terminating && (
        <TerminateJobsModal
          jobs={terminating.jobs}
          clearSelection={terminating.clearSelection}
          onClose={() => setTerminating(null)}
        />
      )}
    </>
  );
}
