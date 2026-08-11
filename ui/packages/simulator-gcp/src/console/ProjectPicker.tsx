import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createProject,
  crmProjectNumber,
  searchProjects,
  waitCrmOperation,
  type CrmProject,
} from "../api.js";
import { GcpDialog } from "./GcpDialog.js";
import { Icon } from "./icons.js";
import { useProject } from "./project.js";

// The header's project picker: a chip naming the selected project that opens
// the project dialog — a searchable list of the projects the operator can
// see, read from the real Cloud Resource Manager v3 SearchProjects API over
// the federated credential, with NEW PROJECT creating one through the real
// projects.create long-running operation.

export const PROJECTS_QUERY_PREFIX = "crm-projects";

// The picker lists selectable projects — the ACTIVE ones — through the
// API's own search query; Manage resources shows the full lifecycle.
export const ACTIVE_PROJECTS_QUERY_KEY = [PROJECTS_QUERY_PREFIX, "state:ACTIVE"];

// The Resource Manager project-ID contract: 6–30 characters, lowercase
// letters, digits and hyphens, starting with a letter and not ending with a
// hyphen. Checked before submitting so the dialog can explain the rule.
const PROJECT_ID_PATTERN = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;

// The console derives the project ID from the name as it is typed, the way
// the real New Project dialog fills the ID field.
function deriveProjectId(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 30);
}

// ProjectPickerList is the dialog's project list: filtered by the search
// box, current selection marked, one button per project.
export function ProjectPickerList({
  projects,
  current,
  filter,
  onSelect,
}: {
  projects: CrmProject[];
  current: string;
  filter: string;
  onSelect: (projectId: string) => void;
}) {
  const needle = filter.trim().toLowerCase();
  const shown = projects.filter(
    (project) =>
      !needle ||
      project.projectId.toLowerCase().includes(needle) ||
      (project.displayName ?? "").toLowerCase().includes(needle),
  );
  if (shown.length === 0) {
    return <p className="gc-picker-empty">No projects match.</p>;
  }
  return (
    <ul className="gc-picker-list">
      {shown.map((project) => (
        <li key={project.projectId}>
          <button
            type="button"
            className={
              project.projectId === current ? "gc-picker-row gc-picker-row-current" : "gc-picker-row"
            }
            data-testid={`project-row-${project.projectId}`}
            onClick={() => onSelect(project.projectId)}
          >
            <span className="gc-picker-row-name">{project.displayName || project.projectId}</span>
            <span className="gc-picker-row-meta">
              {project.projectId} · {crmProjectNumber(project)}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

// NewProjectForm is the dialog's create view: name, derived editable ID, and
// the create submit that drives the real long-running operation. While the
// operation runs the dialog shows it progressing, the way the real console
// reports project creation, then lands on the created project.
export function NewProjectForm({
  creating,
  error,
  onBack,
  onCreate,
}: {
  creating: boolean;
  error: string | null;
  onBack: () => void;
  onCreate: (projectId: string, displayName: string) => void;
}) {
  const [displayName, setDisplayName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [projectIdEdited, setProjectIdEdited] = useState(false);
  const idValid = PROJECT_ID_PATTERN.test(projectId);

  return (
    <>
      <label className="gc-field">
        Project name
        <input
          type="text"
          value={displayName}
          data-testid="project-create-name"
          disabled={creating}
          onChange={(event) => {
            setDisplayName(event.target.value);
            if (!projectIdEdited) setProjectId(deriveProjectId(event.target.value));
          }}
        />
        <p className="gc-field-hint">Display name for this project</p>
      </label>
      <label className="gc-field">
        Project ID
        <input
          type="text"
          value={projectId}
          data-testid="project-create-id"
          disabled={creating}
          onChange={(event) => {
            setProjectIdEdited(true);
            setProjectId(event.target.value);
          }}
        />
        <p className="gc-field-hint">
          6–30 lowercase letters, digits or hyphens; must start with a letter. Cannot be changed later.
        </p>
      </label>
      {creating ? (
        <p className="gc-picker-progress" data-testid="project-create-progress" role="status">
          <span aria-hidden className="gc-picker-spinner" />
          Creating project “{projectId}”…
        </p>
      ) : null}
      {error ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the project.</strong> {error}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onBack} disabled={creating}>
          Back
        </button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="project-create-submit"
          disabled={!idValid || creating}
          onClick={() => onCreate(projectId, displayName)}
        >
          Create
        </button>
      </div>
    </>
  );
}

export function ProjectPickerDialog({ onClose }: { onClose: () => void }) {
  const { project, setProject } = useProject();
  const queryClient = useQueryClient();
  const [view, setView] = useState<"list" | "create">("list");
  const [filter, setFilter] = useState("");

  const projects = useQuery({
    queryKey: ACTIVE_PROJECTS_QUERY_KEY,
    queryFn: () => searchProjects("state:ACTIVE"),
  });

  const create = useMutation({
    mutationFn: async ({ projectId, displayName }: { projectId: string; displayName: string }) => {
      await waitCrmOperation(await createProject(projectId, displayName));
      return projectId;
    },
    onSuccess: (projectId) => {
      void queryClient.invalidateQueries({ queryKey: [PROJECTS_QUERY_PREFIX] });
      setProject(projectId);
      onClose();
    },
  });

  return (
    <GcpDialog title={view === "list" ? "Select a project" : "New project"} testId="project-dialog" onClose={onClose}>
      {view === "list" ? (
        <>
          <div className="gc-picker-toolbar">
            <input
              type="search"
              className="gc-picker-search"
              aria-label="Search projects and folders"
              placeholder="Search projects and folders"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
            />
            <button
              type="button"
              className="gc-button-text"
              data-testid="project-create-open"
              onClick={() => setView("create")}
            >
              New project
            </button>
          </div>
          {projects.isError ? (
            <div className="gc-message gc-message-error" role="alert">
              <strong>Couldn't list projects.</strong>{" "}
              {projects.error instanceof Error ? projects.error.message : "The API did not respond."}
            </div>
          ) : projects.isLoading ? (
            <p className="gc-picker-empty">Loading projects…</p>
          ) : (
            <ProjectPickerList
              projects={projects.data ?? []}
              current={project}
              filter={filter}
              onSelect={(projectId) => {
                setProject(projectId);
                onClose();
              }}
            />
          )}
          <div className="gc-dialog-actions">
            <Link className="gc-button-text" to="/ui/projects" onClick={onClose} data-testid="project-manage-link">
              Manage resources
            </Link>
            <button type="button" className="gc-button-text" onClick={onClose}>
              Cancel
            </button>
          </div>
        </>
      ) : (
        <NewProjectForm
          creating={create.isPending}
          error={
            create.isError
              ? create.error instanceof Error
                ? create.error.message
                : "The API did not respond."
              : null
          }
          onBack={() => setView("list")}
          onCreate={(projectId, displayName) => create.mutate({ projectId, displayName })}
        />
      )}
    </GcpDialog>
  );
}

// ProjectPickerChip is the header chip: the selected project's ID with the
// picker affordance, opening the project dialog.
export function ProjectPickerChip() {
  const { project } = useProject();
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        className="gc-project-chip"
        data-testid="project-picker"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(true)}
      >
        <span aria-hidden className="gc-project-dot" />
        {project}
        <Icon name="unfold_more" size="1em" />
      </button>
      {open ? <ProjectPickerDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}
