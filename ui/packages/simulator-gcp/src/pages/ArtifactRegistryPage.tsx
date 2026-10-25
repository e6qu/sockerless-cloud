import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { LabelsEditor, labelsToPairs, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  CONSOLE_REGION,
  deleteARRepository,
  fetchARRepos,
  sendRequest,
  updateARRepository,
  waitArOperation,
  type ARRepo,
  type CrmOperation,
} from "../api.js";

// A repository's resource name is projects/{project}/locations/{location}/repositories/{id}.
export const repositoryProject = (repo: ARRepo): string => repo.name.split("/")[1] ?? "";
export const repositoryLocation = (repo: ARRepo): string => repo.name.split("/")[3] ?? CONSOLE_REGION;

export function repositoryPath(repo: ARRepo): string {
  return `/ui/artifacts/${(repo.format ?? "docker").toLowerCase()}/${repositoryProject(repo)}/${repositoryLocation(repo)}/${shortName(repo.name)}`;
}

const formatLabel = (format: string | undefined): string =>
  format ? format.charAt(0) + format.slice(1).toLowerCase() : "—";

function RepositoryName({ repo }: { repo: ARRepo }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(repositoryPath(repo))}>
      {shortName(repo.name)}
    </Link>
  );
}

const columns: GcpColumn<ARRepo>[] = [
  { id: "name", header: "Name", cell: (row) => <RepositoryName repo={row} />, value: (row) => shortName(row.name) },
  { id: "format", header: "Format", cell: (row) => formatLabel(row.format), value: (row) => row.format ?? "" },
  { id: "mode", header: "Mode", cell: (row) => formatLabel(row.mode?.replace(/_REPOSITORY$/, "") ?? "STANDARD"), value: (row) => row.mode ?? "STANDARD" },
  { id: "location", header: "Location", cell: repositoryLocation, value: repositoryLocation },
  { id: "description", header: "Description", cell: (row) => row.description || "—", value: (row) => row.description ?? "" },
  {
    id: "labels",
    header: "Labels",
    cell: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}: ${value}`).join(", ") || "—",
    value: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}:${value}`).join(" "),
    defaultHidden: true,
  },
  { id: "encryption", header: "Encryption", cell: () => "Google-managed key", value: () => "Google-managed key" },
  { id: "created", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
  { id: "updated", header: "Updated", cell: (row) => formatTimestamp(row.updateTime ?? ""), value: (row) => row.updateTime ?? "", defaultHidden: true },
];

// Artifact Registry's repository-ID contract: up to 63 characters of
// lowercase letters, numbers and hyphens, starting with a letter.
const REPO_ID_PATTERN = /^[a-z][a-z0-9-]{0,62}$/;

const FORMATS = ["DOCKER", "MAVEN", "NPM", "PYTHON", "APT", "YUM", "GO", "GENERIC"] as const;

