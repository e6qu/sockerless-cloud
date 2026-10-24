import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { PROJECTS_QUERY_PREFIX } from "../console/ProjectPicker.js";
import { useOperations } from "../console/notifications.js";
import { useProject } from "../console/project.js";
import { createProject, waitCrmOperation } from "../api.js";

// The Resource Manager project-ID contract: 6–30 characters, lowercase
// letters, digits and hyphens, starting with a letter and not ending with a
// hyphen.
const PROJECT_ID_PATTERN = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;

// The real New Project page fills the ID from the name as it is typed.
export function deriveProjectId(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 30);
}

export function NewProjectForm({
  creating,
  error,
  onCancel,
  onCreate,
}: {
  creating: boolean;
  error: string | null;
  onCancel: () => void;
  onCreate: (projectId: string, displayName: string) => void;
}) {
  const [displayName, setDisplayName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [projectIdEdited, setProjectIdEdited] = useState(false);
  const idValid = PROJECT_ID_PATTERN.test(projectId);

  return (
    <form
      className="gc-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (idValid) onCreate(projectId, displayName);
      }}
    >
      <label className="gc-field">
        Project name
        <input
          type="text"
          required
          value={displayName}
          data-testid="project-create-name"
          disabled={creating}
          onChange={(event) => {
            setDisplayName(event.target.value);
            if (!projectIdEdited) setProjectId(deriveProjectId(event.target.value));
          }}
        />
      </label>
      <label className="gc-field">
        Project ID
        <input
          type="text"
          value={projectId}
          data-testid="project-create-id"
          aria-invalid={projectId !== "" && !idValid}
          disabled={creating}
          onChange={(event) => {
            setProjectIdEdited(true);
            setProjectId(event.target.value);
          }}
        />
        <span className="gc-field-hint">
          6 to 30 lowercase letters, digits or hyphens, starting with a letter. It can't be changed later.
        </span>
      </label>
      {error ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the project.</strong> {error}
        </div>
      ) : null}
      <div className="gc-form-actions">
        <button type="submit" className="gc-button-primary" data-testid="project-create-submit" disabled={!idValid || creating}>
          {creating ? "Creating…" : "Create"}
        </button>
        <button type="button" className="gc-button-text" onClick={onCancel} disabled={creating}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// The New Project page: projects.create is a long-running operation, polled
// through operations.get until the project exists, then selected.
export function CreateProjectPage() {
  const { setProject } = useProject();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const create = useMutation({
    mutationFn: ({ projectId, displayName }: { projectId: string; displayName: string }) =>
      track(
        `Create project "${projectId}"`,
        async () => {
          await waitCrmOperation(await createProject(projectId, displayName));
          return projectId;
        },
        `Created project "${projectId}"`,
      ),
    onSuccess: (projectId) => {
      void queryClient.invalidateQueries({ queryKey: [PROJECTS_QUERY_PREFIX] });
      setProject(projectId, "/ui/home/dashboard");
    },
  });
  return (
    <div className="gc-page-narrow">
      <GcpPageHeader title="New Project" backTo="/ui/cloud-resource-manager" />
      <NewProjectForm
        creating={create.isPending}
        error={create.isError ? (create.error instanceof Error ? create.error.message : String(create.error)) : null}
        onCancel={() => window.history.back()}
        onCreate={(projectId, displayName) => create.mutate({ projectId, displayName })}
      />
    </div>
  );
}
