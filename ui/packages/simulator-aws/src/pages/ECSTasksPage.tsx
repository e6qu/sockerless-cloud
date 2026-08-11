import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import RadioGroup from "@cloudscape-design/components/radio-group";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import {
  fetchECSClusters,
  fetchECSTaskDefinitionFamilies,
  fetchECSTasks,
  registerECSTaskDefinition,
  runECSTask,
  stopECSTask,
  type ECSTask,
} from "../api.js";
import { ECSServicesPage } from "./ECSServicesPage.js";

function shortName(arn: string): string {
  const slash = arn.lastIndexOf("/");
  return slash === -1 ? arn : arn.slice(slash + 1);
}

const LAUNCH_TYPES = [
  { label: "EC2", value: "EC2" },
  { label: "Fargate", value: "FARGATE" },
];

function RunTaskModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const clusters = useQuery({ queryKey: ["ecs-clusters"], queryFn: fetchECSClusters });
  const families = useQuery({ queryKey: ["ecs-td-families"], queryFn: fetchECSTaskDefinitionFamilies });

  const [cluster, setCluster] = useState("");
  const [launchType, setLaunchType] = useState<"EC2" | "FARGATE">("EC2");
  const [source, setSource] = useState<"existing" | "new">("existing");
  const [family, setFamily] = useState("");
  const [newFamily, setNewFamily] = useState("");
  const [image, setImage] = useState("");
  const [cpu, setCpu] = useState("");
  const [memory, setMemory] = useState("");

  const clusterValid = cluster.trim().length > 0;
  const fargate = launchType === "FARGATE";
  const existingValid = source === "existing" && family.trim().length > 0;
  const newValid =
    source === "new" &&
    newFamily.trim().length > 0 &&
    image.trim().length > 0 &&
    (!fargate || (cpu.trim().length > 0 && memory.trim().length > 0));
  const valid = clusterValid && (existingValid || newValid);

  const run = useMutation({
    mutationFn: async () => {
      let taskDefinition = family;
      if (source === "new") {
        taskDefinition = await registerECSTaskDefinition({
          family: newFamily.trim(),
          image: image.trim(),
          cpu: cpu.trim(),
          memory: memory.trim(),
          launchType,
        });
      }
      await runECSTask({ cluster, taskDefinition, launchType, count: 1 });
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ecs-tasks"] });
      onClose();
    },
  });

  const clusterOptions = (clusters.data ?? []).map((arn) => ({ label: shortName(arn), value: arn }));
  const familyOptions = (families.data ?? []).map((name) => ({ label: name, value: name }));

  return (
    <AwsModal
      title="Run new task"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ecs-run-task-submit"
            disabled={!valid || run.isPending}
            onClick={() => run.mutate()}
          >
            {run.isPending ? "Running…" : "Run task"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField
          label="Cluster"
          description="The cluster the task runs in."
          errorText={clusters.isSuccess && clusterOptions.length === 0 ? "No clusters exist in this account and Region." : undefined}
        >
          <Select
            selectedOption={clusterOptions.find((option) => option.value === cluster) ?? null}
            options={clusterOptions}
            onChange={(event) => setCluster(event.detail.selectedOption.value ?? "")}
            placeholder="Choose a cluster"
            ariaLabel="Cluster"
            statusType={clusters.isLoading ? "loading" : clusters.isError ? "error" : "finished"}
            data-testid="ecs-run-task-cluster"
          />
        </FormField>
        <FormField label="Launch type">
          <Select
            selectedOption={LAUNCH_TYPES.find((option) => option.value === launchType) ?? LAUNCH_TYPES[0]}
            options={LAUNCH_TYPES}
            onChange={(event) => setLaunchType(event.detail.selectedOption.value as "EC2" | "FARGATE")}
            ariaLabel="Launch type"
            data-testid="ecs-run-task-launch-type"
          />
        </FormField>
        <FormField label="Task definition">
          <RadioGroup
            value={source}
            onChange={(event) => setSource(event.detail.value as "existing" | "new")}
            items={[
              { value: "existing", label: "Use an existing task definition" },
              { value: "new", label: "Define a new task definition" },
            ]}
            ariaLabel="Task definition source"
          />
        </FormField>
        {source === "existing" ? (
          <FormField
            label="Family"
            errorText={
              families.isSuccess && familyOptions.length === 0
                ? "No task definitions exist. Define a new one instead."
                : undefined
            }
          >
            <Select
              selectedOption={familyOptions.find((option) => option.value === family) ?? null}
              options={familyOptions}
              onChange={(event) => setFamily(event.detail.selectedOption.value ?? "")}
              placeholder="Choose a task definition family"
              ariaLabel="Task definition family"
              statusType={families.isLoading ? "loading" : families.isError ? "error" : "finished"}
              data-testid="ecs-run-task-family"
            />
          </FormField>
        ) : (
          <>
            <FormField label="Family name" description="A name for the new task definition.">
              <Input
                value={newFamily}
                onChange={(event) => setNewFamily(event.detail.value)}
                nativeInputAttributes={{ "data-testid": "ecs-run-task-new-family" }}
              />
            </FormField>
            <FormField label="Container image">
              <Input
                value={image}
                onChange={(event) => setImage(event.detail.value)}
                placeholder="public.ecr.aws/docker/library/nginx:latest"
                nativeInputAttributes={{ "data-testid": "ecs-run-task-image" }}
              />
            </FormField>
            <FormField
              label={fargate ? "Task CPU" : "Task CPU (optional)"}
              description="CPU units. Required for Fargate."
            >
              <Input
                value={cpu}
                onChange={(event) => setCpu(event.detail.value)}
                placeholder="256"
                nativeInputAttributes={{ "data-testid": "ecs-run-task-cpu" }}
              />
            </FormField>
            <FormField
              label={fargate ? "Task memory" : "Task memory (optional)"}
              description="Memory in MiB. Required for Fargate."
            >
              <Input
                value={memory}
                onChange={(event) => setMemory(event.detail.value)}
                placeholder="512"
                nativeInputAttributes={{ "data-testid": "ecs-run-task-memory" }}
              />
            </FormField>
          </>
        )}
        {run.isError && (
          <AwsErrorAlert>
            <strong>Could not run the task.</strong>{" "}
            {run.error instanceof Error ? run.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

// Amazon Elastic Container Service — Tasks. The list and the header action
// both go through the real ECS awsjson1.1 API (DescribeTasks feeding the
// table, StopTask for the action) with the operator's federated credentials.
// Real ECS never deletes a task record on request — a task is stopped, and
// the service reaps the STOPPED record on its own schedule — so the action
// here is Stop, matching what the real console's task Actions menu offers.

const columns: AwsColumn<ECSTask>[] = [
  {
    id: "taskArn",
    header: "Task ARN",
    cell: (row) => <AwsRowLink to={`/ui/ecs/${encodeURIComponent(row.taskArn)}`}>{row.taskArn}</AwsRowLink>,
    value: (row) => row.taskArn,
  },
  { id: "status", header: "Last status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "clusterArn", header: "Cluster", cell: (row) => row.clusterArn, value: (row) => row.clusterArn },
  { id: "launchType", header: "Launch type", cell: (row) => row.launchType, value: (row) => row.launchType },
  { id: "cpu", header: "CPU", cell: (row) => row.cpu, value: (row) => row.cpu },
  { id: "memory", header: "Memory", cell: (row) => row.memory, value: (row) => row.memory },
];

// The states StopTask can still act on. A task already STOPPED or tearing
// down (DEPROVISIONING) has nothing left to stop, the same restriction the
// real console's Actions menu enforces by disabling Stop for that selection.
export function isStoppable(task: ECSTask): boolean {
  return task.status !== "STOPPED" && task.status !== "DEPROVISIONING";
}

export function StopTasksModal({
  tasks,
  onClose,
  clearSelection,
}: {
  tasks: ECSTask[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    // StopTask is per-task on the wire; a failure part-way surfaces as the
    // real API error, with the already-stopped tasks reflected once the list
    // refetches.
    mutationFn: async () => {
      for (const task of tasks) {
        await stopECSTask(task.clusterArn, task.taskArn);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ecs-tasks"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={tasks.length === 1 ? "Stop this task?" : `Stop ${tasks.length} tasks?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ecs-stop-task-confirm"
            disabled={stop.isPending}
            onClick={() => stop.mutate()}
          >
            {stop.isPending ? "Stopping…" : "Stop"}
          </AwsButton>
        </>
      }
    >
      <p>
        Stopping a task sends its containers a SIGTERM and, after the stop timeout elapses, a SIGKILL. The task's
        status moves to STOPPED and it stops billing.
      </p>
      <ul>
        {tasks.map((task) => (
          <li key={task.taskArn}>
            <code>{task.taskArn}</code>
          </li>
        ))}
      </ul>
      {stop.isError && (
        <AwsErrorAlert>
          <strong>Could not stop the task.</strong>{" "}
          {stop.error instanceof Error ? stop.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function ECSTasksPage() {
  const navigate = useNavigate();
  const [running, setRunning] = useState(false);
  const [stopping, setStopping] = useState<{ tasks: ECSTask[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <SpaceBetween size="l">
        <AwsResourceTable<ECSTask>
          title="Tasks"
          description="Tasks running in this account and Region."
          columns={columns}
          queryKey={["ecs-tasks"]}
          queryFn={fetchECSTasks}
          filterPlaceholder="Find tasks"
          emptyTitle="No tasks"
          emptyDescription="No tasks are running in this account and Region."
          rowKey={(row) => row.taskArn}
          tableTestId="ecs-tasks-table"
          actions={({ selected, clearSelection, refetch, isFetching }) => (
            <>
              <AwsButton
                data-testid="ecs-view-task"
                disabled={selected.length !== 1}
                onClick={() => navigate(`/ui/ecs/${encodeURIComponent(selected[0].taskArn)}`)}
              >
                View details
              </AwsButton>
              <AwsButton
                data-testid="ecs-stop-task"
                disabled={selected.length === 0 || !selected.every(isStoppable)}
                onClick={() => setStopping({ tasks: selected, clearSelection })}
              >
                Stop
              </AwsButton>
              <AwsButton onClick={refetch} disabled={isFetching}>
                {isFetching ? "Refreshing…" : "Refresh"}
              </AwsButton>
              <AwsButton variant="primary" data-testid="ecs-run-task" onClick={() => setRunning(true)}>
                Run new task
              </AwsButton>
            </>
          )}
        />
        <ECSServicesPage />
      </SpaceBetween>
      {running && <RunTaskModal onClose={() => setRunning(false)} />}
      {stopping && (
        <StopTasksModal tasks={stopping.tasks} clearSelection={stopping.clearSelection} onClose={() => setStopping(null)} />
      )}
    </>
  );
}
