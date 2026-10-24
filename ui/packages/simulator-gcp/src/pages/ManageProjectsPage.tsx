import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { PROJECTS_QUERY_PREFIX } from "../console/ProjectPicker.js";
import { formatTimestamp } from "../console/format.js";
import { Icon } from "../console/icons.js";
import { useOperations } from "../console/notifications.js";
import { useProject } from "../console/project.js";
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
  const { track } = useOperations();
  const remove = useMutation({
    mutationFn: () =>
      track(
        `Shut down project "${project.projectId}"`,
        async () => waitCrmOperation(await deleteProject(project.projectId)),
        `Shut down project "${project.projectId}"`,
      ),
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
  const { setProject } = useProject();
  const [deleting, setDeleting] = useState<CrmProject | null>(null);

  const columns: GcpColumn<CrmProject>[] = [
    {
      id: "name",
      header: "Name",
      cell: (row) =>
        row.state === "ACTIVE" ? (
          <button
            type="button"
            className="gc-link-button"
            data-testid={`manage-project-row-${row.projectId}`}
            onClick={() => setProject(row.projectId, "/ui/home/dashboard")}
          >
            <Icon name="account_tree" size="1.15em" />
            {row.displayName || row.projectId}
          </button>
        ) : (
          <span data-testid={`manage-project-row-${row.projectId}`}>{row.displayName || row.projectId}</span>
        ),
      value: (row) => row.displayName || row.projectId,
    },
    { id: "id", header: "ID", cell: (row) => row.projectId, value: (row) => row.projectId },
    { id: "number", header: "Number", cell: crmProjectNumber, value: crmProjectNumber, defaultHidden: true },
    {
      id: "state",
      header: "Status",
      cell: (row) =>
        row.state === "DELETE_REQUESTED" ? (
          <GcpStatus status="Pending deletion" kind="warning" />
        ) : (
          <GcpStatus status={row.state ?? "Unknown"} label={row.state === "ACTIVE" ? "Active" : (row.state ?? "Unknown")} />
        ),
      value: (row) => row.state ?? "",
    },
    { id: "created", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
  ];

  return (
    <>
      <GcpResourceTable<CrmProject>
        title="Manage resources"
        actions={[{ label: "Create project", icon: "add", to: "/ui/projectcreate", testId: "project-create" }]}
        rowActions={(row) => [
          {
            label: "Delete",
            disabled: row.state !== "ACTIVE",
            onSelect: () => setDeleting(row),
          },
        ]}
        columns={columns}
        queryKey={ALL_PROJECTS_QUERY_KEY}
        queryFn={() => searchProjects()}
        resourceNoun="projects"
        testId="manage-projects-table"
        empty={{ headline: "No projects", description: "Projects you create appear here with their lifecycle state." }}
        rowKey={(row) => row.projectId}
        rowLabel={(row) => row.projectId}
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
