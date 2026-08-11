import { useNavigate } from "react-router";
import { AwsButton, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { fetchECSServices, type ECSService } from "../api.js";

function shortName(arn: string): string {
  const slash = arn.lastIndexOf("/");
  return slash === -1 ? arn : arn.slice(slash + 1);
}

const columns: AwsColumn<ECSService>[] = [
  {
    id: "serviceName",
    header: "Service name",
    cell: (service) => service.serviceName,
    value: (service) => service.serviceName,
  },
  {
    id: "cluster",
    header: "Cluster",
    cell: (service) => shortName(service.clusterArn),
    value: (service) => service.clusterArn,
  },
  {
    id: "status",
    header: "Status",
    cell: (service) => <AwsStatus status={service.status} />,
    value: (service) => service.status,
  },
  {
    id: "desiredCount",
    header: "Desired tasks",
    cell: (service) => service.desiredCount,
    value: (service) => String(service.desiredCount),
  },
  {
    id: "runningCount",
    header: "Running tasks",
    cell: (service) => service.runningCount,
    value: (service) => String(service.runningCount),
  },
  {
    id: "deployments",
    header: "Deployments",
    cell: (service) => (
      <>
        {service.deployments.length} · <AwsStatus status={service.deployments[0]?.rolloutState ?? "UNKNOWN"} />
      </>
    ),
    value: (service) => `${service.deployments.length} ${service.deployments[0]?.rolloutState ?? ""}`,
  },
];

// Amazon Elastic Container Service services are read through ListServices and
// DescribeServices. The table exposes scheduler state that the task-only view
// cannot: desired/running convergence, deployment rollout, and service-owned
// task definitions.
export function ECSServicesPage() {
  const navigate = useNavigate();
  return (
    <AwsResourceTable<ECSService>
      title="Services"
      description="Long-running Amazon ECS services and their deployment state."
      columns={columns}
      queryKey={["ecs-services"]}
      queryFn={fetchECSServices}
      filterPlaceholder="Find services"
      emptyTitle="No services"
      emptyDescription="No Amazon ECS services exist in this account and Region."
      rowKey={(service) => service.serviceArn}
      tableTestId="ecs-services-table"
      actions={({ selected, refetch, isFetching }) => (
        <>
          <AwsButton
            data-testid="ecs-view-service"
            disabled={selected.length !== 1}
            onClick={() => {
              const service = selected[0];
              navigate(
                `/ui/ecs/services/${encodeURIComponent(shortName(service.clusterArn))}/${encodeURIComponent(service.serviceName)}`,
              );
            }}
          >
            View service
          </AwsButton>
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        </>
      )}
    />
  );
}
