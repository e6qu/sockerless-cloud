import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Select from "@cloudscape-design/components/select";
import Toggle from "@cloudscape-design/components/toggle";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsContainer,
  AwsCopyButton,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsModal,
  AwsPageHeader,
  AwsResourceTable,
  removedKeys,
  TagsEditorModal,
  type AwsColumn,
} from "../console/index.js";
import { formatBytes, formatEpoch } from "../console/format.js";
import {
  fetchECRImages,
  fetchECRRepo,
  fetchECRTags,
  putECRImageScanningConfiguration,
  putECRImageTagMutability,
  tagECRResource,
  untagECRResource,
  type ECRImage,
  type ECRRepo,
} from "../api.js";
import { DeleteReposModal } from "./ECRReposPage.js";

// Amazon Elastic Container Registry — Repository detail. Reads the real ECR
// awsjson1.1 API (DescribeRepositories filtered to one name, since there is
// no singular DescribeRepository operation, DescribeImages for the image
// list, and ListTagsForResource for the tags) with the operator's federated
// credentials, and drives the real update surface the console offers for a
// repository: PutImageTagMutability, PutImageScanningConfiguration,
// TagResource/UntagResource, and DeleteRepository.

const imageColumns: AwsColumn<ECRImage>[] = [
  {
    id: "tags",
    header: "Image tag",
    cell: (row) => (row.tags.length > 0 ? row.tags.join(", ") : <em>untagged</em>),
    value: (row) => row.tags.join(" "),
  },
  { id: "digest", header: "Digest", cell: (row) => <code>{row.digest}</code>, value: (row) => row.digest },
  { id: "pushedAt", header: "Pushed at", cell: (row) => formatEpoch(row.pushedAt), value: (row) => String(row.pushedAt) },
  { id: "size", header: "Size", cell: (row) => formatBytes(row.sizeBytes), value: (row) => String(row.sizeBytes) },
];

const MUTABILITY_OPTIONS = [
  { label: "Mutable", value: "MUTABLE" },
  { label: "Immutable", value: "IMMUTABLE" },
];

