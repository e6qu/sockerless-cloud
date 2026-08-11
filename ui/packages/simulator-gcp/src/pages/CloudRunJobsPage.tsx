import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import {
  CONSOLE_REGION,
  createCloudRunJob,
  deleteCloudRunJob,
  fetchCloudRunJobsReal,
  runCloudRunJob,
  updateCloudRunJob,
  waitV2Operation,
  type CloudRunEnvVar,
  type CloudRunJob,
} from "../api.js";
import { useProject } from "../console/project.js";

// Cloud Run's resource-name contract for a job: up to 63 characters of
// lowercase letters, digits and hyphens, starting with a letter and not ending
// with a hyphen — checked before submitting so the dialog explains the rule.
const JOB_ID_PATTERN = /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

// pairsToEnv converts the key/value editor's rows into the real Cloud Run
// container `env` array shape.
function pairsToEnv(pairs: LabelPair[]): CloudRunEnvVar[] {
  return Object.entries(pairsToLabels(pairs)).map(([name, value]) => ({ name, value }));
}

function envToPairs(env: CloudRunEnvVar[] | undefined): LabelPair[] {
  return (env ?? []).map((variable) => ({ key: variable.name, value: variable.value ?? "" }));
}

// The real Job resource reports readiness through its terminal condition; the
// console reads that rather than inventing a status field.
function jobState(job: CloudRunJob): string {
  const condition = job.terminalCondition;
  if (!condition) return job.reconciling ? "Reconciling" : "Unknown";
  if (condition.state === "CONDITION_SUCCEEDED") return "Ready";
  if (condition.state === "CONDITION_FAILED") return condition.message ? `Failed: ${condition.message}` : "Failed";
  return condition.state ?? "Unknown";
}

const columns: GcpColumn<CloudRunJob>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link className="gc-cell-link" to={`/ui/cloudrun/${shortName(row.name)}`}>{shortName(row.name)}</Link>,
    value: (row) => shortName(row.name),
  },
  { id: "state", header: "Status of last execution", cell: (row) => <GcpStatus status={jobState(row)} />, value: jobState },
  { id: "created", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
  { id: "executions", header: "Executions", cell: (row) => row.executionCount ?? 0, value: (row) => String(row.executionCount ?? 0) },
  { id: "launchStage", header: "Launch stage", cell: (row) => row.launchStage ?? "—", value: (row) => row.launchStage ?? "" },
];

