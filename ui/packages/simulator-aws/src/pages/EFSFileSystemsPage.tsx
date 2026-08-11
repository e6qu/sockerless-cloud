import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatBytes, formatEpoch } from "../console/format.js";
import { createEFSFileSystem, deleteEFSFileSystem, fetchEFSFileSystems, type EFSFileSystem } from "../api.js";

// Amazon Elastic File System (EFS) — File systems. DescribeFileSystems for the
// table, CreateFileSystem and DeleteFileSystem for the header actions, all on
// the real EFS REST-JSON API.

const columns: AwsColumn<EFSFileSystem>[] = [
  { id: "name", header: "Name", cell: (row) => row.name || "–", value: (row) => row.name },
  {
    id: "fileSystemId",
    header: "File system ID",
    cell: (row) => <AwsRowLink to={`/ui/efs/${encodeURIComponent(row.fileSystemId)}`}>{row.fileSystemId}</AwsRowLink>,
    value: (row) => row.fileSystemId,
  },
  {
    id: "lifeCycleState",
    header: "State",
    cell: (row) => <AwsStatus status={row.lifeCycleState} />,
    value: (row) => row.lifeCycleState,
  },
  {
    id: "sizeInBytes",
    header: "Size in EFS Standard",
    cell: (row) => formatBytes(row.sizeInBytes),
    value: (row) => String(row.sizeInBytes),
  },
  {
    id: "numberOfMountTargets",
    header: "Mount targets",
    cell: (row) => String(row.numberOfMountTargets),
    value: (row) => String(row.numberOfMountTargets),
  },
  {
    id: "performanceMode",
    header: "Performance mode",
    cell: (row) => row.performanceMode,
    value: (row) => row.performanceMode,
  },
  {
    id: "encrypted",
    header: "Encrypted",
    cell: (row) => (row.encrypted ? "Yes" : "No"),
    value: (row) => (row.encrypted ? "Yes" : "No"),
  },
  {
    id: "creationTime",
    header: "Created",
    cell: (row) => formatEpoch(row.creationTime),
    value: (row) => String(row.creationTime),
  },
];

function CreateFileSystemModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => createEFSFileSystem(name.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["efs-file-systems"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create file system"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="efs-create-file-system-submit"
            disabled={name.trim().length === 0 || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create"}
          </AwsButton>
        </>
      }
    >
      <p>A file system stores files that many compute resources can mount at once. Its name is stored as a Name tag.</p>
      <FormField label="Name" constraintText="Stored as the file system's Name tag.">
        <Input
          value={name}
          onChange={(event) => setName(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "efs-file-system-name-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the file system.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function DeleteFileSystemsModal({
  fileSystems,
  onClose,
  clearSelection,
}: {
  fileSystems: EFSFileSystem[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const fileSystem of fileSystems) {
        await deleteEFSFileSystem(fileSystem.fileSystemId);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["efs-file-systems"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={fileSystems.length === 1 ? `Delete ${fileSystems[0].fileSystemId}?` : `Delete ${fileSystems.length} file systems?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="efs-delete-file-system-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a file system is permanent and deletes every file it holds. A file system with mount targets must have them deleted first.</p>
      <ul>
        {fileSystems.map((fileSystem) => (
          <li key={fileSystem.fileSystemId}>
            <code>{fileSystem.fileSystemId}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function EFSFileSystemsPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ fileSystems: EFSFileSystem[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<EFSFileSystem>
        title="File systems"
        description="Amazon EFS file systems in this account and Region."
        columns={columns}
        queryKey={["efs-file-systems"]}
        queryFn={fetchEFSFileSystems}
        filterPlaceholder="Find file systems"
        emptyTitle="No file systems"
        emptyDescription="No Amazon EFS file systems exist in this account and Region."
        rowKey={(row) => row.fileSystemId}
        tableTestId="efs-table"
        errorTestId="efs-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="efs-view-file-system"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/efs/${encodeURIComponent(selected[0].fileSystemId)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="efs-delete-file-system"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ fileSystems: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="efs-create-file-system" onClick={() => setCreating(true)}>
              Create file system
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateFileSystemModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteFileSystemsModal
          fileSystems={deleting.fileSystems}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
