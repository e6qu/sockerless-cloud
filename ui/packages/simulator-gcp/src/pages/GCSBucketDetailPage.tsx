import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copyable, GcpPageHeader } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { DetailList, LoadError, Loading } from "../console/GcpResourceDetail.js";
import { ResourceTableView, type GcpColumn } from "../console/GcpTable.js";
import { Icon } from "../console/icons.js";
import { shortName, formatTimestamp, formatBytes } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProjectPath } from "../console/project.js";
import {
  deleteGCSObject,
  downloadGCSObject,
  fetchBucketIamPolicy,
  fetchGCSBucket,
  fetchGCSObjectLevel,
  uploadGCSObject,
  type GCSBucket,
  type GCSObject,
} from "../api.js";
import {
  DeleteBucketDialog,
  EditBucketDialog,
  accessControlLabel,
  locationTypeLabel,
  publicAccessLabel,
  storageClassLabel,
} from "./GCSBucketsPage.js";
import { flattenBindings } from "./IamPage.js";

// A row of the Objects tab: an object, or a folder (a prefix the "/"
// delimiter groups objects under).
type ObjectRow = { kind: "folder"; prefix: string } | { kind: "object"; object: GCSObject };

const rowName = (row: ObjectRow, prefix: string): string =>
  (row.kind === "folder" ? row.prefix : row.object.name).slice(prefix.length);

function ObjectsView({
  rows,
  prefix,
  isLoading,
  error,
  onOpenFolder,
  selected,
  onSelectedChange,
}: {
  rows: ObjectRow[] | undefined;
  prefix: string;
  isLoading: boolean;
  error: unknown;
  onOpenFolder: (prefix: string) => void;
  selected: Set<string>;
  onSelectedChange: (selected: Set<string>) => void;
}) {
  const columns: GcpColumn<ObjectRow>[] = [
    {
      id: "name",
      header: "Name",
      cell: (row) =>
        row.kind === "folder" ? (
          <button type="button" className="gc-link-button" onClick={() => onOpenFolder(row.prefix)}>
            <Icon name="folder" size="1.25em" />
            {rowName(row, prefix)}
          </button>
        ) : (
          <span className="gc-object-name">
            <Icon name="description" size="1.25em" />
            {rowName(row, prefix)}
          </span>
        ),
      value: (row) => rowName(row, prefix),
    },
    {
      id: "size",
      header: "Size",
      cell: (row) => (row.kind === "object" ? formatBytes(row.object.size) : "—"),
      value: (row) => (row.kind === "object" ? (row.object.size ?? "") : ""),
    },
    {
      id: "type",
      header: "Type",
      cell: (row) => (row.kind === "object" ? (row.object.contentType ?? "—") : "Folder"),
      value: (row) => (row.kind === "object" ? (row.object.contentType ?? "") : "Folder"),
    },
    {
      id: "created",
      header: "Created",
      cell: (row) => (row.kind === "object" ? formatTimestamp(row.object.timeCreated ?? "") : "—"),
      value: (row) => (row.kind === "object" ? (row.object.timeCreated ?? "") : ""),
    },
    {
      id: "storageClass",
      header: "Storage class",
      cell: (row) => (row.kind === "object" ? storageClassLabel(row.object.storageClass) : "—"),
      value: (row) => (row.kind === "object" ? (row.object.storageClass ?? "") : ""),
    },
    {
      id: "updated",
      header: "Last modified",
      cell: (row) => (row.kind === "object" ? formatTimestamp(row.object.updated ?? "") : "—"),
      value: (row) => (row.kind === "object" ? (row.object.updated ?? "") : ""),
      defaultHidden: true,
    },
  ];
  return (
    <ResourceTableView<ObjectRow>
      rows={rows}
      isLoading={isLoading}
      error={error}
      columns={columns}
      rowKey={(row) => (row.kind === "folder" ? row.prefix : row.object.name)}
      rowLabel={(row) => rowName(row, prefix)}
      resourceNoun="objects"
      tableId="gcs-objects-table"
      selectable
      selected={selected}
      onSelectedChange={onSelectedChange}
      empty={{ headline: "No rows to display", description: "Upload files to add objects to this bucket." }}
    />
  );
}

