import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Toggle from "@cloudscape-design/components/toggle";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsModal,
  AwsPageHeader,
  AwsResourceTable,
  AwsStatus,
  TagsEditorModal,
  type AwsColumn,
} from "../console/index.js";
import { formatBytes, formatTimestamp } from "../console/format.js";
import {
  fetchS3BucketLocation,
  fetchS3BucketTagging,
  fetchS3BucketVersioning,
  fetchS3Buckets,
  fetchS3Objects,
  putS3BucketTagging,
  putS3BucketVersioning,
  type S3Bucket,
  type S3Object,
} from "../api.js";
import { DeleteBucketsModal } from "./S3BucketsPage.js";

// Amazon Simple Storage Service (S3) — Bucket detail. Reads the real S3
// REST-XML API (GetBucketLocation for the Region, GetBucketVersioning and
// GetBucketTagging for the properties, ListObjectsV2 for the object listing)
// with the operator's federated credentials, and drives the real update
// surface: PutBucketVersioning, PutBucketTagging, and DeleteBucket. S3 has no
// per-bucket "properties" read for the creation date — the real console reads
// it from the same ListBuckets response the Buckets page already fetches, and
// this page does the same rather than inventing a value.

const objectColumns: AwsColumn<S3Object>[] = [
  { id: "key", header: "Key", cell: (row) => row.key, value: (row) => row.key },
  { id: "size", header: "Size", cell: (row) => formatBytes(row.size), value: (row) => String(row.size) },
  {
    id: "lastModified",
    header: "Last modified",
    cell: (row) => formatTimestamp(row.lastModified),
    value: (row) => row.lastModified,
  },
];

function EditVersioningModal({
  name,
  current,
  onClose,
}: {
  name: string;
  current: "Enabled" | "Suspended" | "Disabled";
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [enabled, setEnabled] = useState(current === "Enabled");
  const update = useMutation({
    mutationFn: () => putS3BucketVersioning(name, enabled ? "Enabled" : "Suspended"),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["s3-bucket-versioning", name] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Edit bucket versioning"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="s3-versioning-save"
            disabled={update.isPending}
            onClick={() => update.mutate()}
          >
            {update.isPending ? "Saving…" : "Save changes"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>
          Versioning keeps every version of every object in the bucket, so you can recover from unintended overwrites
          and deletions. Once enabled, versioning can be suspended but not fully disabled.
        </p>
        <FormField label="Bucket versioning">
          <Toggle checked={enabled} onChange={(event) => setEnabled(event.detail.checked)} data-testid="s3-versioning-toggle">
            {enabled ? "Enabled" : "Suspended"}
          </Toggle>
        </FormField>
        {update.isError && (
          <AwsErrorAlert>
            <strong>Could not update versioning.</strong>{" "}
            {update.error instanceof Error ? update.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function S3BucketDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [editingVersioning, setEditingVersioning] = useState(false);
  const [tagging, setTagging] = useState(false);

  const buckets = useQuery({ queryKey: ["s3-buckets"], queryFn: fetchS3Buckets });
  const location = useQuery({ queryKey: ["s3-bucket-location", name], queryFn: () => fetchS3BucketLocation(name) });
  const versioning = useQuery({ queryKey: ["s3-bucket-versioning", name], queryFn: () => fetchS3BucketVersioning(name) });
  const tags = useQuery({ queryKey: ["s3-bucket-tagging", name], queryFn: () => fetchS3BucketTagging(name) });
  const bucket = buckets.data?.find((candidate) => candidate.name === name);
  const isError = buckets.isError || location.isError;
  const isLoading = buckets.isLoading || location.isLoading;

  const asS3Bucket: S3Bucket | null = bucket ?? null;

  return (
    <>
      <AwsPageHeader
        title={name}
        description="Bucket in Amazon Simple Storage Service."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton
              data-testid="s3-bucket-edit-versioning"
              disabled={!versioning.isSuccess}
              onClick={() => setEditingVersioning(true)}
            >
              Edit versioning
            </AwsButton>
            <AwsButton data-testid="s3-bucket-manage-tags" disabled={!tags.isSuccess} onClick={() => setTagging(true)}>
              Manage tags
            </AwsButton>
            <AwsButton data-testid="s3-bucket-delete" disabled={!location.isSuccess} onClick={() => setDeleting(true)}>
              Delete
            </AwsButton>
          </SpaceBetween>
        }
      />
      <AwsContainer>
        {isError ? (
          <AwsErrorAlert testId="s3-bucket-error">
            <strong>Could not load the bucket.</strong>{" "}
            {(buckets.error ?? location.error) instanceof Error
              ? ((buckets.error ?? location.error) as Error).message
              : "The request failed."}
          </AwsErrorAlert>
        ) : isLoading ? (
          <AwsEmptyState title="Loading bucket…" loading />
        ) : (
          <div data-testid="s3-bucket-summary">
            <AwsKeyValue
              items={[
                { label: "Region", value: location.data },
                { label: "Creation date", value: bucket ? formatTimestamp(bucket.creationDate) : "–" },
                {
                  label: "Versioning",
                  value: versioning.isSuccess ? (
                    <AwsStatus
                      status={versioning.data}
                      kind={versioning.data === "Enabled" ? "success" : "inactive"}
                    />
                  ) : (
                    "–"
                  ),
                },
                {
                  label: "Tags",
                  value: tags.isSuccess ? `${Object.keys(tags.data).length} tag(s)` : "–",
                },
              ]}
            />
          </div>
        )}
      </AwsContainer>
      <AwsResourceTable<S3Object>
        title="Objects"
        headingVariant="h2"
        description="Objects stored in this bucket."
        columns={objectColumns}
        queryKey={["s3-objects", name]}
        queryFn={() => fetchS3Objects(name)}
        filterPlaceholder="Find objects"
        emptyTitle="No objects"
        emptyDescription="This bucket has no objects."
        rowKey={(row) => row.key}
        tableTestId="s3-objects-table"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {editingVersioning && versioning.isSuccess && (
        <EditVersioningModal name={name} current={versioning.data} onClose={() => setEditingVersioning(false)} />
      )}
      {tagging && tags.isSuccess && (
        <TagsEditorModal
          title="Manage bucket tags"
          intro={`Tags applied to the ${name} bucket.`}
          initialTags={tags.data}
          testIdPrefix="s3-bucket"
          onClose={() => setTagging(false)}
          onSaved={() => queryClient.invalidateQueries({ queryKey: ["s3-bucket-tagging", name] })}
          save={(next) => putS3BucketTagging(name, next)}
        />
      )}
      {deleting && asS3Bucket && (
        <DeleteBucketsModal
          buckets={[asS3Bucket]}
          clearSelection={() => navigate("/ui/s3")}
          onClose={() => {
            setDeleting(false);
            void queryClient.invalidateQueries({ queryKey: ["s3-buckets"] });
          }}
        />
      )}
    </>
  );
}
