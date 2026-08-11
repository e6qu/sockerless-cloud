import { AwsButton, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { fetchLoadBalancers, fetchTargetGroups, type LoadBalancer, type TargetGroup } from "../api.js";

// Elastic Load Balancing — Load balancers and Target groups, the two resources
// the real console's navigation leads with. DescribeLoadBalancers and
// DescribeTargetGroups on the real Query API (Version 2015-12-01).

const loadBalancerColumns: AwsColumn<LoadBalancer>[] = [
  { id: "name", header: "Name", cell: (row) => row.loadBalancerName, value: (row) => row.loadBalancerName },
  { id: "dnsName", header: "DNS name", cell: (row) => row.dnsName, value: (row) => row.dnsName },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "scheme", header: "Scheme", cell: (row) => row.scheme, value: (row) => row.scheme },
  { id: "vpcId", header: "VPC ID", cell: (row) => row.vpcId || "–", value: (row) => row.vpcId },
  {
    id: "availabilityZones",
    header: "Availability Zones",
    cell: (row) => row.availabilityZones.join(", ") || "–",
    value: (row) => row.availabilityZones.join(", "),
  },
];

const targetGroupColumns: AwsColumn<TargetGroup>[] = [
  { id: "name", header: "Name", cell: (row) => row.targetGroupName, value: (row) => row.targetGroupName },
  { id: "protocol", header: "Protocol", cell: (row) => row.protocol, value: (row) => row.protocol },
  { id: "port", header: "Port", cell: (row) => String(row.port), value: (row) => String(row.port) },
  { id: "targetType", header: "Target type", cell: (row) => row.targetType, value: (row) => row.targetType },
  { id: "vpcId", header: "VPC ID", cell: (row) => row.vpcId || "–", value: (row) => row.vpcId },
  {
    id: "healthCheckPath",
    header: "Health check path",
    cell: (row) => row.healthCheckPath || "–",
    value: (row) => row.healthCheckPath,
  },
];

export function LoadBalancersPage() {
  return (
    <>
      <AwsResourceTable<LoadBalancer>
        title="Load balancers"
        description="Application, Network, and Gateway Load Balancers in this account and Region."
        columns={loadBalancerColumns}
        queryKey={["elb-load-balancers"]}
        queryFn={fetchLoadBalancers}
        filterPlaceholder="Find load balancers"
        emptyTitle="No load balancers"
        emptyDescription="No load balancers exist in this account and Region."
        rowKey={(row) => row.loadBalancerArn || row.loadBalancerName}
        tableTestId="elb-load-balancers-table"
        errorTestId="elb-load-balancers-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      <AwsResourceTable<TargetGroup>
        title="Target groups"
        headingVariant="h2"
        description="The target groups listeners forward to."
        columns={targetGroupColumns}
        queryKey={["elb-target-groups"]}
        queryFn={fetchTargetGroups}
        filterPlaceholder="Find target groups"
        emptyTitle="No target groups"
        emptyDescription="No target groups exist in this account and Region."
        rowKey={(row) => row.targetGroupArn || row.targetGroupName}
        tableTestId="elb-target-groups-table"
        errorTestId="elb-target-groups-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
    </>
  );
}
