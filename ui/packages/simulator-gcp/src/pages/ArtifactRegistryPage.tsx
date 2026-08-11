import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { LabelsEditor, labelsToPairs, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import {
  CONSOLE_REGION,
  createARRepository,
  deleteARRepository,
  fetchARRepos,
  updateARRepository,
  waitArOperation,
  type ARRepo,
} from "../api.js";
import { useProject } from "../console/project.js";

const columns: GcpColumn<ARRepo>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link className="gc-cell-link" to={`/ui/ar/${shortName(row.name)}`}>{shortName(row.name)}</Link>,
    value: (row) => shortName(row.name),
  },
  { id: "format", header: "Format", cell: (row) => row.format ?? "—", value: (row) => row.format ?? "" },
  { id: "mode", header: "Mode", cell: (row) => row.mode ?? "Standard", value: (row) => row.mode ?? "" },
  { id: "createTime", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
];

// Artifact Registry's repository-ID contract: up to 63 characters of
// lowercase letters, numbers and hyphens, starting with a letter — checked
// before submitting so the dialog explains the rule the real Create
// repository page does.
const REPO_ID_PATTERN = /^[a-z][a-z0-9-]{0,62}$/;

const FORMATS = ["DOCKER", "MAVEN", "NPM", "PYTHON", "APT", "YUM", "GO", "GENERIC"] as const;

export function CreateRepoDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [repositoryId, setRepositoryId] = useState("");
  const [format, setFormat] = useState<(typeof FORMATS)[number]>("DOCKER");

  const create = useMutation({
    mutationFn: async () =>
      waitArOperation(await createARRepository(project, CONSOLE_REGION, repositoryId, format)),
    onSuccess: onCreated,
  });

  const valid = REPO_ID_PATTERN.test(repositoryId);

  return (
    <GcpDialog title="Create repository" testId="ar-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Name
        <input
          type="text"
          value={repositoryId}
          data-testid="ar-create-id"
          onChange={(event) => setRepositoryId(event.target.value)}
        />
        <p className="gc-field-hint">
          Up to 63 lowercase letters, numbers or hyphens; must start with a letter.
        </p>
      </label>
      <label className="gc-field">
        Format
        <select
          value={format}
          data-testid="ar-create-format"
          onChange={(event) => setFormat(event.target.value as (typeof FORMATS)[number])}
        >
          {FORMATS.map((candidate) => (
            <option key={candidate} value={candidate}>
              {candidate}
            </option>
          ))}
        </select>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the repository.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="ar-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

// EditRepoDialog edits a repository's labels through the real
// projects.locations.repositories.patch operation (UpdateRepository, which the
// real API answers synchronously — no operation to poll). Shared by the
// repository detail page's Edit action.
export function EditRepoDialog({
  project,
  repo,
  onClose,
  onSaved,
}: {
  project: string;
  repo: ARRepo;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [pairs, setPairs] = useState<LabelPair[]>(labelsToPairs(repo.labels));

  const save = useMutation({
    mutationFn: () => updateARRepository(project, shortName(repo.name), { labels: pairsToLabels(pairs) }),
    onSuccess: onSaved,
  });

  return (
    <GcpDialog title="Edit repository" testId="ar-edit-dialog" onClose={onClose}>
      <LabelsEditor pairs={pairs} onChange={setPairs} idPrefix="ar-edit" />
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the repository.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="ar-edit-submit"
          disabled={save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

// DeleteRepoDialog is shared by the list's per-row action and the repository
// detail page's header action — the same real
// projects.locations.repositories.delete long-running operation, driven
// through the same operations.get poll (waitArOperation) the create flow uses.
export function DeleteRepoDialog({
  project,
  repositoryId,
  onClose,
  onDeleted,
}: {
  project: string;
  repositoryId: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({
    mutationFn: async () => waitArOperation(await deleteARRepository(project, repositoryId)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete repository?" testId="ar-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{repositoryId}</strong> permanently removes the repository and every image,
        package, and file it holds. This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the repository.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="ar-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ArtifactRegistryPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["ar-repos-real", project] });

  const columnsWithActions: GcpColumn<ARRepo>[] = [
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
            data-testid={`ar-delete-${id}`}
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
      <GcpResourceTable<ARRepo>
        title="Artifact Registry"
        description="Store and manage your build artifacts — container images and language packages — in one place."
        actions={[
          { label: "Create repository", icon: "add", primary: true, testId: "ar-create-repo", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["ar-repos-real", project]}
        queryFn={() => fetchARRepos(project)}
        filterPlaceholder="Filter repositories"
        resourceNoun="repositories"
        empty={{
          headline: "Store your build artifacts",
          description: "Create a repository to store container images and language packages.",
          primaryLabel: "Create repository",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateRepoDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteRepoDialog
          project={project}
          repositoryId={deleting}
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
