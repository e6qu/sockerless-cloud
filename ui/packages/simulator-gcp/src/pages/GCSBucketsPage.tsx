import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { LabelsEditor, labelsToPairs, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import { deleteGCSBucket, fetchGCSBuckets, updateGCSBucket, type GCSBucket } from "../api.js";

// Cloud Storage's storage classes, in the order the console offers them.
export const STORAGE_CLASSES = ["STANDARD", "NEARLINE", "COLDLINE", "ARCHIVE"] as const;

export function storageClassLabel(storageClass: string | undefined): string {
  return storageClass ? storageClass.charAt(0) + storageClass.slice(1).toLowerCase() : "—";
}

export function locationTypeLabel(locationType: string | undefined): string {
  switch (locationType) {
    case "region":
      return "Region";
    case "dual-region":
      return "Dual-region";
    case "multi-region":
      return "Multi-region";
    default:
      return "—";
  }
}

export function publicAccessLabel(bucket: GCSBucket): string {
  return bucket.iamConfiguration?.publicAccessPrevention === "enforced" ? "Not public" : "Subject to object ACLs";
}

export function accessControlLabel(bucket: GCSBucket): string {
  return bucket.iamConfiguration?.uniformBucketLevelAccess?.enabled ? "Uniform" : "Fine-grained";
}

// storage.buckets.patch deep-merges `labels`, and a null value removes a key,
// so a key the operator dropped is sent as null rather than omitted.
function labelsDiff(
  original: Record<string, string> = {},
  next: Record<string, string>,
): Record<string, string | null> {
  const patch: Record<string, string | null> = { ...next };
  for (const key of Object.keys(original)) {
    if (!(key in next)) patch[key] = null;
  }
  return patch;
}

// EditBucketDialog edits the default storage class and the labels through
// storage.buckets.patch.
export function EditBucketDialog({
  bucket,
  onClose,
  onSaved,
}: {
  bucket: GCSBucket;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [storageClass, setStorageClass] = useState(bucket.storageClass ?? "STANDARD");
  const [pairs, setPairs] = useState<LabelPair[]>(labelsToPairs(bucket.labels));
  const { track } = useOperations();
  const name = shortName(bucket.name);

  const save = useMutation({
    mutationFn: () =>
      track(
        `Update bucket "${name}"`,
        () => updateGCSBucket(name, { storageClass, labels: labelsDiff(bucket.labels, pairsToLabels(pairs)) }),
        `Updated bucket "${name}"`,
      ),
    onSuccess: onSaved,
  });

  return (
    <GcpDialog title="Edit bucket" testId="gcs-edit-dialog" onClose={onClose}>
      <label className="gc-field">
        <span className="gc-field-label">Default storage class</span>
        <select value={storageClass} data-testid="gcs-edit-storage-class" onChange={(event) => setStorageClass(event.target.value)}>
          {STORAGE_CLASSES.map((candidate) => (
            <option key={candidate} value={candidate}>
              {storageClassLabel(candidate)}
            </option>
          ))}
        </select>
      </label>
      <LabelsEditor pairs={pairs} onChange={setPairs} idPrefix="gcs-edit" />
      {save.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't save the bucket.</strong>{" "}
          {save.error instanceof Error ? save.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="gc-button-primary" data-testid="gcs-edit-submit" disabled={save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

// DeleteBucketDialog deletes one or more buckets through storage.buckets.delete
// after the operator types DELETE, the confirmation the real console asks for.
export function DeleteBucketDialog({
  names,
  onClose,
  onDeleted,
}: {
  names: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [confirmation, setConfirmation] = useState("");
  const { track } = useOperations();
  const subject = names.length === 1 ? `bucket "${names[0]}"` : `${names.length} buckets`;
  const remove = useMutation({
    mutationFn: () =>
      track(`Delete ${subject}`, () => Promise.all(names.map((name) => deleteGCSBucket(name))), `Deleted ${subject}`),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={names.length === 1 ? `Delete bucket?` : `Delete ${names.length} buckets?`} testId="gcs-delete-dialog" onClose={onClose}>
      <p>
        {names.length === 1 ? <strong>{names[0]}</strong> : names.join(", ")} will be deleted permanently. A bucket that still
        holds objects can't be deleted.
      </p>
      <label className="gc-field">
        <span className="gc-field-label">To confirm deletion, type DELETE</span>
        <input type="text" value={confirmation} data-testid="gcs-delete-confirmation" onChange={(event) => setConfirmation(event.target.value)} />
      </label>
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
          data-testid="gcs-delete-confirm"
          disabled={confirmation !== "DELETE" || remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

function BucketName({ bucket }: { bucket: GCSBucket }) {
  const path = useProjectPath();
  const name = shortName(bucket.name);
  return (
    <Link className="gc-cell-link" to={path(`/ui/storage/browser/${name}`)}>
      {name}
    </Link>
  );
}

const columns: GcpColumn<GCSBucket>[] = [
  { id: "name", header: "Name", cell: (row) => <BucketName bucket={row} />, value: (row) => shortName(row.name) },
  { id: "created", header: "Created", cell: (row) => formatTimestamp(row.timeCreated ?? ""), value: (row) => row.timeCreated ?? "" },
  {
    id: "locationType",
    header: "Location type",
    cell: (row) => locationTypeLabel(row.locationType),
    value: (row) => locationTypeLabel(row.locationType),
  },
  { id: "location", header: "Location", cell: (row) => (row.location ?? "—").toLowerCase(), value: (row) => (row.location ?? "").toLowerCase() },
  {
    id: "storageClass",
    header: "Default storage class",
    cell: (row) => storageClassLabel(row.storageClass),
    value: (row) => storageClassLabel(row.storageClass),
  },
  { id: "updated", header: "Last modified", cell: (row) => formatTimestamp(row.updated ?? ""), value: (row) => row.updated ?? "" },
  { id: "publicAccess", header: "Public access", cell: publicAccessLabel, value: publicAccessLabel },
  { id: "accessControl", header: "Access control", cell: accessControlLabel, value: accessControlLabel },
  {
    id: "labels",
    header: "Labels",
    cell: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}: ${value}`).join(", ") || "—",
    value: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}:${value}`).join(" "),
    defaultHidden: true,
  },
];

export function GCSBucketsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string[] | null>(null);
  return (
    <>
      <GcpResourceTable<GCSBucket>
        title="Buckets"
        actions={[{ label: "Create", icon: "add", to: "/ui/storage/create-bucket", testId: "gcs-create-bucket" }]}
        selectionActions={(selected) => [
          { label: "Delete", icon: "delete", testId: "gcs-delete-selected", onSelect: () => setDeleting(selected.map((row) => shortName(row.name))) },
        ]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting([shortName(row.name)]) }]}
        columns={columns}
        queryKey={["gcs-buckets-real", project]}
        queryFn={() => fetchGCSBuckets(project)}
        resourceNoun="buckets"
        testId="gcs-buckets-table"
        empty={{
          headline: "No buckets",
          description: "Buckets hold your objects: durable, scalable storage for any amount of data.",
        }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteBucketDialog
          names={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["gcs-buckets-real", project] });
          }}
        />
      ) : null}
    </>
  );
}
