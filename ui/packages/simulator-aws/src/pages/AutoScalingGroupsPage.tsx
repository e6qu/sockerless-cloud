import { AwsButton, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import { fetchAutoScalingGroups, type AutoScalingGroup } from "../api.js";

// Amazon EC2 Auto Scaling — Auto Scaling groups. DescribeAutoScalingGroups on
// the real Auto Scaling Query API (Version 2011-01-01).

const columns: AwsColumn<AutoScalingGroup>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  {
    id: "instances",
    header: "Instances",
    cell: (row) => String(row.instanceCount),
    value: (row) => String(row.instanceCount),
  },
  {
    id: "desiredCapacity",
    header: "Desired capacity",
    cell: (row) => String(row.desiredCapacity),
    value: (row) => String(row.desiredCapacity),
  },
  { id: "minSize", header: "Minimum capacity", cell: (row) => String(row.minSize), value: (row) => String(row.minSize) },
  { id: "maxSize", header: "Maximum capacity", cell: (row) => String(row.maxSize), value: (row) => String(row.maxSize) },
  {
    id: "availabilityZones",
    header: "Availability Zones",
    cell: (row) => row.availabilityZones.join(", ") || "–",
    value: (row) => row.availabilityZones.join(", "),
  },
  {
    id: "createdTime",
    header: "Created",
    cell: (row) => formatTimestamp(row.createdTime),
    value: (row) => row.createdTime,
  },
];

export function AutoScalingGroupsPage() {
  return (
    <AwsResourceTable<AutoScalingGroup>
      title="Auto Scaling groups"
      description="Amazon EC2 Auto Scaling groups in this account and Region."
      columns={columns}
      queryKey={["autoscaling-groups"]}
      queryFn={fetchAutoScalingGroups}
      filterPlaceholder="Find Auto Scaling groups"
      emptyTitle="No Auto Scaling groups"
      emptyDescription="No Auto Scaling groups exist in this account and Region."
      rowKey={(row) => row.name}
      tableTestId="autoscaling-groups-table"
      errorTestId="autoscaling-groups-error"
      actions={({ refetch, isFetching }) => (
        <AwsButton onClick={refetch} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      )}
    />
  );
}
