import { useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Toggle from "@cloudscape-design/components/toggle";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import KeyValuePairs from "@cloudscape-design/components/key-value-pairs";
import Box from "@cloudscape-design/components/box";
import Table from "@cloudscape-design/components/table";
import {
  AwsButton,
  AwsCopyableText,
  AwsDeleteModal,
  AwsEmptyState,
  AwsErrorAlert,
  AwsModal,
  AwsPageHeader,
  AwsRefreshButton,
  AwsResourceTable,
  AwsStatus,
  AwsTabs,
  TagsEditorModal,
  errorText,
  useNotify,
  type AwsColumn,
} from "../console/index.js";
import { formatBytes, formatTimestamp } from "../console/format.js";
import { regionName } from "../console/regions.js";
import {
  deleteS3Object,
  fetchS3BucketLocation,
  fetchS3BucketTagging,
  fetchS3BucketVersioning,
  fetchS3Buckets,
  fetchS3Objects,
  putS3BucketTagging,
  putS3BucketVersioning,
  putS3Object,
  type S3Object,
} from "../api.js";
import { DeleteBucketModal } from "./S3BucketsPage.js";

// Amazon S3 — bucket detail, with the real console's Objects and Properties
// tabs. Objects reads ListObjectsV2 and drives PutObject (Upload) and
// DeleteObject (Delete); Properties reads GetBucketLocation,
// GetBucketVersioning and GetBucketTagging and edits them through
// PutBucketVersioning and PutBucketTagging. S3 has no per-bucket read for the
// creation date: the real console takes it from ListBuckets, as this page does.

const objectColumns: AwsColumn<S3Object>[] = [
  { id: "key", header: "Name", cell: (row) => row.key, value: (row) => row.key },
  { id: "type", header: "Type", cell: (row) => row.key.split(".").pop() ?? "–", value: (row) => row.key.split(".").pop() ?? "" },
  { id: "lastModified", header: "Last modified", cell: (row) => formatTimestamp(row.lastModified), value: (row) => row.lastModified },
  { id: "size", header: "Size", cell: (row) => formatBytes(row.size), value: (row) => String(row.size) },
  { id: "etag", header: "ETag", cell: (row) => row.etag, value: (row) => row.etag },
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
  const notify = useNotify();
  const [enabled, setEnabled] = useState(current === "Enabled");
  const update = useMutation({
    mutationFn: () => putS3BucketVersioning(name, enabled ? "Enabled" : "Suspended"),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["s3-bucket-versioning", name] });
      notify("success", "Successfully edited Bucket Versioning.");
      onClose();
    },
  });
  return (
    <AwsModal
      title="Edit Bucket Versioning"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton variant="link" onClick={onClose}>
            Cancel
          </AwsButton>
          <AwsButton variant="primary" data-testid="s3-versioning-save" loading={update.isPending} onClick={() => update.mutate()}>
            Save changes
          </AwsButton>
        </>
      }
    >
      <Box>
        Versioning keeps every version of every object in the bucket, so you can recover from unintended overwrites and
        deletions. Once enabled, versioning can be suspended but not fully disabled.
      </Box>
      <FormField label="Bucket Versioning">
        <Toggle checked={enabled} onChange={(event) => setEnabled(event.detail.checked)} data-testid="s3-versioning-toggle">
          {enabled ? "Enable" : "Suspend"}
        </Toggle>
      </FormField>
      {update.isError && (
        <AwsErrorAlert>
          <strong>Could not update versioning.</strong> {errorText(update.error)}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function ObjectsTab({ name }: { name: string }) {
  const queryClient = useQueryClient();
  const notify = useNotify();
  const fileInput = useRef<HTMLInputElement>(null);
  const [deleting, setDeleting] = useState<{ objects: S3Object[]; clearSelection: () => void } | null>(null);
  const upload = useMutation({
    mutationFn: async (files: File[]) => {
      for (const file of files) {
        await putS3Object(name, file.name, new Uint8Array(await file.arrayBuffer()), file.type);
      }
      return files;
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["s3-objects", name] }),
    onSuccess: (files) => notify("success", `Upload succeeded: ${files.length} file${files.length === 1 ? "" : "s"}`),
    onError: (error) => notify("error", "Upload failed", errorText(error)),
  });
  const remove = useMutation({
    mutationFn: async (objects: S3Object[]) => {
      for (const object of objects) await deleteS3Object(name, object.key);
      return objects;
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["s3-objects", name] }),
    onSuccess: (objects) => {
      notify("success", `Successfully deleted ${objects.length} object${objects.length === 1 ? "" : "s"}`);
      deleting?.clearSelection();
      setDeleting(null);
    },
  });
  return (
    <>
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        aria-label="Choose files to upload"
        data-testid="s3-upload-input"
        onChange={(event) => {
          const files = Array.from(event.target.files ?? []);
          event.target.value = "";
          if (files.length > 0) upload.mutate(files);
        }}
      />
      <AwsResourceTable<S3Object>
        title="Objects"
        headingVariant="h2"
        description="Objects are the fundamental entities stored in Amazon S3."
        columns={objectColumns}
        queryKey={["s3-objects", name]}
        queryFn={() => fetchS3Objects(name)}
        filterPlaceholder="Find objects by prefix"
        emptyTitle="No objects"
        emptyDescription="You don't have any objects in this bucket."
        rowKey={(row) => row.key}
        tableTestId="s3-objects-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <AwsButton
              data-testid="s3-delete-objects"
              disabled={selected.length === 0}
              onClick={() => {
                remove.reset();
                setDeleting({ objects: selected, clearSelection });
              }}
            >
              Delete
            </AwsButton>
            <AwsButton variant="primary" data-testid="s3-upload" loading={upload.isPending} onClick={() => fileInput.current?.click()}>
              Upload
            </AwsButton>
          </>
        )}
      />
      {deleting && (
        <AwsDeleteModal
          title="Delete objects"
          confirmationText="permanently delete"
          onDismiss={() => setDeleting(null)}
          onDelete={() => remove.mutate(deleting.objects)}
          pending={remove.isPending}
          testIdPrefix="s3-delete-objects"
          error={remove.isError ? <AwsErrorAlert>{errorText(remove.error)}</AwsErrorAlert> : undefined}
        >
          <Box>Deleting the specified objects can't be undone.</Box>
          <ul>
            {deleting.objects.map((object) => (
              <li key={object.key}>{object.key}</li>
            ))}
          </ul>
        </AwsDeleteModal>
      )}
    </>
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
  const failure = buckets.error ?? location.error;
  const tagRows = Object.entries(tags.data ?? {}).map(([key, value]) => ({ key, value }));

  const properties = failure ? (
    <AwsErrorAlert testId="s3-bucket-error">
      <strong>Could not load the bucket.</strong> {errorText(failure)}
    </AwsErrorAlert>
  ) : buckets.isLoading || location.isLoading ? (
    <AwsEmptyState title="Loading bucket" loading />
  ) : (
    <SpaceBetween size="l">
      <Container header={<Header variant="h2">Bucket overview</Header>}>
        <div data-testid="s3-bucket-summary">
          <KeyValuePairs
            columns={3}
            items={[
              { label: "AWS Region", value: location.data ? `${regionName(location.data)} ${location.data}` : "–" },
              { label: "Amazon Resource Name (ARN)", value: <AwsCopyableText value={`arn:aws:s3:::${name}`} label="Copy ARN" /> },
              { label: "Creation date", value: bucket ? formatTimestamp(bucket.creationDate) : "–" },
            ]}
          />
        </div>
      </Container>
      <Container
        header={
          <Header
            variant="h2"
            actions={
              <AwsButton data-testid="s3-bucket-edit-versioning" disabled={!versioning.isSuccess} onClick={() => setEditingVersioning(true)}>
                Edit
              </AwsButton>
            }
          >
            Bucket Versioning
          </Header>
        }
      >
        <KeyValuePairs
          items={[
            {
              label: "Bucket Versioning",
              value: versioning.isSuccess ? (
                <AwsStatus status={versioning.data} kind={versioning.data === "Enabled" ? "success" : "inactive"} />
              ) : (
                "–"
              ),
            },
          ]}
        />
      </Container>
      <Table
        variant="container"
        header={
          <Header
            variant="h2"
            counter={`(${tagRows.length})`}
            actions={
              <AwsButton data-testid="s3-bucket-manage-tags" disabled={!tags.isSuccess} onClick={() => setTagging(true)}>
                Edit
              </AwsButton>
            }
          >
            Tags
          </Header>
        }
        items={tagRows}
        loading={tags.isLoading}
        loadingText="Loading tags"
        columnDefinitions={[
          { id: "key", header: "Key", cell: (row) => row.key, isRowHeader: true },
          { id: "value", header: "Value", cell: (row) => row.value || "–" },
        ]}
        empty={<Box textAlign="center">No tags associated with the resource.</Box>}
      />
    </SpaceBetween>
  );

  return (
    <SpaceBetween size="l">
      <AwsPageHeader
        title={name}
        actions={
          <AwsButton data-testid="s3-bucket-delete" disabled={!bucket} onClick={() => setDeleting(true)}>
            Delete
          </AwsButton>
        }
      />
      <AwsTabs
        ariaLabel="Bucket details"
        tabs={[
          { id: "objects", label: "Objects", content: <ObjectsTab name={name} /> },
          { id: "properties", label: "Properties", content: properties },
        ]}
      />
      {editingVersioning && versioning.isSuccess && (
        <EditVersioningModal name={name} current={versioning.data} onClose={() => setEditingVersioning(false)} />
      )}
      {tagging && tags.isSuccess && (
        <TagsEditorModal
          title="Edit bucket tags"
          intro={`Tags applied to the ${name} bucket.`}
          initialTags={tags.data}
          testIdPrefix="s3-bucket"
          onClose={() => setTagging(false)}
          onSaved={() => queryClient.invalidateQueries({ queryKey: ["s3-bucket-tagging", name] })}
          save={(next) => putS3BucketTagging(name, next)}
        />
      )}
      {deleting && bucket && <DeleteBucketModal bucket={bucket} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/s3")} />}
    </SpaceBetween>
  );
}