// EditRepoDialog edits a repository's description and labels through
// projects.locations.repositories.patch.
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
  const { track } = useOperations();
  const name = shortName(repo.name);
  const save = useMutation({
    mutationFn: () =>
      track(`Update repository "${name}"`, () => updateARRepository(project, name, { labels: pairsToLabels(pairs) }), `Updated repository "${name}"`),
    onSuccess: onSaved,
  });
  return (
    <GcpDialog title="Edit repository" testId="ar-edit-dialog" onClose={onClose}>
      <LabelsEditor pairs={pairs} onChange={setPairs} idPrefix="ar-edit" />
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the repository.</strong> {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="gc-button-primary" data-testid="ar-edit-submit" disabled={save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

// DeleteRepoDialog runs projects.locations.repositories.delete, a long-running
// operation, on each repository.
export function DeleteRepoDialog({
  project,
  repositoryIds,
  onClose,
  onDeleted,
}: {
  project: string;
  repositoryIds: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { track } = useOperations();
  const subject = repositoryIds.length === 1 ? `repository "${repositoryIds[0]}"` : `${repositoryIds.length} repositories`;
  const remove = useMutation({
    mutationFn: () =>
      track(
        `Delete ${subject}`,
        () => Promise.all(repositoryIds.map(async (id) => waitArOperation(await deleteARRepository(project, id)))),
        `Deleted ${subject}`,
      ),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="ar-delete-dialog" onClose={onClose}>
      <p>Deleting removes every image, package and file the repository holds. This can't be undone.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong> {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary gc-button-danger"
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
  const [deleting, setDeleting] = useState<string[] | null>(null);
  return (
    <>
      <GcpResourceTable<ARRepo>
        title="Repositories"
        actions={[{ label: "Create repository", icon: "add", to: "/ui/artifacts/create-repo", testId: "ar-create-repo" }]}
        selectionActions={(selected) => [
          { label: "Delete", icon: "delete", onSelect: () => setDeleting(selected.map((row) => shortName(row.name))) },
        ]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting([shortName(row.name)]) }]}
        columns={columns}
        queryKey={["ar-repos-real", project]}
        queryFn={() => fetchARRepos(project)}
        resourceNoun="repositories"
        testId="ar-repos-table"
        empty={{ headline: "No repositories", description: "Repositories store container images and language packages." }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteRepoDialog
          project={project}
          repositoryIds={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["ar-repos-real", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function CreateRepositoryPage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [repositoryId, setRepositoryId] = useState("");
  const [format, setFormat] = useState<(typeof FORMATS)[number]>("DOCKER");
  const [description, setDescription] = useState("");
  const [labels, setLabels] = useState<LabelPair[]>([]);
  const location = CONSOLE_REGION;
  const labelMap = pairsToLabels(labels);
  const request = {
    method: "POST" as const,
    path: `/v1/projects/${project}/locations/${location}/repositories?repositoryId=${encodeURIComponent(repositoryId)}`,
    body: {
      format,
      ...(description ? { description } : {}),
      ...(Object.keys(labelMap).length > 0 ? { labels: labelMap } : {}),
    },
  };
  const idError = repositoryId && !REPO_ID_PATTERN.test(repositoryId)
    ? "Use up to 63 lowercase letters, numbers or hyphens, starting with a letter."
    : null;
  const create = useMutation({
    mutationFn: () =>
      track(
        `Create repository "${repositoryId}"`,
        async () => waitArOperation(await sendRequest<CrmOperation>(request)),
        `Created repository "${repositoryId}"`,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["ar-repos-real", project] });
      navigate(path(`/ui/artifacts/${format.toLowerCase()}/${project}/${location}/${repositoryId}`));
    },
  });
  return (
    <CreatePage
      title="Create repository"
      backTo="/ui/artifacts"
      testId="ar-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(repositoryId && !idError)}
      error={create.error}
      errorTitle="Couldn't create the repository."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Repository">
        <Field label="Name" error={idError}>
          <input type="text" value={repositoryId} data-testid="ar-create-id" onChange={(event) => setRepositoryId(event.target.value)} />
        </Field>
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Format</legend>
          {FORMATS.map((candidate) => (
            <label className="gc-radio" key={candidate}>
              <input type="radio" name="format" checked={format === candidate} onChange={() => setFormat(candidate)} />
              {formatLabel(candidate)}
            </label>
          ))}
        </fieldset>
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Mode</legend>
          <label className="gc-radio">
            <input type="radio" name="mode" checked readOnly />
            Standard
          </label>
        </fieldset>
      </FormSection>
      <FormSection title="Location type">
        <Field label="Region">
          <select value={location} disabled>
            <option value={location}>{location}</option>
          </select>
        </Field>
      </FormSection>
      <FormSection title="Details">
        <Field label="Description">
          <textarea rows={2} value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <LabelsEditor pairs={labels} onChange={setLabels} idPrefix="ar-create" />
      </FormSection>
    </CreatePage>
  );
}
