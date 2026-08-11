import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createCodeBuildProject,
  deleteCodeBuildProject,
  fetchCodeBuildBuilds,
  fetchCodeBuildProjects,
  startCodeBuild,
  stopCodeBuild,
  type CodeBuildBuild,
  type CodeBuildProject,
} from "../api.js";

// AWS CodeBuild — projects and build history. Every action below calls the
// public CodeBuild AWS JSON 1.1 API, so the console uses the same data plane at
// simulator and real-AWS coordinates.

const projectColumns: AwsColumn<CodeBuildProject>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "sourceType", header: "Source provider", cell: (row) => row.sourceType || "–", value: (row) => row.sourceType },
  {
    id: "environmentImage",
    header: "Environment image",
    cell: (row) => row.environmentImage || "–",
    value: (row) => row.environmentImage,
  },
  {
    id: "environmentType",
    header: "Environment type",
    cell: (row) => row.environmentType || "–",
    value: (row) => row.environmentType,
  },
  { id: "serviceRole", header: "Service role", cell: (row) => row.serviceRole || "–", value: (row) => row.serviceRole },
  { id: "created", header: "Created", cell: (row) => formatEpoch(row.created), value: (row) => String(row.created) },
];

const buildColumns: AwsColumn<CodeBuildBuild>[] = [
  { id: "id", header: "Build ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "project", header: "Project", cell: (row) => row.projectName, value: (row) => row.projectName },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  {
    id: "image",
    header: "Environment image",
    cell: (row) => row.environmentImage || "–",
    value: (row) => row.environmentImage,
  },
  { id: "started", header: "Started", cell: (row) => formatEpoch(row.startTime), value: (row) => String(row.startTime) },
  { id: "ended", header: "Ended", cell: (row) => (row.endTime ? formatEpoch(row.endTime) : "–"), value: (row) => String(row.endTime) },
];

const defaultBuildspec = `version: 0.2
phases:
  build:
    commands:
      - echo "Build started"
      - uname -a
`;

function CreateProjectModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [image, setImage] = useState("public.ecr.aws/docker/library/alpine:3.21");
  const [serviceRole, setServiceRole] = useState("");
  const [buildspec, setBuildspec] = useState(defaultBuildspec);
  const valid = /^[A-Za-z0-9][A-Za-z0-9_-]{1,149}$/.test(name) && image.includes(":") && serviceRole.startsWith("arn:") && buildspec.trim().length > 0;
  const create = useMutation({
    mutationFn: () => createCodeBuildProject({ name, image, serviceRole, buildspec }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["codebuild-projects"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create build project"
      size="max"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="codebuild-create-project-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create build project"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <FormField label="Project name">
          <Input value={name} onChange={(event) => setName(event.detail.value)} />
        </FormField>
        <FormField
          label="Environment image"
          description="The build runs in this exact Docker image. AWS-managed or custom registry image identifiers use the normal CodeBuild field."
        >
          <Input value={image} onChange={(event) => setImage(event.detail.value)} />
        </FormField>
        <FormField label="Service role">
          <Input
            value={serviceRole}
            placeholder="arn:aws:iam::123456789012:role/codebuild-role"
            onChange={(event) => setServiceRole(event.detail.value)}
          />
        </FormField>
        <FormField label="Build specification">
          <Textarea
            value={buildspec}
            onChange={(event) => setBuildspec(event.detail.value)}
            rows={14}
            spellcheck={false}
            ariaLabel="AWS CodeBuild build specification"
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the project.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function DeleteProjectsModal({
  projects,
  clearSelection,
  onClose,
}: {
  projects: CodeBuildProject[];
  clearSelection: () => void;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const project of projects) await deleteCodeBuildProject(project.name);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["codebuild-projects"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={projects.length === 1 ? `Delete ${projects[0].name}?` : `Delete ${projects.length} build projects?`}
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
      <p>Build history remains available after its project is deleted, matching AWS CodeBuild.</p>
      {remove.isError && <AwsErrorAlert>{remove.error instanceof Error ? remove.error.message : "Delete failed."}</AwsErrorAlert>}
    </AwsModal>
  );
}

export function CodeBuildPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ projects: CodeBuildProject[]; clearSelection: () => void } | null>(null);
  const start = useMutation({
    mutationFn: async (projects: CodeBuildProject[]) => {
      for (const project of projects) await startCodeBuild(project.name);
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["codebuild-builds"] });
    },
  });
  const stop = useMutation({
    mutationFn: async (builds: CodeBuildBuild[]) => {
      for (const build of builds) await stopCodeBuild(build.id);
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["codebuild-builds"] });
    },
  });
  return (
    <>
      <AwsResourceTable<CodeBuildProject>
        title="Build projects"
        description="Create projects and start their containerized builds."
        columns={projectColumns}
        queryKey={["codebuild-projects"]}
        queryFn={fetchCodeBuildProjects}
        filterPlaceholder="Find build projects"
        emptyTitle="No build projects"
        emptyDescription="No AWS CodeBuild projects exist in this account and Region."
        rowKey={(row) => row.arn || row.name}
        tableTestId="codebuild-table"
        errorTestId="codebuild-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="codebuild-start-build"
              disabled={selected.length === 0 || start.isPending}
              onClick={() => start.mutate(selected)}
            >
              {start.isPending ? "Starting…" : "Start build"}
            </AwsButton>
            <AwsButton
              data-testid="codebuild-delete-project"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ projects: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
            <AwsButton variant="primary" data-testid="codebuild-create-project" onClick={() => setCreating(true)}>
              Create build project
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<CodeBuildBuild>
        title="Build history"
        headingVariant="h2"
        description="Actual build containers and their terminal exit-derived status."
        columns={buildColumns}
        queryKey={["codebuild-builds"]}
        queryFn={fetchCodeBuildBuilds}
        filterPlaceholder="Find builds"
        emptyTitle="No builds"
        emptyDescription="Start a project build to populate build history."
        rowKey={(row) => row.id}
        refreshInterval={1_000}
        tableTestId="codebuild-builds-table"
        errorTestId="codebuild-builds-error"
        actions={({ selected, refetch, isFetching }) => (
          <>
            <AwsButton
              disabled={selected.length === 0 || selected.some((build) => build.status !== "IN_PROGRESS") || stop.isPending}
              onClick={() => stop.mutate(selected)}
            >
              Stop
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
          </>
        )}
      />
      {creating && <CreateProjectModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteProjectsModal
          projects={deleting.projects}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
