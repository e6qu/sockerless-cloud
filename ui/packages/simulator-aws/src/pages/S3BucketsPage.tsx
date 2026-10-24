import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Button from "@cloudscape-design/components/button";
import Box from "@cloudscape-design/components/box";
import CopyToClipboard from "@cloudscape-design/components/copy-to-clipboard";
import {
  AwsButton,
  AwsDeleteModal,
  AwsErrorAlert,
  AwsRefreshButton,
  AwsResourceTable,
  AwsRowLink,
  errorText,
  useNotify,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import { regionName } from "../console/regions.js";
import { deleteS3Bucket, fetchS3Buckets, type S3Bucket } from "../api.js";

// Amazon S3 — General purpose buckets: ListBuckets for the table and
// DeleteBucket for Delete. DeleteBucket only succeeds on an empty bucket; the
// console surfaces S3's BucketNotEmpty error rather than emptying it on the
// operator's behalf, the same restriction the real console enforces.

const columns: AwsColumn<S3Bucket>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <AwsRowLink to={`/ui/s3/${encodeURIComponent(row.name)}`}>{row.name}</AwsRowLink>,
    value: (row) => row.name,
  },
  {
    id: "region",
    header: "AWS Region",
    cell: (row) => (row.region ? `${regionName(row.region)} ${row.region}` : "–"),
    value: (row) => row.region,
  },
  { id: "creationDate", header: "Creation date", cell: (row) => formatTimestamp(row.creationDate), value: (row) => row.creationDate },
];

// The DNS-compliant name shape real S3 enforces on CreateBucket: 3–63
// characters, lowercase letters, digits, dots, and hyphens, starting and
// ending with a letter or digit, no adjacent dots, and never formatted as an
// IPv4 address.
const IPV4_PATTERN = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
export function isValidS3BucketName(name: string): boolean {
  if (name.length < 3 || name.length > 63) return false;
  if (!/^[a-z0-9][a-z0-9.-]*[a-z0-9]$/.test(name)) return false;
  if (name.includes("..")) return false;
  if (IPV4_PATTERN.test(name)) return false;
  return true;
}

export function DeleteBucketModal({ bucket, onClose, onDeleted }: { bucket: S3Bucket; onClose: () => void; onDeleted: () => void }) {
  const queryClient = useQueryClient();
  const notify = useNotify();
  const remove = useMutation({
    mutationFn: () => deleteS3Bucket(bucket.name),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["s3-buckets"] }),
    onSuccess: () => {
      notify("success", `Successfully deleted bucket "${bucket.name}".`);
      onDeleted();
    },
  });
  return (
    <AwsDeleteModal
      title="Delete bucket"
      confirmationText={bucket.name}
      onDismiss={onClose}
      onDelete={() => remove.mutate()}
      pending={remove.isPending}
      testIdPrefix="s3-delete-bucket"
      error={
        remove.isError ? (
          <AwsErrorAlert>
            <strong>Failed to delete bucket.</strong> {errorText(remove.error)}
          </AwsErrorAlert>
        ) : undefined
      }
    >
      <Box>
        Deleting bucket <strong>{bucket.name}</strong> is permanent. S3 refuses to delete a bucket that still holds objects —
        empty it first.
      </Box>
    </AwsDeleteModal>
  );
}

export function S3BucketsPage() {
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState<{ bucket: S3Bucket; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<S3Bucket>
        title="General purpose buckets"
        description="Buckets are containers for data stored in S3."
        columns={columns}
        queryKey={["s3-buckets"]}
        queryFn={fetchS3Buckets}
        filterPlaceholder="Find buckets by name"
        emptyTitle="No buckets"
        emptyDescription="You don't have any buckets."
        emptyAction={<Button onClick={() => navigate("/ui/s3/create")}>Create bucket</Button>}
        selectionType="single"
        rowKey={(row) => row.name}
        tableTestId="s3-buckets-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <CopyToClipboard
              variant="button"
              textToCopy={selected[0] ? `arn:aws:s3:::${selected[0].name}` : ""}
              copyButtonText="Copy ARN"
              copySuccessText="ARN copied"
              copyErrorText="ARN failed to copy"
              disabled={selected.length !== 1}
            />
            <AwsButton
              data-testid="s3-delete-bucket"
              disabled={selected.length !== 1}
              onClick={() => setDeleting({ bucket: selected[0], clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="s3-create-bucket" onClick={() => navigate("/ui/s3/create")}>
              Create bucket
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <DeleteBucketModal
          bucket={deleting.bucket}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            deleting.clearSelection();
            setDeleting(null);
          }}
        />
      )}
    </>
  );
}
