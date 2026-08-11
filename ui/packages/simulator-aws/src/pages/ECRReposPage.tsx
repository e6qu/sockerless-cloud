import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsRowLink, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import { createECRRepository, deleteECRRepo, fetchECRRepos, type ECRRepo } from "../api.js";

// Amazon Elastic Container Registry — Repositories. The list and Delete both
// go through the real ECR awsjson1.1 API (DescribeRepositories for the
// table, DeleteRepository for the header action) with the operator's
// federated credentials.

const columns: AwsColumn<ECRRepo>[] = [
  {
    id: "name",
    header: "Repository name",
    cell: (row) => <AwsRowLink to={`/ui/ecr/${encodeURIComponent(row.name)}`}>{row.name}</AwsRowLink>,
    value: (row) => row.name,
  },
  { id: "uri", header: "URI", cell: (row) => row.uri, value: (row) => row.uri },
  { id: "createdAt", header: "Created at", cell: (row) => formatEpoch(row.createdAt), value: (row) => String(row.createdAt) },
];

// The repository-name shape real ECR enforces on CreateRepository: 2–256
// characters of lowercase letters, numbers, and the separators `.` `_` `-`,
// optionally namespaced with `/` — validating it inline is what the real
// console does before it lets the request go out.
const ECR_REPO_NAME_PATTERN = /^(?:[a-z0-9]+(?:[._-][a-z0-9]+)*\/)*[a-z0-9]+(?:[._-][a-z0-9]+)*$/;
function isValidECRRepoName(name: string): boolean {
  return name.length >= 2 && name.length <= 256 && ECR_REPO_NAME_PATTERN.test(name);
}

function CreateRepoModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: createECRRepository,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ecr-repos"] });
      onClose();
    },
  });
  const trimmed = name.trim();
  const valid = isValidECRRepoName(trimmed);
  return (
    <AwsModal
      title="Create repository"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ecr-create-repo-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate(trimmed)}
          >
            {create.isPending ? "Creating…" : "Create repository"}
          </AwsButton>
        </>
      }
    >
      <p>A private repository stores the container images you push to it, versioned by tag or digest.</p>
      <FormField
        label="Repository name"
        constraintText="2–256 characters. Lowercase letters, numbers, and the separators . _ -, optionally namespaced with /."
      >
        <Input
          value={name}
          onChange={(event) => setName(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "ecr-repo-name-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the repository.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function DeleteReposModal({
  repos,
  onClose,
  clearSelection,
}: {
  repos: ECRRepo[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    // DeleteRepository is per-repository on the wire; a failure part-way
    // surfaces as the real API error, with the already-deleted repositories
    // gone from the refreshed list.
    mutationFn: async () => {
      for (const repo of repos) {
        await deleteECRRepo(repo.name);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ecr-repos"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={repos.length === 1 ? `Delete ${repos[0].name}?` : `Delete ${repos.length} repositories?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ecr-delete-repo-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a repository is permanent and deletes every image it holds.</p>
      <ul>
        {repos.map((repo) => (
          <li key={repo.name}>
            <code>{repo.name}</code>
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

export function ECRReposPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ repos: ECRRepo[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<ECRRepo>
        title="Repositories"
        description="Private repositories in this account and Region."
        columns={columns}
        queryKey={["ecr-repos"]}
        queryFn={fetchECRRepos}
        filterPlaceholder="Find repositories"
        emptyTitle="No repositories"
        emptyDescription="No private repositories exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="ecr-repos-table"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="ecr-view-repo"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/ecr/${encodeURIComponent(selected[0].name)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="ecr-delete-repo"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ repos: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="ecr-create-repo" onClick={() => setCreating(true)}>
              Create repository
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateRepoModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteReposModal repos={deleting.repos} clearSelection={deleting.clearSelection} onClose={() => setDeleting(null)} />
      )}
    </>
  );
}
