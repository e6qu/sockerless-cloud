import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { PROJECTS_QUERY_PREFIX } from "../console/ProjectPicker.js";
import { formatTimestamp } from "../console/format.js";
import {
  crmProjectNumber,
  deleteProject,
  searchProjects,
  waitCrmOperation,
  type CrmProject,
} from "../api.js";

// The console's Manage resources page: every project the operator can see,
// across the full ACTIVE ⇄ DELETE_REQUESTED lifecycle, read from the real
// Cloud Resource Manager v3 SearchProjects API, with per-project deletion
// through the real projects.delete long-running operation (a soft delete —
// the project enters its 30-day pending-deletion window).

const ALL_PROJECTS_QUERY_KEY = [PROJECTS_QUERY_PREFIX, "all"];

export function DeleteProjectDialog({
  project,
  onClose,
  onDeleted,
}: {
  project: CrmProject;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({
    mutationFn: async () => waitCrmOperation(await deleteProject(project.projectId)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Shut down project?" testId="project-delete-dialog" onClose={onClose}>
      <p>
        Shutting down <strong>{project.displayName || project.projectId}</strong> ({project.projectId})
        schedules it for deletion: it stops serving immediately and is pending deletion for 30 days,
        during which it can be restored.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't shut down the project.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="project-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Shutting down…" : "Shut down"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ManageProjectsPage() {
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<CrmProject | null>(null);

  const columns: GcpColumn<CrmProject>[] = [
    {
      id: "name",
      header: "Name",
      cell: (row) => (
        <span data-testid={`manage-project-row-${row.projectId}`}>{row.displayName || row.projectId}</span>
      ),
      value: (row) => row.displayName || row.projectId,
    },
    { id: "id", header: "ID", cell: (row) => row.projectId, value: (row) => row.projectId },
    { id: "number", header: "Number", cell: crmProjectNumber, value: crmProjectNumber },
    {
      id: "state",
      header: "State",
      cell: (row) => <GcpStatus status={row.state === "DELETE_REQUESTED" ? "Pending deletion" : (row.state ?? "Unknown")} />,
      value: (row) => row.state ?? "",
    },
    {
      id: "created",
      header: "Created",
      cell: (row) => formatTimestamp(row.createTime ?? ""),
      value: (row) => row.createTime ?? "",
    },
    {
      id: "actions",
      header: "Actions",
      cell: (row) =>
        row.state === "DELETE_REQUESTED" ? (
          "—"
        ) : (
          <button
            type="button"
            className="gc-button-text"
            data-testid={`project-delete-${row.projectId}`}
            aria-label={`Shut down ${row.projectId}`}
            onClick={() => setDeleting(row)}
          >
            Shut down
          </button>
        ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<CrmProject>
        title="Manage resources"
        description="Projects the signed-in principal can see, across their full lifecycle. Shutting a project down schedules it for deletion after a 30-day pending period."
        columns={columns}
        queryKey={ALL_PROJECTS_QUERY_KEY}
        queryFn={() => searchProjects()}
        filterPlaceholder="Filter projects"
        resourceNoun="projects"
        empty={{
          headline: "No projects to manage",
          description: "Projects you create appear here with their lifecycle state and deletion controls.",
          primaryLabel: "Create project",
        }}
        rowKey={(row) => row.projectId}
      />
      {deleting ? (
        <DeleteProjectDialog
          project={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: [PROJECTS_QUERY_PREFIX] });
          }}
        />
      ) : null}
    </>
  );
}
