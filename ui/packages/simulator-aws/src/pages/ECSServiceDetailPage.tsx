import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsModal,
  AwsPageHeader,
  AwsStatus,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  deleteECSService,
  fetchECSService,
  updateECSServiceDesiredCount,
  type ECSService,
} from "../api.js";

function ScaleServiceModal({ service, onClose }: { service: ECSService; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [desiredCount, setDesiredCount] = useState(String(service.desiredCount));
  const parsed = Number(desiredCount);
  const valid = Number.isInteger(parsed) && parsed >= 0;
  const scale = useMutation({
    mutationFn: () => updateECSServiceDesiredCount(service.clusterArn, service.serviceName, parsed),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ecs-service", service.clusterArn, service.serviceName] });
      await queryClient.invalidateQueries({ queryKey: ["ecs-services"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title={`Update ${service.serviceName}`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton variant="primary" disabled={!valid || scale.isPending} onClick={() => scale.mutate()}>
            {scale.isPending ? "Updating…" : "Update"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>Amazon ECS starts or stops real service tasks until the running count reaches this desired count.</p>
        <FormField label="Desired tasks" errorText={!valid ? "Enter a whole number of zero or more." : undefined}>
          <Input
            type="number"
            value={desiredCount}
            onChange={(event) => setDesiredCount(event.detail.value)}
            nativeInputAttributes={{ min: 0, "data-testid": "ecs-service-desired-count" }}
          />
        </FormField>
        {scale.isError && (
          <AwsErrorAlert>
            <strong>Could not update the service.</strong>{" "}
            {scale.error instanceof Error ? scale.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function ECSServiceDetailPage() {
  const { cluster = "", serviceName = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [scaling, setScaling] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const service = useQuery({
    queryKey: ["ecs-service", cluster, serviceName],
    queryFn: () => fetchECSService(cluster, serviceName),
    refetchInterval: 2_000,
  });
  const remove = useMutation({
    mutationFn: () => deleteECSService(cluster, serviceName),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ecs-services"] });
      navigate("/ui/ecs");
    },
  });

  const current = service.data;
  const circuitBreaker = current?.deploymentConfiguration?.deploymentCircuitBreaker;
  const alarms = current?.deploymentConfiguration?.alarms;
  return (
    <>
      <AwsPageHeader
        title={serviceName}
        description="Amazon Elastic Container Service deployment, discovery, and scheduler state."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton disabled={!current || remove.isPending} onClick={() => setScaling(true)}>
              Update desired count
            </AwsButton>
            <AwsButton
              data-testid="ecs-service-delete"
              disabled={!current || remove.isPending}
              onClick={() => setConfirmingDelete(true)}
            >
              {remove.isPending ? "Deleting…" : "Delete service"}
            </AwsButton>
          </SpaceBetween>
        }
      />
      <SpaceBetween size="l">
        {remove.isError && (
          <AwsErrorAlert>
            <strong>Could not delete the service.</strong>{" "}
            {remove.error instanceof Error ? remove.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
        <AwsContainer>
          {service.isError ? (
            <AwsErrorAlert testId="ecs-service-error">
              <strong>Could not load the service.</strong>{" "}
              {service.error instanceof Error ? service.error.message : "The request failed."}
            </AwsErrorAlert>
          ) : service.isLoading || !current ? (
            <AwsEmptyState title="Loading service…" loading />
          ) : (
            <AwsKeyValue
              ariaLabel="Service details"
              items={[
                { label: "Status", value: <AwsStatus status={current.status} /> },
                { label: "Cluster", value: current.clusterArn },
                { label: "Task definition", value: current.taskDefinition },
                { label: "Tasks", value: `${current.runningCount} running, ${current.pendingCount} pending, ${current.desiredCount} desired` },
                { label: "Launch type", value: current.launchType || "Capacity provider strategy" },
                { label: "Platform version", value: current.platformVersion || "LATEST" },
              ]}
            />
          )}
        </AwsContainer>
        <Table
          header={<Header variant="h2">Deployments</Header>}
          items={current?.deployments ?? []}
          trackBy="id"
          columnDefinitions={[
            { id: "status", header: "Status", cell: (deployment) => deployment.status },
            {
              id: "rollout",
              header: "Rollout state",
              cell: (deployment) => <AwsStatus status={deployment.rolloutState} />,
            },
            { id: "taskDefinition", header: "Task definition", cell: (deployment) => deployment.taskDefinition },
            {
              id: "counts",
              header: "Tasks",
              cell: (deployment) => `${deployment.runningCount}/${deployment.desiredCount}`,
            },
            {
              id: "reason",
              header: "State reason",
              cell: (deployment) => deployment.rolloutStateReason || "–",
            },
          ]}
          empty={<AwsEmptyState title="No deployments" />}
        />
        <Table
          header={<Header variant="h2">Service events</Header>}
          items={current?.events ?? []}
          trackBy="id"
          columnDefinitions={[
            { id: "created", header: "Created", cell: (event) => formatEpoch(event.createdAt ?? 0) },
            { id: "message", header: "Message", cell: (event) => event.message },
          ]}
          empty={<AwsEmptyState title="No service events" />}
        />
        <AwsContainer>
          <Header variant="h2">Deployment configuration</Header>
          <AwsKeyValue
            ariaLabel="Deployment configuration"
            items={[
              {
                label: "Circuit breaker",
                value: circuitBreaker?.enable
                  ? `Enabled · rollback ${circuitBreaker.rollback ? "on" : "off"}${
                      circuitBreaker.thresholdConfiguration
                        ? ` · ${circuitBreaker.thresholdConfiguration.type} ${circuitBreaker.thresholdConfiguration.value}`
                        : ""
                    }`
                  : "Disabled",
              },
              {
                label: "Alarms",
                value: alarms?.enable ? (alarms.alarmNames ?? []).join(", ") || "Enabled" : "Disabled",
              },
            ]}
          />
        </AwsContainer>
        <AwsContainer>
          <Header variant="h2">Service discovery</Header>
          <AwsKeyValue
            ariaLabel="Service discovery"
            items={[
              {
                label: "AWS Cloud Map registries",
                value:
                  current?.serviceRegistries.map((registry) => registry.registryArn).filter(Boolean).join(", ") ||
                  "None",
              },
            ]}
          />
        </AwsContainer>
      </SpaceBetween>
      {scaling && current && <ScaleServiceModal service={current} onClose={() => setScaling(false)} />}
      {confirmingDelete && current && (
        <AwsModal
          title={`Delete ${current.serviceName}?`}
          onDismiss={() => setConfirmingDelete(false)}
          footer={
            <>
              <AwsButton onClick={() => setConfirmingDelete(false)}>Cancel</AwsButton>
              <AwsButton variant="primary" disabled={remove.isPending} onClick={() => remove.mutate()}>
                {remove.isPending ? "Deleting…" : "Delete"}
              </AwsButton>
            </>
          }
        >
          <p>
            Amazon ECS stops the service's running tasks and removes the service. This action cannot be undone.
          </p>
        </AwsModal>
      )}
    </>
  );
}