function EditRepoSettingsModal({ repo, onClose }: { repo: ECRRepo; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [mutability, setMutability] = useState<"MUTABLE" | "IMMUTABLE">(
    repo.imageTagMutability === "IMMUTABLE" ? "IMMUTABLE" : "MUTABLE",
  );
  const [scanOnPush, setScanOnPush] = useState(repo.scanOnPush);
  const update = useMutation({
    mutationFn: async () => {
      if (mutability !== repo.imageTagMutability) await putECRImageTagMutability(repo.name, mutability);
      if (scanOnPush !== repo.scanOnPush) await putECRImageScanningConfiguration(repo.name, scanOnPush);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ecr-repo", repo.name] });
      onClose();
    },
  });
  const selected = MUTABILITY_OPTIONS.find((option) => option.value === mutability) ?? MUTABILITY_OPTIONS[0];
  return (
    <AwsModal
      title="Edit repository settings"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ecr-repo-settings-save"
            disabled={update.isPending}
            onClick={() => update.mutate()}
          >
            {update.isPending ? "Saving…" : "Save"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField
          label="Image tag mutability"
          description="Immutable tags prevent an image tag from being overwritten by a later push."
        >
          <Select
            selectedOption={selected}
            options={MUTABILITY_OPTIONS}
            onChange={(event) => setMutability(event.detail.selectedOption.value as "MUTABLE" | "IMMUTABLE")}
            ariaLabel="Image tag mutability"
            data-testid="ecr-mutability-select"
          />
        </FormField>
        <FormField label="Scan on push" description="Scan images for vulnerabilities each time they are pushed.">
          <Toggle checked={scanOnPush} onChange={(event) => setScanOnPush(event.detail.checked)} data-testid="ecr-scan-toggle">
            {scanOnPush ? "Enabled" : "Disabled"}
          </Toggle>
        </FormField>
        {update.isError && (
          <AwsErrorAlert>
            <strong>Could not save settings.</strong>{" "}
            {update.error instanceof Error ? update.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function ECRRepositoryDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [tagging, setTagging] = useState(false);
  const repo = useQuery({ queryKey: ["ecr-repo", name], queryFn: () => fetchECRRepo(name) });
  const arn = repo.data?.arn ?? "";
  const tags = useQuery({ queryKey: ["ecr-tags", arn], queryFn: () => fetchECRTags(arn), enabled: Boolean(arn) });

  const asECRRepo: ECRRepo | null = repo.data ?? null;

  return (
    <>
      <AwsPageHeader
        title={name}
        description="Repository in Amazon Elastic Container Registry."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton data-testid="ecr-repo-edit" disabled={!repo.isSuccess} onClick={() => setEditing(true)}>
              Edit
            </AwsButton>
            <AwsButton data-testid="ecr-repo-manage-tags" disabled={!tags.isSuccess} onClick={() => setTagging(true)}>
              Manage tags
            </AwsButton>
            <AwsButton data-testid="ecr-repo-delete" disabled={!repo.isSuccess} onClick={() => setDeleting(true)}>
              Delete
            </AwsButton>
          </SpaceBetween>
        }
      />
      <AwsContainer>
        {repo.isError ? (
          <AwsErrorAlert testId="ecr-repo-error">
            <strong>Could not load the repository.</strong>{" "}
            {repo.error instanceof Error ? repo.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : repo.isLoading ? (
          <AwsEmptyState title="Loading repository…" loading />
        ) : repo.data ? (
          <div data-testid="ecr-repo-summary">
            <AwsKeyValue
              items={[
                {
                  label: "URI",
                  value: (
                    <>
                      <code>{repo.data.uri}</code>
                      <AwsCopyButton value={repo.data.uri} label="Copy repository URI" />
                    </>
                  ),
                },
                { label: "Tag mutability", value: repo.data.imageTagMutability || "MUTABLE" },
                { label: "Scan on push", value: repo.data.scanOnPush ? "Enabled" : "Disabled" },
                { label: "Tags", value: tags.isSuccess ? `${Object.keys(tags.data).length} tag(s)` : "–" },
                { label: "Created at", value: formatEpoch(repo.data.createdAt) },
              ]}
            />
          </div>
        ) : null}
      </AwsContainer>
      <AwsResourceTable<ECRImage>
        title="Images"
        headingVariant="h2"
        description="Images pushed to this repository."
        columns={imageColumns}
        queryKey={["ecr-images", name]}
        queryFn={() => fetchECRImages(name)}
        filterPlaceholder="Find images"
        emptyTitle="No images"
        emptyDescription="No images have been pushed to this repository."
        rowKey={(row) => row.digest}
        tableTestId="ecr-images-table"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {editing && repo.data && <EditRepoSettingsModal repo={repo.data} onClose={() => setEditing(false)} />}
      {tagging && tags.isSuccess && arn && (
        <TagsEditorModal
          title="Manage tags"
          intro={`Tags applied to the ${name} repository.`}
          initialTags={tags.data}
          testIdPrefix="ecr-repo"
          onClose={() => setTagging(false)}
          onSaved={() => queryClient.invalidateQueries({ queryKey: ["ecr-tags", arn] })}
          save={async (next) => {
            const remove = removedKeys(tags.data, next);
            if (Object.keys(next).length > 0) await tagECRResource(arn, next);
            if (remove.length > 0) await untagECRResource(arn, remove);
          }}
        />
      )}
      {deleting && asECRRepo && (
        <DeleteReposModal
          repos={[asECRRepo]}
          clearSelection={() => navigate("/ui/ecr")}
          onClose={() => {
            setDeleting(false);
            void queryClient.invalidateQueries({ queryKey: ["ecr-repo", name] });
          }}
        />
      )}
    </>
  );
}
