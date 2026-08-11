import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { LabelsEditor, labelsToPairs, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import {
  CONSOLE_REGION,
  createCloudFunction,
  deleteCloudFunction,
  fetchCloudFunctions,
  updateCloudFunction,
  waitV2Operation,
  type CloudFunction,
} from "../api.js";
import { useProject } from "../console/project.js";

// The memory options the real Edit function page offers for a Gen2 function.
const MEMORY_OPTIONS = ["128Mi", "256Mi", "512Mi", "1Gi", "2Gi", "4Gi", "8Gi"] as const;

// A representative slice of the Cloud Functions runtimes the Create page lists.
const RUNTIMES = ["nodejs20", "nodejs22", "python312", "go122", "java21", "dotnet8", "ruby33", "php83"] as const;

// Cloud Functions' function-name contract: up to 63 characters of lowercase
// letters, digits and hyphens, starting with a letter and not ending with a
// hyphen — checked before submitting so the dialog explains the rule.
const FUNCTION_ID_PATTERN = /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

// EditFunctionDialog edits the serviceConfig fields the real Edit function page
// edits — memory, timeout, min/max instances and environment variables —
// through the real projects.locations.functions.patch long-running operation,
// driven to completion through the same operations.get poll (waitV2Operation)
// the delete flow uses. Shared by the function detail page's Edit action.
export function EditFunctionDialog({
  project,
  fn,
  onClose,
  onSaved,
}: {
  project: string;
  fn: CloudFunction;
  onClose: () => void;
  onSaved: () => void;
}) {
  const config = fn.serviceConfig;
  const [memory, setMemory] = useState(config?.availableMemory || "256Mi");
  const [timeoutSeconds, setTimeoutSeconds] = useState(String(config?.timeoutSeconds ?? 60));
  const [minInstances, setMinInstances] = useState(String(config?.minInstanceCount ?? 0));
  const [maxInstances, setMaxInstances] = useState(String(config?.maxInstanceCount ?? 100));
  const [pairs, setPairs] = useState<LabelPair[]>(labelsToPairs(config?.environmentVariables));

  const save = useMutation({
    mutationFn: async () =>
      waitV2Operation(
        await updateCloudFunction(project, shortName(fn.name), {
          // The whole serviceConfig is replaced at serviceConfig granularity,
          // so the loaded config is carried forward with the edited fields
          // applied — dropping nothing the operator didn't change.
          ...config,
          availableMemory: memory,
          timeoutSeconds: Number(timeoutSeconds),
          minInstanceCount: Number(minInstances),
          maxInstanceCount: Number(maxInstances),
          environmentVariables: pairsToLabels(pairs),
        }),
      ),
    onSuccess: onSaved,
  });

  const valid =
    Number(timeoutSeconds) > 0 && Number(minInstances) >= 0 && Number(maxInstances) >= Number(minInstances);

  return (
    <GcpDialog title="Edit function" testId="function-edit-dialog" onClose={onClose}>
      <label className="gc-field">
        Memory allocated
        <select value={memory} data-testid="function-edit-memory" onChange={(event) => setMemory(event.target.value)}>
          {MEMORY_OPTIONS.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      </label>
      <label className="gc-field">
        Request timeout (seconds)
        <input
          type="number"
          min={1}
          value={timeoutSeconds}
          data-testid="function-edit-timeout"
          onChange={(event) => setTimeoutSeconds(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Minimum number of instances
        <input
          type="number"
          min={0}
          value={minInstances}
          data-testid="function-edit-min-instances"
          onChange={(event) => setMinInstances(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Maximum number of instances
        <input
          type="number"
          min={0}
          value={maxInstances}
          data-testid="function-edit-max-instances"
          onChange={(event) => setMaxInstances(event.target.value)}
        />
      </label>
      <LabelsEditor
        pairs={pairs}
        onChange={setPairs}
        idPrefix="function-edit-env"
        title="Runtime environment variables"
        addLabel="Add variable"
        entryNoun="Variable"
      />
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the function.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="function-edit-submit"
          disabled={!valid || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

// CreateFunctionDialog deploys a new function through the real
// projects.locations.functions.create long-running operation, collecting the
// runtime and entry point the real Create page leads with.
export function CreateFunctionDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [functionId, setFunctionId] = useState("");
  const [runtime, setRuntime] = useState<(typeof RUNTIMES)[number]>("nodejs20");
  const [entryPoint, setEntryPoint] = useState("");

  const create = useMutation({
    mutationFn: async () =>
      waitV2Operation(await createCloudFunction(project, functionId, { runtime, entryPoint })),
    onSuccess: onCreated,
  });

  const valid = FUNCTION_ID_PATTERN.test(functionId) && entryPoint.trim().length > 0;

  return (
    <GcpDialog title="Create function" testId="function-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Function name
        <input
          type="text"
          value={functionId}
          data-testid="function-create-id"
          onChange={(event) => setFunctionId(event.target.value)}
        />
        <p className="gc-field-hint">
          Up to 63 lowercase letters, numbers or hyphens; must start with a letter.
        </p>
      </label>
      <label className="gc-field">
        Region
        <input type="text" value={CONSOLE_REGION} data-testid="function-create-region" readOnly />
      </label>
      <label className="gc-field">
        Runtime
        <select
          value={runtime}
          data-testid="function-create-runtime"
          onChange={(event) => setRuntime(event.target.value as (typeof RUNTIMES)[number])}
        >
          {RUNTIMES.map((candidate) => (
            <option key={candidate} value={candidate}>
              {candidate}
            </option>
          ))}
        </select>
      </label>
      <label className="gc-field">
        Entry point
        <input
          type="text"
          value={entryPoint}
          data-testid="function-create-entrypoint"
          onChange={(event) => setEntryPoint(event.target.value)}
        />
        <p className="gc-field-hint">The name of the function in your source that handles requests.</p>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the function.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="function-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

const columns: GcpColumn<CloudFunction>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link className="gc-cell-link" to={`/ui/functions/${shortName(row.name)}`}>{shortName(row.name)}</Link>,
    value: (row) => shortName(row.name),
  },
  { id: "state", header: "State", cell: (row) => <GcpStatus status={row.state ?? "UNKNOWN"} />, value: (row) => row.state ?? "" },
  { id: "environment", header: "Environment", cell: (row) => row.environment ?? "—", value: (row) => row.environment ?? "" },
  { id: "createTime", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
];

// DeleteFunctionDialog is shared by the list's per-row action and the
// function detail page's header action — the same real
// projects.locations.functions.delete long-running operation, driven through
// the same operations.get poll (waitV2Operation) Cloud Run job deletion uses.
export function DeleteFunctionDialog({
  project,
  functionId,
  onClose,
  onDeleted,
}: {
  project: string;
  functionId: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({
    mutationFn: async () => waitV2Operation(await deleteCloudFunction(project, functionId)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete function?" testId="function-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{functionId}</strong> permanently removes the function. Any triggers or
        callers relying on it fail immediately. This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the function.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="function-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudFunctionsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["cloud-functions-real", project] });

  const columnsWithActions: GcpColumn<CloudFunction>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = shortName(row.name);
        return (
          <button
            type="button"
            className="gc-button-text"
            data-testid={`function-delete-${id}`}
            aria-label={`Delete ${id}`}
            onClick={() => setDeleting(id)}
          >
            Delete
          </button>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<CloudFunction>
        title="Cloud Run functions"
        description="Run your code in response to events without provisioning or managing servers."
        actions={[
          { label: "Create function", icon: "add", primary: true, testId: "function-create", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["cloud-functions-real", project]}
        queryFn={() => fetchCloudFunctions(project)}
        filterPlaceholder="Filter functions"
        resourceNoun="functions"
        empty={{
          headline: "Write and deploy your first function",
          description: "Functions run your code in response to events, scaling from zero automatically.",
          primaryLabel: "Create function",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateFunctionDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteFunctionDialog
          project={project}
          functionId={deleting}
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
