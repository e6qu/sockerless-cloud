import { useState } from "react";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { crmProjectNumber, searchProjects, type CrmProject } from "../api.js";
import { GcpDialog } from "./GcpDialog.js";
import { Icon } from "./icons.js";
import { locate, productHome } from "./catalog.js";
import { recentProjects, useProject } from "./project.js";

// The top bar's project picker and its "Select a project" dialog: the projects
// the operator can see, read from Cloud Resource Manager's projects.search,
// under Recent and All tabs, with New project opening the create page.

export const PROJECTS_QUERY_PREFIX = "crm-projects";

// The picker lists the projects that can be selected — the ACTIVE ones — through
// the API's own search query; Manage resources shows the full lifecycle.
export const ACTIVE_PROJECTS_QUERY_KEY = [PROJECTS_QUERY_PREFIX, "state:ACTIVE"];

export function useActiveProjects() {
  return useQuery({ queryKey: ACTIVE_PROJECTS_QUERY_KEY, queryFn: () => searchProjects("state:ACTIVE") });
}

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
    <table className="gc-table gc-picker-table">
      <thead>
        <tr>
          <th scope="col">Name</th>
          <th scope="col">ID</th>
          <th scope="col">Number</th>
        </tr>
      </thead>
      <tbody>
        {shown.map((project) => {
          const isCurrent = project.projectId === current;
          return (
            <tr key={project.projectId} className={isCurrent ? "gc-row-selected" : undefined}>
              <td>
                <button
                  type="button"
                  className="gc-picker-name"
                  data-testid={`project-row-${project.projectId}`}
                  aria-current={isCurrent ? "true" : undefined}
                  onClick={() => onSelect(project.projectId)}
                >
                  <Icon name={isCurrent ? "check" : "account_tree"} size="1.15em" />
                  {project.displayName || project.projectId}
                </button>
              </td>
              <td>{project.projectId}</td>
              <td>{crmProjectNumber(project)}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function ProjectPickerDialog({ onClose }: { onClose: () => void }) {
  const { project, setProject } = useProject();
  const navigate = useNavigate();
  const [tab, setTab] = useState<"recent" | "all">("recent");
  const [filter, setFilter] = useState("");
  const projects = useActiveProjects();
  const recent = recentProjects();
  const all = projects.data ?? [];
  const shown =
    tab === "all"
      ? all
      : [project, ...recent.filter((id) => id !== project)]
          .map((id) => all.find((candidate) => candidate.projectId === id))
          .filter((candidate): candidate is CrmProject => Boolean(candidate));

  return (
    <GcpDialog title="Select a project" testId="project-dialog" onClose={onClose} wide>
      <div className="gc-picker-toolbar">
        <label className="gc-search-field">
          <Icon name="search" size="1.25em" />
          <input
            type="search"
            aria-label="Search projects and folders"
            placeholder="Search projects and folders"
            value={filter}
            onChange={(event) => {
              setFilter(event.target.value);
              if (event.target.value) setTab("all");
            }}
          />
        </label>
        <button
          type="button"
          className="gc-button-outlined"
          data-testid="project-create-open"
          onClick={() => {
            onClose();
            navigate(`/ui/projectcreate?project=${encodeURIComponent(project)}`);
          }}
        >
          New project
        </button>
      </div>
      <div role="tablist" aria-label="Projects" className="gc-tablist">
        {(["recent", "all"] as const).map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            className={tab === id ? "gc-tab gc-tab-active" : "gc-tab"}
            onClick={() => setTab(id)}
          >
            {id === "recent" ? "Recent" : "All"}
          </button>
        ))}
      </div>
      <div className="gc-picker-body">
        {projects.isError ? (
          <div className="gc-message gc-message-error" role="alert">
            <strong>Couldn't list projects.</strong>{" "}
            {projects.error instanceof Error ? projects.error.message : "The API did not respond."}
          </div>
        ) : projects.isLoading ? (
          <p className="gc-picker-empty">Loading projects…</p>
        ) : (
          <ProjectPickerList
            projects={shown}
            current={project}
            filter={filter}
            onSelect={(projectId) => {
              const { product } = locate(window.location.pathname);
              setProject(projectId, product ? productHome(product) : undefined);
              onClose();
            }}
          />
        )}
      </div>
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
      </div>
    </GcpDialog>
  );
}

/** The top bar's project selector, naming the selected project. */
export function ProjectPickerChip() {
  const { project } = useProject();
  const [open, setOpen] = useState(false);
  const projects = useActiveProjects();
  const current = projects.data?.find((candidate) => candidate.projectId === project);
  const name = current?.displayName || project;
  return (
    <>
      <button
        type="button"
        className="gc-project-chip"
        data-testid="project-picker"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`Project: ${name}. Select a project`}
        onClick={() => setOpen(true)}
      >
        <Icon name="account_tree" size="1.25em" />
        <span className="gc-project-chip-name">{name}</span>
        <Icon name="arrow_drop_down" size="1.5em" />
      </button>
      {open ? <ProjectPickerDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}
