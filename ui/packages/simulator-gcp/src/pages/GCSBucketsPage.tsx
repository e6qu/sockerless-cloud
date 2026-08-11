import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { LabelsEditor, labelsToPairs, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { createGCSBucket, deleteGCSBucket, fetchGCSBuckets, updateGCSBucket, type GCSBucket } from "../api.js";
import { useProject } from "../console/project.js";

// Cloud Storage's default storage classes, as the Edit bucket page offers them.
const STORAGE_CLASSES = ["STANDARD", "NEARLINE", "COLDLINE", "ARCHIVE"] as const;

// labelsDiff turns an edited labels map into the storage.buckets.patch body:
// GCS deep-merges `labels`, and a null value removes a key, so a key the
// operator dropped must be sent as null rather than simply omitted.
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

// EditBucketDialog edits the fields the real Edit bucket page edits — the
// default storage class and the bucket's labels — through the real
// storage.buckets.patch operation. Shared by the detail page's Edit action.
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

  const save = useMutation({
    mutationFn: () =>
      updateGCSBucket(shortName(bucket.name), {
        storageClass,
        labels: labelsDiff(bucket.labels, pairsToLabels(pairs)),
      }),
    onSuccess: onSaved,
  });

  return (
    <GcpDialog title="Edit bucket" testId="gcs-edit-dialog" onClose={onClose}>
      <label className="gc-field">
        Default storage class
        <select
          value={storageClass}
          data-testid="gcs-edit-storage-class"
          onChange={(event) => setStorageClass(event.target.value)}
        >
          {STORAGE_CLASSES.map((candidate) => (
            <option key={candidate} value={candidate}>
              {candidate}
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
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="gcs-edit-submit"
          disabled={save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </button>
      </div>
    </GcpDialog>
  );
}

const columns: GcpColumn<GCSBucket>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link className="gc-cell-link" to={`/ui/gcs/${shortName(row.name)}`}>{shortName(row.name)}</Link>,
    value: (row) => shortName(row.name),
  },
  { id: "location", header: "Location", cell: (row) => row.location ?? "—", value: (row) => row.location ?? "" },
  { id: "storageClass", header: "Storage class", cell: (row) => row.storageClass ?? "—", value: (row) => row.storageClass ?? "" },
  { id: "timeCreated", header: "Created", cell: (row) => formatTimestamp(row.timeCreated ?? ""), value: (row) => row.timeCreated ?? "" },
];

// DeleteBucketDialog is shared by the list's per-row action and the bucket
// detail page's header action — the same real storage.buckets.delete
// operation (DELETE /storage/v1/b/{bucket}, answered 204 No Content) either
// way.
export function DeleteBucketDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({
    mutationFn: () => deleteGCSBucket(name),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete bucket?" testId="gcs-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the bucket and every object it holds.
        This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the bucket.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="gcs-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

// Cloud Storage's bucket-name contract: 3–63 characters, lowercase letters,
// digits, hyphens, underscores and dots, starting and ending with a letter
// or digit — checked before submitting so the dialog explains the rule the
// way the real Create bucket page does.
const BUCKET_NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$/;

export function CreateBucketDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [name, setName] = useState("");

  const create = useMutation({
    mutationFn: () => createGCSBucket(project, name),
    onSuccess: onCreated,
  });

  const valid = BUCKET_NAME_PATTERN.test(name);

  return (
    <GcpDialog title="Create a bucket" testId="gcs-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Name your bucket
        <input
          type="text"
          value={name}
          data-testid="gcs-create-name"
          onChange={(event) => setName(event.target.value)}
        />
        <p className="gc-field-hint">
          3–63 characters. Lowercase letters, numbers, dashes (-), underscores (_) and dots (.); must start and
          end with a letter or number.
        </p>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the bucket.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="gcs-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function GCSBucketsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["gcs-buckets-real", project] });

  const columnsWithActions: GcpColumn<GCSBucket>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const name = shortName(row.name);
        return (
          <button
            type="button"
            className="gc-button-text"
            data-testid={`gcs-delete-${name}`}
            aria-label={`Delete ${name}`}
            onClick={() => setDeleting(name)}
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
      <GcpResourceTable<GCSBucket>
        title="Cloud Storage"
        description="Buckets hold your objects — durable, scalable storage for any amount of data."
        actions={[
          { label: "Create bucket", icon: "add", primary: true, testId: "gcs-create-bucket", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["gcs-buckets-real", project]}
        queryFn={() => fetchGCSBuckets(project)}
        filterPlaceholder="Filter buckets"
        resourceNoun="buckets"
        empty={{
          headline: "Store any amount of data",
          description: "Create a bucket to store and serve objects with Cloud Storage.",
          primaryLabel: "Create bucket",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateBucketDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteBucketDialog
          name={deleting}
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
