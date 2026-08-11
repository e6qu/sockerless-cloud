import { useMemo, useState } from "react";
import { useParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import Table from "@cloudscape-design/components/table";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsStatus,
  AwsTabs,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import { fetchECSTaskDefinition, fetchECSTaskDetail, type ECSTask } from "../api.js";
import { isStoppable, StopTasksModal } from "./ECSTasksPage.js";

// Amazon Elastic Container Service — Task detail. Reads the real ECS
// awsjson1.1 API (DescribeTasks for the task, DescribeTaskDefinition for the
// task definition it runs) with the operator's federated credentials, and
// reuses the Tasks list page's real StopTask action.

function shortArn(arn: string): string {
  const slash = arn.lastIndexOf("/");
  return slash === -1 ? arn : arn.slice(slash + 1);
}

export function ECSTaskDetailPage() {
  const { taskArn = "" } = useParams();
  const queryClient = useQueryClient();
  const [stopping, setStopping] = useState(false);

  const task = useQuery({ queryKey: ["ecs-task", taskArn], queryFn: () => fetchECSTaskDetail(taskArn) });
  const taskDefinition = useQuery({
    queryKey: ["ecs-task-definition", task.data?.taskDefinitionArn],
    queryFn: () => fetchECSTaskDefinition(task.data!.taskDefinitionArn),
    enabled: Boolean(task.data?.taskDefinitionArn),
  });

  const containersWithImage = useMemo(() => {
    const images = new Map((taskDefinition.data?.containerDefinitions ?? []).map((c) => [c.name, c.image]));
    return (task.data?.containers ?? []).map((container) => ({
      ...container,
      image: images.get(container.name) || container.image,
    }));
  }, [task.data, taskDefinition.data]);

  const asECSTask: ECSTask | null = task.data
    ? {
        taskArn: task.data.taskArn,
        status: task.data.status,
        clusterArn: task.data.clusterArn,
        launchType: task.data.launchType,
        cpu: task.data.cpu,
        memory: task.data.memory,
      }
    : null;

  return (
    <>
      <AwsPageHeader
        title={taskArn}
        description="Task in Amazon Elastic Container Service."
        actions={
          <AwsButton
            data-testid="ecs-task-stop"
            disabled={!asECSTask || !isStoppable(asECSTask)}
            onClick={() => setStopping(true)}
          >
            Stop
          </AwsButton>
        }
      />
      <AwsContainer>
        {task.isError ? (
          <AwsErrorAlert testId="ecs-task-error">
            <strong>Could not load the task.</strong>{" "}
            {task.error instanceof Error ? task.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : task.isLoading ? (
          <AwsEmptyState title="Loading task…" loading />
        ) : task.data ? (
          <>
            <div data-testid="ecs-task-summary">
              <AwsKeyValue
                items={[
                  { label: "Last status", value: <AwsStatus status={task.data.status} /> },
                  { label: "Desired status", value: task.data.desiredStatus || "–" },
                  { label: "Cluster", value: shortArn(task.data.clusterArn) || "–" },
                  { label: "Launch type", value: task.data.launchType || "–" },
                  { label: "Connectivity", value: task.data.connectivity || "–" },
                  { label: "CPU", value: task.data.cpu || "–" },
                  { label: "Memory", value: task.data.memory || "–" },
                  {
                    label: "Task definition",
                    value: taskDefinition.data ? `${taskDefinition.data.family}:${taskDefinition.data.revision}` : "–",
                  },
                  { label: "Created", value: task.data.createdAt ? formatEpoch(task.data.createdAt) : "–" },
                  { label: "Started", value: task.data.startedAt ? formatEpoch(task.data.startedAt) : "–" },
                  ...(task.data.stoppedAt
                    ? [
                        { label: "Stopped", value: formatEpoch(task.data.stoppedAt) },
                        { label: "Stop reason", value: task.data.stoppedReason || task.data.stopCode || "–" },
                      ]
                    : []),
                ]}
              />
            </div>
            <div style={{ marginTop: 20 }}>
              <AwsTabs
                ariaLabel="Task detail"
                tabs={[
                  {
                    id: "containers",
                    label: "Containers",
                    content: (
                      <div data-testid="ecs-task-containers">
                        <Table
                          variant="embedded"
                          ariaLabels={{ tableLabel: "Containers" }}
                          items={containersWithImage}
                          columnDefinitions={[
                            { id: "name", header: "Container name", cell: (container) => container.name },
                            { id: "image", header: "Image", cell: (container) => container.image || "–" },
                            {
                              id: "status",
                              header: "Status",
                              cell: (container) => <AwsStatus status={container.lastStatus} />,
                            },
                            { id: "exitCode", header: "Exit code", cell: (container) => container.exitCode ?? "–" },
                            {
                              id: "privateIp",
                              header: "Private IP",
                              cell: (container) => container.privateIpv4Address ?? "–",
                            },
                          ]}
                          empty={<AwsEmptyState title="No containers" description="This task reports no containers." />}
                        />
                      </div>
                    ),
                  },
                  {
                    id: "network",
                    label: "Network",
                    content: (
                      <div data-testid="ecs-task-network">
                        <SpaceBetween size="l">
                          {task.data.networkConfiguration && (
                            <AwsKeyValue
                              items={[
                                { label: "Subnets", value: task.data.networkConfiguration.subnets.join(", ") || "–" },
                                {
                                  label: "Security groups",
                                  value: task.data.networkConfiguration.securityGroups.join(", ") || "–",
                                },
                                { label: "Assign public IP", value: task.data.networkConfiguration.assignPublicIp || "–" },
                              ]}
                            />
                          )}
                          {task.data.attachments.length === 0 ? (
                            <AwsEmptyState
                              title="No network attachments"
                              description="This task has no elastic network interface attachments."
                            />
                          ) : (
                            task.data.attachments.map((attachment) => (
                              <div key={attachment.id}>
                                <Header variant="h3">{attachment.type || "Attachment"}</Header>
                                <AwsKeyValue
                                  items={[
                                    { label: "Status", value: <AwsStatus status={attachment.status} /> },
                                    ...attachment.details.map((detail) => ({ label: detail.name, value: detail.value })),
                                  ]}
                                />
                              </div>
                            ))
                          )}
                        </SpaceBetween>
                      </div>
                    ),
                  },
                  {
                    id: "task-definition",
                    label: "Task definition",
                    content: taskDefinition.isError ? (
                      <AwsErrorAlert>
                        <strong>Could not load the task definition.</strong>{" "}
                        {taskDefinition.error instanceof Error ? taskDefinition.error.message : "The request failed."}
                      </AwsErrorAlert>
                    ) : taskDefinition.isLoading || !taskDefinition.data ? (
                      <AwsEmptyState title="Loading task definition…" loading />
                    ) : (
                      <div data-testid="ecs-task-definition">
                        <SpaceBetween size="l">
                          <AwsKeyValue
                            items={[
                              { label: "Family : revision", value: `${taskDefinition.data.family}:${taskDefinition.data.revision}` },
                              { label: "Network mode", value: taskDefinition.data.networkMode || "–" },
                              {
                                label: "Requires compatibilities",
                                value: taskDefinition.data.requiresCompatibilities.join(", ") || "–",
                              },
                              { label: "Task CPU", value: taskDefinition.data.cpu || "–" },
                              { label: "Task memory", value: taskDefinition.data.memory || "–" },
                              { label: "Task role", value: taskDefinition.data.taskRoleArn || "–" },
                              { label: "Execution role", value: taskDefinition.data.executionRoleArn || "–" },
                            ]}
                          />
                          {taskDefinition.data.containerDefinitions.map((container) => (
                            <div key={container.name}>
                              <Header variant="h3">{container.name}</Header>
                              <AwsKeyValue
                                items={[
                                  { label: "Image", value: container.image },
                                  { label: "Essential", value: container.essential ? "Yes" : "No" },
                                  ...(container.cpu !== undefined ? [{ label: "CPU units", value: String(container.cpu) }] : []),
                                  ...(container.memory !== undefined
                                    ? [{ label: "Memory hard limit", value: `${container.memory} MiB` }]
                                    : []),
                                  ...(container.memoryReservation !== undefined
                                    ? [{ label: "Memory soft limit", value: `${container.memoryReservation} MiB` }]
                                    : []),
                                  ...(container.entryPoint.length > 0
                                    ? [{ label: "Entry point", value: <code>{container.entryPoint.join(" ")}</code> }]
                                    : []),
                                  ...(container.command.length > 0
                                    ? [{ label: "Command", value: <code>{container.command.join(" ")}</code> }]
                                    : []),
                                  ...(container.portMappings.length > 0
                                    ? [
                                        {
                                          label: "Port mappings",
                                          value: container.portMappings
                                            .map((mapping) =>
                                              mapping.hostPort
                                                ? `${mapping.hostPort}:${mapping.containerPort}/${mapping.protocol ?? "tcp"}`
                                                : `${mapping.containerPort}/${mapping.protocol ?? "tcp"}`,
                                            )
                                            .join(", "),
                                        },
                                      ]
                                    : []),
                                  ...(container.logDriver ? [{ label: "Log driver", value: container.logDriver }] : []),
                                  ...(container.environment.length > 0
                                    ? [
                                        {
                                          label: "Environment variables",
                                          value: (
                                            <ul>
                                              {container.environment.map((entry) => (
                                                <li key={entry.name}>
                                                  <code>
                                                    {entry.name}={entry.value}
                                                  </code>
                                                </li>
                                              ))}
                                            </ul>
                                          ),
                                        },
                                      ]
                                    : []),
                                ]}
                              />
                            </div>
                          ))}
                        </SpaceBetween>
                      </div>
                    ),
                  },
                ]}
              />
            </div>
          </>
        ) : null}
      </AwsContainer>
      {stopping && task.data && asECSTask && (
        <StopTasksModal
          tasks={[asECSTask]}
          clearSelection={() => {}}
          onClose={() => {
            setStopping(false);
            void queryClient.invalidateQueries({ queryKey: ["ecs-task", taskArn] });
          }}
        />
      )}
    </>
  );
}
