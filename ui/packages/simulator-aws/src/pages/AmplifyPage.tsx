import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createAmplifyApp,
  createAmplifyBranch,
  deleteAmplifyApp,
  fetchAmplifyApps,
  fetchAmplifyBranches,
  fetchAmplifyJobs,
  startAmplifyJob,
  type AmplifyApp,
} from "../api.js";

// AWS Amplify — Apps. ListApps on the real Amplify REST-JSON API (GET /apps).

const columns: AwsColumn<AmplifyApp>[] = [
  { id: "name", header: "App name", cell: (row) => row.name, value: (row) => row.name },
  { id: "appId", header: "App ID", cell: (row) => row.appId, value: (row) => row.appId },
  { id: "platform", header: "Platform", cell: (row) => row.platform || "–", value: (row) => row.platform },
  {
    id: "defaultDomain",
    header: "Default domain",
    cell: (row) => row.defaultDomain || "–",
    value: (row) => row.defaultDomain,
  },
  { id: "repository", header: "Repository", cell: (row) => row.repository || "–", value: (row) => row.repository },
  {
    id: "repositoryCloneMethod",
    header: "Repository access",
    cell: (row) => row.repositoryCloneMethod || "Manual deploy",
    value: (row) => row.repositoryCloneMethod,
  },
  {
    id: "createTime",
    header: "Created",
    cell: (row) => formatEpoch(row.createTime),
    value: (row) => String(row.createTime),
  },
];

function CreateAmplifyAppModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [repository, setRepository] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [platform, setPlatform] = useState<"WEB" | "WEB_COMPUTE">("WEB");
  const [buildSpec, setBuildSpec] = useState("");
  const valid = name.trim().length > 0 && (!repository || /^https?:\/\//.test(repository));
  const create = useMutation({
    mutationFn: () => createAmplifyApp({ name, repository, accessToken, platform, buildSpec }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["amplify-apps"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create Amplify app"
      size="max"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="amplify-create-app-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create app"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <FormField label="App name">
          <Input value={name} onChange={(event) => setName(event.detail.value)} />
        </FormField>
        <FormField
          label="Repository URL"
          description="An HTTPS Git repository. Leave blank when the app will use manual deployments."
        >
          <Input value={repository} onChange={(event) => setRepository(event.detail.value)} placeholder="https://github.com/example/site" />
        </FormField>
        <FormField
          label="Repository access token"
          description="Used to establish a private repository connection. The token is write-only and is not returned by Amplify."
        >
          <Input value={accessToken} type="password" onChange={(event) => setAccessToken(event.detail.value)} />
        </FormField>
        <FormField label="Platform">
          <Select
            selectedOption={{ label: platform === "WEB" ? "Web" : "Web compute", value: platform }}
            options={[
              { label: "Web", value: "WEB", description: "Static web hosting" },
              { label: "Web compute", value: "WEB_COMPUTE", description: "Server-side rendered hosting" },
            ]}
            onChange={(event) => setPlatform(event.detail.selectedOption.value as "WEB" | "WEB_COMPUTE")}
          />
        </FormField>
        <FormField
          label="Build specification"
          description="Optional. Leave blank to use amplify.yml from the repository. The managed build image includes Node.js and Python."
        >
          <Textarea
            value={buildSpec}
            onChange={(event) => setBuildSpec(event.detail.value)}
            rows={16}
            spellcheck={false}
            ariaLabel="AWS Amplify build specification"
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the app.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function ManageAmplifyAppModal({ app, onClose }: { app: AmplifyApp; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [newBranch, setNewBranch] = useState("");
  const [selectedBranch, setSelectedBranch] = useState("");
  const branches = useQuery({
    queryKey: ["amplify-branches", app.appId],
    queryFn: () => fetchAmplifyBranches(app.appId),
  });
  const branchName = selectedBranch || branches.data?.[0]?.branchName || "";
  const jobs = useQuery({
    queryKey: ["amplify-jobs", app.appId, branchName],
    queryFn: () => fetchAmplifyJobs(app.appId, branchName),
    enabled: branchName.length > 0,
    refetchInterval: (query) =>
      query.state.data?.some((job) => job.status === "PENDING" || job.status === "RUNNING") ? 1_000 : false,
  });
  const createBranch = useMutation({
    mutationFn: () => createAmplifyBranch(app.appId, newBranch),
    onSuccess: async () => {
      setSelectedBranch(newBranch);
      setNewBranch("");
      await queryClient.invalidateQueries({ queryKey: ["amplify-branches", app.appId] });
    },
  });
  const start = useMutation({
    mutationFn: () => startAmplifyJob(app.appId, branchName),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["amplify-jobs", app.appId, branchName] });
    },
  });
  return (
    <AwsModal title={`Hosting for ${app.name}`} size="max" onDismiss={onClose} footer={<AwsButton onClick={onClose}>Close</AwsButton>}>
      <SpaceBetween size="l">
        <div>
          <h3>Connected repository</h3>
          <p><code>{app.repository || "Manual deployments"}</code></p>
          <p>Repository access: {app.repositoryCloneMethod || "No connected repository credential"}</p>
        </div>
        <FormField label="Create branch">
          <SpaceBetween direction="horizontal" size="s">
            <Input
              value={newBranch}
              data-testid="amplify-branch-name"
              onChange={(event) => setNewBranch(event.detail.value)}
              placeholder="main"
            />
            <AwsButton
              variant="primary"
              data-testid="amplify-create-branch"
              disabled={!/^[A-Za-z0-9._/-]+$/.test(newBranch) || createBranch.isPending}
              onClick={() => createBranch.mutate()}
            >
              Create branch
            </AwsButton>
          </SpaceBetween>
        </FormField>
        <FormField label="Branch">
          <Select
            selectedOption={branchName ? { label: branchName, value: branchName } : null}
            options={(branches.data ?? []).map((branch) => ({
              label: branch.branchName,
              value: branch.branchName,
              description: `${branch.stage || "NONE"}${branch.activeJobId ? ` · active job ${branch.activeJobId}` : ""}`,
            }))}
            onChange={(event) => setSelectedBranch(event.detail.selectedOption.value ?? "")}
            placeholder="Choose a branch"
            loadingText="Loading branches"
            statusType={branches.isLoading ? "loading" : "finished"}
          />
        </FormField>
        <div>
          <AwsButton variant="primary" disabled={!branchName || !app.repository || start.isPending} onClick={() => start.mutate()}>
            {start.isPending ? "Starting…" : "Start deployment"}
          </AwsButton>
        </div>
        <div>
          <h3>Deployment history</h3>
          {jobs.isError && <AwsErrorAlert>{jobs.error instanceof Error ? jobs.error.message : "Could not load jobs."}</AwsErrorAlert>}
          {!jobs.isLoading && (jobs.data?.length ?? 0) === 0 && <p>No deployments exist for this branch.</p>}
          {(jobs.data ?? []).map((job) => (
            <div key={job.jobId} style={{ display: "grid", gridTemplateColumns: "9rem 8rem 1fr 12rem", gap: "1rem", padding: "0.5rem 0" }}>
              <code>{job.jobId}</code>
              <AwsStatus status={job.status} />
              <span>{job.commitMessage || job.commitId || job.jobType}</span>
              <span>{formatEpoch(job.startTime)}</span>
            </div>
          ))}
        </div>
        {(createBranch.isError || start.isError) && (
          <AwsErrorAlert>
            {(createBranch.error ?? start.error) instanceof Error
              ? (createBranch.error ?? start.error as Error).message
              : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function DeleteAmplifyAppsModal({
  apps,
  clearSelection,
  onClose,
}: {
  apps: AmplifyApp[];
  clearSelection: () => void;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const app of apps) await deleteAmplifyApp(app.appId);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["amplify-apps"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={apps.length === 1 ? `Delete ${apps[0].name}?` : `Delete ${apps.length} apps?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton variant="primary" disabled={remove.isPending} onClick={() => remove.mutate()}>
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Branches, deployments, compute runtimes, artifacts, repository credentials, and build caches are removed with each app.</p>
      {remove.isError && <AwsErrorAlert>{remove.error instanceof Error ? remove.error.message : "Delete failed."}</AwsErrorAlert>}
    </AwsModal>
  );
}

export function AmplifyPage() {
  const [creating, setCreating] = useState(false);
  const [managing, setManaging] = useState<AmplifyApp | null>(null);
  const [deleting, setDeleting] = useState<{ apps: AmplifyApp[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<AmplifyApp>
        title="Apps"
        description="AWS Amplify apps in this account and Region."
        columns={columns}
        queryKey={["amplify-apps"]}
        queryFn={fetchAmplifyApps}
        filterPlaceholder="Find apps"
        emptyTitle="No apps"
        emptyDescription="No AWS Amplify apps exist in this account and Region."
        rowKey={(row) => row.appId}
        tableTestId="amplify-table"
        errorTestId="amplify-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="amplify-manage-hosting"
              disabled={selected.length !== 1}
              onClick={() => setManaging(selected[0] ?? null)}
            >
              Manage hosting
            </AwsButton>
            <AwsButton
              data-testid="amplify-delete-app"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ apps: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="amplify-create-app" onClick={() => setCreating(true)}>
              Create app
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateAmplifyAppModal onClose={() => setCreating(false)} />}
      {managing && <ManageAmplifyAppModal app={managing} onClose={() => setManaging(null)} />}
      {deleting && (
        <DeleteAmplifyAppsModal
          apps={deleting.apps}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