// CreateJobDialog deploys a new job through the real
// projects.locations.jobs.create long-running operation, composing the real
// nested execution/task template from the form's fields and driving the
// operation to completion through the same operations.get poll
// (waitV2Operation) the delete flow uses.
export function CreateJobDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [jobId, setJobId] = useState("");
  const [image, setImage] = useState("");
  const [taskCount, setTaskCount] = useState("1");
  const [timeoutSeconds, setTimeoutSeconds] = useState("600");
  const [pairs, setPairs] = useState<LabelPair[]>([]);

  const create = useMutation({
    mutationFn: async () =>
      waitV2Operation(
        await createCloudRunJob(project, jobId, {
          image,
          taskCount: Number(taskCount),
          timeoutSeconds: Number(timeoutSeconds),
          env: pairsToEnv(pairs),
        }),
      ),
    onSuccess: onCreated,
  });

  const valid =
    JOB_ID_PATTERN.test(jobId) && image.trim().length > 0 && Number(taskCount) >= 1 && Number(timeoutSeconds) > 0;

  return (
    <GcpDialog title="Create job" testId="cloudrun-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Job name
        <input type="text" value={jobId} data-testid="cloudrun-create-id" onChange={(event) => setJobId(event.target.value)} />
        <p className="gc-field-hint">Up to 63 lowercase letters, numbers or hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        Container image URL
        <input type="text" value={image} data-testid="cloudrun-create-image" onChange={(event) => setImage(event.target.value)} />
      </label>
      <label className="gc-field">
        Region
        <input type="text" value={CONSOLE_REGION} data-testid="cloudrun-create-region" readOnly />
      </label>
      <label className="gc-field">
        Number of tasks
        <input
          type="number"
          min={1}
          value={taskCount}
          data-testid="cloudrun-create-tasks"
          onChange={(event) => setTaskCount(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Task timeout (seconds)
        <input
          type="number"
          min={1}
          value={timeoutSeconds}
          data-testid="cloudrun-create-timeout"
          onChange={(event) => setTimeoutSeconds(event.target.value)}
        />
      </label>
      <LabelsEditor
        pairs={pairs}
        onChange={setPairs}
        idPrefix="cloudrun-create-env"
        title="Environment variables"
        addLabel="Add variable"
        entryNoun="Variable"
      />
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the job.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="cloudrun-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

// EditJobDialog edits the job's image, task count, timeout and environment
// through the real projects.locations.jobs.patch operation. UpdateJob replaces
// the full mutable resource, so the loaded job is carried forward with the
// edited template fields applied rather than sending a partial patch that would
// drop the rest of the template. Shared by the job detail page's Edit action.
export function EditJobDialog({
  project,
  job,
  onClose,
  onSaved,
}: {
  project: string;
  job: CloudRunJob;
  onClose: () => void;
  onSaved: () => void;
}) {
  const container = job.template?.template?.containers?.[0];
  const [image, setImage] = useState(container?.image ?? "");
  const [taskCount, setTaskCount] = useState(String(job.template?.taskCount ?? 1));
  const [timeoutSeconds, setTimeoutSeconds] = useState(
    String(Number.parseInt(job.template?.template?.timeout ?? "600", 10) || 600),
  );
  const [pairs, setPairs] = useState<LabelPair[]>(envToPairs(container?.env));

  const save = useMutation({
    mutationFn: async () =>
      waitV2Operation(
        await updateCloudRunJob(project, shortName(job.name), {
          ...job,
          template: {
            ...job.template,
            taskCount: Number(taskCount),
            template: {
              ...job.template?.template,
              timeout: `${timeoutSeconds}s`,
              containers: [{ ...(container ?? {}), image, env: pairsToEnv(pairs) }],
            },
          },
        }),
      ),
    onSuccess: onSaved,
  });

  const valid = image.trim().length > 0 && Number(taskCount) >= 1 && Number(timeoutSeconds) > 0;

  return (
    <GcpDialog title="Edit job" testId="cloudrun-edit-dialog" onClose={onClose}>
      <label className="gc-field">
        Container image URL
        <input type="text" value={image} data-testid="cloudrun-edit-image" onChange={(event) => setImage(event.target.value)} />
      </label>
      <label className="gc-field">
        Number of tasks
        <input
          type="number"
          min={1}
          value={taskCount}
          data-testid="cloudrun-edit-tasks"
          onChange={(event) => setTaskCount(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Task timeout (seconds)
        <input
          type="number"
          min={1}
          value={timeoutSeconds}
          data-testid="cloudrun-edit-timeout"
          onChange={(event) => setTimeoutSeconds(event.target.value)}
        />
      </label>
      <LabelsEditor
        pairs={pairs}
        onChange={setPairs}
        idPrefix="cloudrun-edit-env"
        title="Environment variables"
        addLabel="Add variable"
        entryNoun="Variable"
      />
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the job.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="cloudrun-edit-submit"
          disabled={!valid || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

// RunJobDialog executes the job through the real projects.locations.jobs.run
// operation, which creates a new execution (surfaced in the job's Executions
// tab). Driven to completion through the same operations.get poll the other
// job operations use. Shared by the list's per-row Run action and the job
// detail page's Run action.
export function RunJobDialog({
  project,
  jobId,
  onClose,
  onRan,
}: {
  project: string;
  jobId: string;
  onClose: () => void;
  onRan: () => void;
}) {
  const run = useMutation({
    mutationFn: async () => waitV2Operation(await runCloudRunJob(project, jobId)),
    onSuccess: onRan,
  });
  return (
    <GcpDialog title="Execute job?" testId="cloudrun-run-dialog" onClose={onClose}>
      <p>
        Executing <strong>{jobId}</strong> starts a new execution of its tasks. The execution appears in
        the job's Executions tab.
      </p>
      {run.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't execute the job.</strong>{" "}
          {run.error instanceof Error ? run.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="cloudrun-run-confirm"
          disabled={run.isPending}
          onClick={() => run.mutate()}
        >
          {run.isPending ? "Executing…" : "Execute"}
        </button>
      </div>
    </GcpDialog>
  );
}

// DeleteJobDialog is shared by the list's per-row action and the job detail
// page's header action — the same real projects.locations.jobs.delete
// long-running operation, driven through the same operations.get poll
// (waitV2Operation) Cloud Functions deletion uses.
export function DeleteJobDialog({
  project,
  jobId,
  onClose,
  onDeleted,
}: {
  project: string;
  jobId: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({
    mutationFn: async () => waitV2Operation(await deleteCloudRunJob(project, jobId)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete job?" testId="cloudrun-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{jobId}</strong> permanently removes the job and its execution history.
        This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the job.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="cloudrun-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudRunJobsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [running, setRunning] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["cloudrun-jobs-real", project] });

  const columnsWithActions: GcpColumn<CloudRunJob>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = shortName(row.name);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`cloudrun-run-${id}`}
              aria-label={`Run ${id}`}
              onClick={() => setRunning(id)}
            >
              Run
            </button>
            <button
              type="button"
              className="gc-button-text"
              data-testid={`cloudrun-delete-${id}`}
              aria-label={`Delete ${id}`}
              onClick={() => setDeleting(id)}
            >
              Delete
            </button>
          </span>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<CloudRunJob>
        title="Cloud Run jobs"
        description="A job executes tasks to completion. Jobs are ideal for batch processing and scheduled workloads."
        actions={[
          { label: "Create job", icon: "add", primary: true, testId: "cloudrun-create-job", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["cloudrun-jobs-real", project]}
        queryFn={() => fetchCloudRunJobsReal(project)}
        filterPlaceholder="Filter jobs"
        resourceNoun="jobs"
        empty={{
          headline: "Execute jobs on a fully managed platform",
          description: "Cloud Run jobs execute containers to completion.",
          primaryLabel: "Create job",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateJobDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {running ? (
        <RunJobDialog
          project={project}
          jobId={running}
          onClose={() => setRunning(null)}
          onRan={() => {
            setRunning(null);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteJobDialog
          project={project}
          jobId={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}