function DeleteObjectsDialog({
  bucket,
  names,
  onClose,
  onDeleted,
}: {
  bucket: string;
  names: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { track } = useOperations();
  const subject = names.length === 1 ? `object "${names[0]}"` : `${names.length} objects`;
  const remove = useMutation({
    mutationFn: () =>
      track(`Delete ${subject}`, () => Promise.all(names.map((name) => deleteGCSObject(bucket, name))), `Deleted ${subject}`),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="gcs-object-delete-dialog" onClose={onClose}>
      <p>Deleted objects can't be recovered unless the bucket's soft delete policy retains them.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary gc-button-danger"
          data-testid="gcs-object-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

function CreateFolderDialog({ onClose, onCreate }: { onClose: () => void; onCreate: (name: string) => void }) {
  const [name, setName] = useState("");
  const valid = name.trim() !== "" && !name.includes("/");
  return (
    <GcpDialog title="Create folder" testId="gcs-folder-dialog" onClose={onClose}>
      <label className="gc-field">
        <span className="gc-field-label">Name</span>
        <input type="text" value={name} data-testid="gcs-folder-name" onChange={(event) => setName(event.target.value)} />
      </label>
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="gc-button-primary" disabled={!valid} onClick={() => onCreate(name.trim())}>
          Create
        </button>
      </div>
    </GcpDialog>
  );
}

function ObjectsTab({ bucket }: { bucket: string }) {
  const path = useProjectPath();
  const [prefix, setPrefix] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState<string[] | null>(null);
  const [folder, setFolder] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const level = useQuery({
    queryKey: ["gcs-bucket-objects", bucket, prefix],
    queryFn: () => fetchGCSObjectLevel(bucket, prefix),
  });
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["gcs-bucket-objects", bucket] });
  const rows: ObjectRow[] | undefined = level.data
    ? [
        ...level.data.prefixes.map((candidate) => ({ kind: "folder" as const, prefix: candidate })),
        ...level.data.objects.map((object) => ({ kind: "object" as const, object })),
      ]
    : undefined;
  const selectedObjects = (level.data?.objects ?? []).filter((object) => selected.has(object.name));

  const upload = useMutation({
    mutationFn: (files: File[]) => {
      const subject = files.length === 1 ? `"${files[0].name}"` : `${files.length} files`;
      return track(
        `Upload ${subject} to ${bucket}`,
        () => Promise.all(files.map((file) => uploadGCSObject(bucket, `${prefix}${file.name}`, file))),
        `Uploaded ${subject}`,
      );
    },
    onSuccess: refresh,
  });
  const createFolder = useMutation({
    mutationFn: (name: string) =>
      track(`Create folder "${name}"`, () => uploadGCSObject(bucket, `${prefix}${name}/`, new Blob([])), `Created folder "${name}"`),
    onSuccess: refresh,
  });
  const download = (object: GCSObject) =>
    void track(`Download "${object.name}"`, async () => {
      const blob = await downloadGCSObject(bucket, object.name);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = shortName(object.name);
      anchor.click();
      URL.revokeObjectURL(url);
    });

  const segments = prefix.split("/").filter(Boolean);
  return (
    <>
      <nav className="gc-object-path" aria-label="Folder path">
        <Link to={path("/ui/storage/browser")}>Buckets</Link>
        {" › "}
        <button type="button" className="gc-link-button" onClick={() => setPrefix("")}>
          {bucket}
        </button>
        {segments.map((segment, index) => (
          <span key={`${segment}-${index}`}>
            {" › "}
            <button
              type="button"
              className="gc-link-button"
              onClick={() => setPrefix(`${segments.slice(0, index + 1).join("/")}/`)}
            >
              {segment}
            </button>
          </span>
        ))}
      </nav>
      <div className="gc-toolbar">
        <button type="button" className="gc-action" data-testid="gcs-upload" onClick={() => fileInput.current?.click()}>
          <Icon name="upload" size="1.25em" />
          Upload files
        </button>
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          data-testid="gcs-upload-input"
          aria-label="Files to upload"
          onChange={(event) => {
            const files = Array.from(event.target.files ?? []);
            event.target.value = "";
            if (files.length > 0) upload.mutate(files);
          }}
        />
        <button type="button" className="gc-action" onClick={() => setFolder(true)}>
          <Icon name="folder" size="1.25em" />
          Create folder
        </button>
        <button
          type="button"
          className="gc-action"
          disabled={selectedObjects.length !== 1}
          onClick={() => download(selectedObjects[0])}
        >
          <Icon name="download" size="1.25em" />
          Download
        </button>
        <button
          type="button"
          className="gc-action"
          data-testid="gcs-object-delete"
          disabled={selectedObjects.length === 0}
          onClick={() => setDeleting(selectedObjects.map((object) => object.name))}
        >
          <Icon name="delete" size="1.25em" />
          Delete
        </button>
      </div>
      {upload.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't upload.</strong> {upload.error instanceof Error ? upload.error.message : String(upload.error)}
        </div>
      ) : null}
      <ObjectsView
        rows={rows}
        prefix={prefix}
        isLoading={level.isLoading}
        error={level.error}
        onOpenFolder={(next) => {
          setPrefix(next);
          setSelected(new Set());
        }}
        selected={selected}
        onSelectedChange={setSelected}
      />
      {deleting ? (
        <DeleteObjectsDialog
          bucket={bucket}
          names={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            setSelected(new Set());
            refresh();
          }}
        />
      ) : null}
      {folder ? (
        <CreateFolderDialog
          onClose={() => setFolder(false)}
          onCreate={(name) => {
            setFolder(false);
            createFolder.mutate(name);
          }}
        />
      ) : null}
    </>
  );
}

