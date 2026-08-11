import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsRowLink, type AwsColumn } from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import { createS3Bucket, deleteS3Bucket, fetchS3Buckets, type S3Bucket } from "../api.js";

// Amazon Simple Storage Service (S3) — Buckets. The list and Delete both go
// through the real S3 REST-XML API (GET / for the table, DELETE /{bucket} for
// the header action) with the operator's federated credentials. DeleteBucket
// only succeeds on an empty bucket — the console surfaces S3's BucketNotEmpty
// error rather than emptying it on the operator's behalf, the same
// restriction the real console enforces.

const columns: AwsColumn<S3Bucket>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <AwsRowLink to={`/ui/s3/${encodeURIComponent(row.name)}`}>{row.name}</AwsRowLink>,
    value: (row) => row.name,
  },
  { id: "creationDate", header: "Creation date", cell: (row) => formatTimestamp(row.creationDate), value: (row) => row.creationDate },
];

// The DNS-compliant name shape real S3 enforces on CreateBucket: 3–63
// characters, lowercase letters, digits, dots, and hyphens, starting and
// ending with a letter or digit, no adjacent dots, and never formatted as an
// IPv4 address — validating it inline is what the real console does before
// it lets the request go out.
const IPV4_PATTERN = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
export function isValidS3BucketName(name: string): boolean {
  if (name.length < 3 || name.length > 63) return false;
  if (!/^[a-z0-9][a-z0-9.-]*[a-z0-9]$/.test(name)) return false;
  if (name.includes("..")) return false;
  if (IPV4_PATTERN.test(name)) return false;
  return true;
}

function CreateBucketModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: createS3Bucket,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["s3-buckets"] });
      onClose();
    },
  });
  const trimmed = name.trim();
  const valid = isValidS3BucketName(trimmed);
  return (
    <AwsModal
      title="Create bucket"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="s3-create-bucket-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate(trimmed)}
          >
            {create.isPending ? "Creating…" : "Create bucket"}
          </AwsButton>
        </>
      }
    >
      <p>General purpose buckets are for a broad set of storage use cases. Bucket names are unique across all of S3.</p>
      <FormField
        label="Bucket name"
        constraintText="3–63 characters. Lowercase letters, numbers, dots, and hyphens. Must start and end with a letter or number."
      >
        <Input
          value={name}
          onChange={(event) => setName(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "s3-bucket-name-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the bucket.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function DeleteBucketsModal({
  buckets,
  onClose,
  clearSelection,
}: {
  buckets: S3Bucket[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    // DeleteBucket is per-bucket on the wire; a non-empty bucket fails with
    // BucketNotEmpty, surfaced as the real API error, with the already-empty
    // buckets among the selection gone from the refreshed list.
    mutationFn: async () => {
      for (const bucket of buckets) {
        await deleteS3Bucket(bucket.name);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["s3-buckets"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={buckets.length === 1 ? `Delete ${buckets[0].name}?` : `Delete ${buckets.length} buckets?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="s3-delete-bucket-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a bucket is permanent. S3 refuses to delete a bucket that still holds objects — empty it first.</p>
      <ul>
        {buckets.map((bucket) => (
          <li key={bucket.name}>
            <code>{bucket.name}</code>
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

export function S3BucketsPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ buckets: S3Bucket[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<S3Bucket>
        title="Buckets"
        description="General purpose buckets in this account."
        columns={columns}
        queryKey={["s3-buckets"]}
        queryFn={fetchS3Buckets}
        filterPlaceholder="Find buckets"
        emptyTitle="No buckets"
        emptyDescription="No buckets exist in this account."
        rowKey={(row) => row.name}
        tableTestId="s3-buckets-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="s3-view-bucket"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/s3/${encodeURIComponent(selected[0].name)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="s3-delete-bucket"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ buckets: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="s3-create-bucket" onClick={() => setCreating(true)}>
              Create bucket
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateBucketModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteBucketsModal
          buckets={deleting.buckets}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
