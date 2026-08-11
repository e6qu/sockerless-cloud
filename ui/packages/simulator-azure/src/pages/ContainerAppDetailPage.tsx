import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Table, TableHeader, TableRow, TableHeaderCell, TableBody, TableCell, Text } from "@fluentui/react-components";
import {
  AzureCommandBar,
  AzureConfirmDialog,
  AzureEssentials,
  AzureStatus,
  AzureErrorMessage,
  AzureEmptyState,
  TagsEditor,
} from "../portal/index.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import { resourceGroupOf, locationLabel, tagsSummary } from "../portal/format.js";
import {
  API_VERSION,
  deleteContainerAppJob,
  fetchContainerAppJob,
  fetchContainerAppJobExecutions,
  startContainerAppJobExecution,
  stopContainerAppJobExecutions,
  updateContainerAppJob,
  updateResourceTags,
  type ContainerAppJobConfigInput,
} from "../api.js";
import { ContainerAppJobEditForm } from "./ContainerAppJobForms.js";

// The Container App jobs blade this console lists (ContainerAppsPage) reads the
// real Microsoft.App/jobs resource — Container Apps Jobs' run-to-completion
// model, the one sockerless deploys container tasks onto — so this detail
// blade stays on that same resource: its real Essentials, its containers
// (from the job's own template), and its run history (the real executions
// list), all read and driven through real Azure Resource Manager calls.
export function ContainerAppDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [panel, setPanel] = useState<"config" | "tags" | null>(null);

  const job = useQuery({ queryKey: ["ca-job", name], queryFn: () => fetchContainerAppJob(name) });
  const jobId = job.data?.id;
  const executions = useQuery({
    queryKey: ["ca-job-executions", jobId],
    queryFn: () => fetchContainerAppJobExecutions(jobId!),
    enabled: Boolean(jobId),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["ca-job-executions", jobId] });
  };
  const start = useMutation({
    mutationFn: () => startContainerAppJobExecution(jobId!),
    onSuccess: invalidate,
  });
  const stop = useMutation({
    mutationFn: () => stopContainerAppJobExecutions(jobId!),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: () => deleteContainerAppJob(jobId!),
    onSuccess: () => navigate("/ui/container-apps"),
  });
  const invalidateJob = () => {
    void queryClient.invalidateQueries({ queryKey: ["ca-job", name] });
    void queryClient.invalidateQueries({ queryKey: ["ca-jobs"] });
  };
  const editJob = useMutation({
    mutationFn: (config: ContainerAppJobConfigInput) =>
      updateContainerAppJob(job.data!.id, job.data!.location, job.data!.environmentId, config),
    onSuccess: () => {
      setPanel(null);
      invalidateJob();
    },
  });
  const saveTags = useMutation({
    mutationFn: (tags: Record<string, string>) => updateResourceTags(job.data!.id, API_VERSION.jobs, tags),
    onSuccess: () => {
      setPanel(null);
      invalidateJob();
    },
  });

  const mutationError = start.error ?? stop.error;

  return (
    <>
      <AzureCommandBar
        commands={[
          {
            label: "Run now",
            icon: "play",
            testid: "ca-job-run",
            disabled: !jobId || start.isPending,
            onSelect: () => start.mutate(),
          },
          {
            label: "Stop all executions",
            icon: "stop",
            testid: "ca-job-stop",
            disabled: !jobId || stop.isPending,
            onSelect: () => stop.mutate(),
          },
          {
            label: "Edit",
            icon: "edit",
            testid: "ca-job-edit",
            disabled: !jobId || editJob.isPending,
            onSelect: () => setPanel((current) => (current === "config" ? null : "config")),
          },
          {
            label: "Edit tags",
            icon: "tag",
            testid: "ca-job-tags",
            disabled: !jobId || saveTags.isPending,
            onSelect: () => setPanel((current) => (current === "tags" ? null : "tags")),
          },
          {
            label: "Refresh",
            icon: "refresh",
            onSelect: () => {
              void job.refetch();
              void executions.refetch();
            },
            disabled: job.isFetching,
          },
          {
            label: "Delete",
            icon: "delete",
            testid: "ca-job-delete",
            disabled: !jobId || remove.isPending,
            onSelect: () => setDeleting(true),
          },
        ]}
      />
      {job.data ? (
        <AzureConfirmDialog
          open={deleting}
          title={`Delete ${job.data.name}?`}
          busy={remove.isPending}
          testid="ca-job-delete-dialog"
          error={
            remove.isError ? (
              <>
                <strong>Could not delete.</strong>{" "}
                {remove.error instanceof Error ? remove.error.message : "Azure Resource Manager did not respond."}
              </>
            ) : undefined
          }
          onConfirm={() => remove.mutate()}
          onCancel={() => setDeleting(false)}
        >
          <Text as="p">
            Deleting a Container App job is permanent and removes its execution history. This action can&rsquo;t be
            undone.
          </Text>
        </AzureConfirmDialog>
      ) : null}
      <div className="az-main" data-testid="ca-job-detail">
        {job.isError ? (
          <AzureErrorMessage testid="ca-job-error">
            <strong>Could not load this Container App job.</strong>{" "}
            {job.error instanceof Error ? job.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : job.isLoading || !job.data ? (
          <AzureEmptyState title="Loading the job…" loading />
        ) : (
          <>
            <AzureEssentials
              properties={[
                { label: "Resource group", value: resourceGroupOf(job.data.id) },
                { label: "Location", value: locationLabel(job.data.location) },
                { label: "Provisioning state", value: <AzureStatus status={job.data.provisioningState || "Unknown"} /> },
                { label: "Trigger type", value: job.data.triggerType || "—" },
                { label: "Replica timeout", value: job.data.replicaTimeout ? `${job.data.replicaTimeout}s` : "—" },
                {
                  label: "Environment",
                  value: job.data.environmentId ? (job.data.environmentId.split("/").pop() ?? "—") : "—",
                },
                { label: "Parallelism", value: job.data.parallelism ? String(job.data.parallelism) : "—" },
                { label: "Tags", value: tagsSummary(job.data.tags) },
              ]}
            />

            {panel === "config" ? (
              <ContainerAppJobEditForm
                job={job.data}
                busy={editJob.isPending}
                error={
                  editJob.isError ? (
                    <>
                      <strong>The job could not be updated.</strong>{" "}
                      {editJob.error instanceof Error ? editJob.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(config) => editJob.mutate(config)}
                onDismiss={() => setPanel(null)}
              />
            ) : null}
            {panel === "tags" ? (
              <TagsEditor
                tags={job.data.tags}
                busy={saveTags.isPending}
                testidPrefix="ca-job-tags"
                error={
                  saveTags.isError ? (
                    <>
                      <strong>The tags could not be saved.</strong>{" "}
                      {saveTags.error instanceof Error ? saveTags.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(tags) => saveTags.mutate(tags)}
                onDismiss={() => setPanel(null)}
              />
            ) : null}

            {mutationError ? (
              <AzureErrorMessage testid="ca-job-action-error">
                <strong>The job operation failed.</strong>{" "}
                {mutationError instanceof Error ? mutationError.message : "Azure Resource Manager did not respond."}
              </AzureErrorMessage>
            ) : null}

            <section className="az-blade-section" aria-label="Containers">
              <Text as="h2" weight="semibold" block>
                Containers
              </Text>
              <Table aria-label="Containers" size="small" data-testid="ca-job-containers">
                <TableHeader>
                  <TableRow>
                    <TableHeaderCell>Name</TableHeaderCell>
                    <TableHeaderCell>Image</TableHeaderCell>
                    <TableHeaderCell>Command</TableHeaderCell>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {job.data.containers.length === 0 ? (
                    <AzureTableEmptyRow colSpan={3} title="This job's template has no containers." />
                  ) : (
                    job.data.containers.map((container) => (
                      <TableRow key={container.name}>
                        <TableCell>{container.name}</TableCell>
                        <TableCell>
                          <code>{container.image}</code>
                        </TableCell>
                        <TableCell>
                          <code>{[...container.command, ...container.args].join(" ") || "—"}</code>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </section>

            <section className="az-blade-section" aria-label="Execution history">
              <Text as="h2" weight="semibold" block>
                Execution history
              </Text>
              <Table aria-label="Execution history" size="small" data-testid="ca-job-executions">
                <TableHeader>
                  <TableRow>
                    <TableHeaderCell>Name</TableHeaderCell>
                    <TableHeaderCell>Status</TableHeaderCell>
                    <TableHeaderCell>Start time</TableHeaderCell>
                    <TableHeaderCell>End time</TableHeaderCell>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {executions.isError ? (
                    <AzureTableErrorRow colSpan={4}>
                      <strong>Could not load executions.</strong>{" "}
                      {executions.error instanceof Error ? executions.error.message : "Azure Resource Manager did not respond."}
                    </AzureTableErrorRow>
                  ) : executions.isLoading ? (
                    <AzureTableLoadingRow colSpan={4} label="Loading executions…" />
                  ) : (executions.data ?? []).length === 0 ? (
                    <AzureTableEmptyRow colSpan={4} title="No executions yet" description="Runs of this job appear here." />
                  ) : (
                    (executions.data ?? []).map((execution) => (
                      <TableRow key={execution.name}>
                        <TableCell>{execution.name}</TableCell>
                        <TableCell>
                          <AzureStatus status={execution.status || "Unknown"} />
                        </TableCell>
                        <TableCell>{execution.startTime || "—"}</TableCell>
                        <TableCell>{execution.endTime || "—"}</TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </section>
          </>
        )}
      </div>
    </>
  );
}