function ConfigurationTab({ bucket }: { bucket: GCSBucket }) {
  const name = shortName(bucket.name);
  const retention = Number(bucket.softDeletePolicy?.retentionDurationSeconds ?? 0);
  return (
    <>
      <h2 className="gc-detail-heading">Overview</h2>
      <DetailList
        properties={[
          { label: "gsutil URI", value: <Copyable value={`gs://${name}`} /> },
          { label: "Created", value: formatTimestamp(bucket.timeCreated ?? "") },
          { label: "Updated", value: formatTimestamp(bucket.updated ?? "") },
          { label: "Location type", value: locationTypeLabel(bucket.locationType) },
          { label: "Location", value: (bucket.location ?? "—").toLowerCase() },
          { label: "Default storage class", value: storageClassLabel(bucket.storageClass) },
          {
            label: "Labels",
            value: Object.entries(bucket.labels ?? {}).length
              ? Object.entries(bucket.labels ?? {}).map(([key, value]) => (
                  <span key={key} className="gc-label-chip">
                    {key}: {value}
                  </span>
                ))
              : "None",
          },
        ]}
      />
      <h2 className="gc-detail-heading">Permissions</h2>
      <DetailList
        properties={[
          { label: "Public access", value: publicAccessLabel(bucket) },
          { label: "Access control", value: accessControlLabel(bucket) },
        ]}
      />
      <h2 className="gc-detail-heading">Protection</h2>
      <DetailList
        properties={[
          { label: "Soft delete policy", value: retention ? `${Math.round(retention / 86400)} days` : "None" },
          { label: "Object versioning", value: bucket.versioning?.enabled ? "On" : "Off" },
        ]}
      />
    </>
  );
}

function PermissionsTab({ bucket }: { bucket: string }) {
  const policy = useQuery({ queryKey: ["gcs-bucket-iam", bucket], queryFn: () => fetchBucketIamPolicy(bucket) });
  return (
    <ResourceTableView
      rows={policy.data ? flattenBindings(policy.data) : undefined}
      isLoading={policy.isLoading}
      error={policy.error}
      tableId="gcs-permissions-table"
      resourceNoun="principals"
      rowKey={(row) => `${row.member} ${row.role}`}
      columns={[
        { id: "principal", header: "Principal", cell: (row) => row.member, value: (row) => row.member },
        { id: "role", header: "Role", cell: (row) => row.role, value: (row) => row.role },
      ]}
      empty={{ headline: "No principals", description: "Principals granted roles on this bucket appear here." }}
    />
  );
}

export function GCSBucketDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const path = useProjectPath();
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const bucket = useQuery({ queryKey: ["gcs-bucket", name], queryFn: () => fetchGCSBucket(name) });
  const data = bucket.data;

  return (
    <>
      <GcpPageHeader
        backTo="/ui/storage/browser"
        title="Bucket details"
        actions={[
          ...(data ? [{ label: "Edit", icon: "edit" as const, testId: "gcs-bucket-edit", onSelect: () => setEditing(true) }] : []),
          { label: "Delete", icon: "delete", testId: "gcs-bucket-delete", onSelect: () => setDeleting(true) },
        ]}
        onRefresh={() => void bucket.refetch()}
        refreshing={bucket.isFetching}
      />
      <p className="gc-resource-title">
        <Icon name="storage" size="1.25em" />
        <Copyable value={name} />
      </p>
      {bucket.isError ? (
        <LoadError error={bucket.error} noun="this bucket" />
      ) : bucket.isLoading || !data ? (
        <Loading noun="bucket" />
      ) : (
        <GcpTabs
          label="Bucket details"
          tabs={[
            { id: "objects", label: "Objects", content: <ObjectsTab bucket={name} /> },
            { id: "configuration", label: "Configuration", content: <ConfigurationTab bucket={data} /> },
            { id: "permissions", label: "Permissions", content: <PermissionsTab bucket={name} /> },
          ]}
        />
      )}
      {deleting ? (
        <DeleteBucketDialog
          names={[name]}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate(path("/ui/storage/browser"))}
        />
      ) : null}
      {editing && data ? (
        <EditBucketDialog
          bucket={data}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void bucket.refetch();
          }}
        />
      ) : null}
    </>
  );
}
