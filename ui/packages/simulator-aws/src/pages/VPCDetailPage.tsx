import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import {
  fetchEC2SecurityGroups,
  fetchEC2Subnets,
  fetchEC2Vpcs,
  type EC2SecurityGroup,
  type EC2Subnet,
} from "../api.js";

// Amazon VPC — VPC detail. DescribeVpcs for the summary (there is no singular
// GetVpc operation), then DescribeSubnets and DescribeSecurityGroups filtered
// by `vpc-id` — the same three reads the real console's VPC detail page makes.

const subnetColumns: AwsColumn<EC2Subnet>[] = [
  { id: "subnetId", header: "Subnet ID", cell: (row) => row.subnetId, value: (row) => row.subnetId },
  { id: "name", header: "Name", cell: (row) => row.name || "–", value: (row) => row.name },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "cidrBlock", header: "IPv4 CIDR", cell: (row) => row.cidrBlock, value: (row) => row.cidrBlock },
  {
    id: "availabilityZone",
    header: "Availability Zone",
    cell: (row) => row.availabilityZone,
    value: (row) => row.availabilityZone,
  },
  {
    id: "availableIpAddressCount",
    header: "Available IPv4 addresses",
    cell: (row) => String(row.availableIpAddressCount),
    value: (row) => String(row.availableIpAddressCount),
  },
];

const securityGroupColumns: AwsColumn<EC2SecurityGroup>[] = [
  { id: "groupId", header: "Security group ID", cell: (row) => row.groupId, value: (row) => row.groupId },
  { id: "groupName", header: "Security group name", cell: (row) => row.groupName, value: (row) => row.groupName },
  { id: "description", header: "Description", cell: (row) => row.description, value: (row) => row.description },
];

export function VPCDetailPage() {
  const { vpcId = "" } = useParams();
  const vpcs = useQuery({ queryKey: ["ec2-vpcs"], queryFn: fetchEC2Vpcs });
  const vpc = vpcs.data?.find((candidate) => candidate.vpcId === vpcId);

  return (
    <>
      <AwsPageHeader title={vpcId} description="Virtual private cloud in this account and Region." />
      <AwsContainer>
        {vpcs.isError ? (
          <AwsErrorAlert testId="vpc-detail-error">
            <strong>Could not load the VPC.</strong>{" "}
            {vpcs.error instanceof Error ? vpcs.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : vpcs.isLoading ? (
          <AwsEmptyState title="Loading VPC…" loading />
        ) : vpc ? (
          <div data-testid="vpc-detail-summary">
            <AwsKeyValue
              ariaLabel="VPC details"
              items={[
                { label: "State", value: <AwsStatus status={vpc.state} /> },
                { label: "IPv4 CIDR", value: vpc.cidrBlock },
                { label: "Default VPC", value: vpc.isDefault ? "Yes" : "No" },
                { label: "DHCP option set", value: vpc.dhcpOptionsId || "–" },
                { label: "Tenancy", value: vpc.instanceTenancy || "–" },
                { label: "Name", value: vpc.name || "–" },
              ]}
            />
          </div>
        ) : (
          <AwsEmptyState title="VPC not found" description={`No VPC named ${vpcId} exists in this account and Region.`} />
        )}
      </AwsContainer>
      <AwsResourceTable<EC2Subnet>
        title="Subnets"
        headingVariant="h2"
        description="Subnets in this VPC."
        columns={subnetColumns}
        queryKey={["ec2-subnets", vpcId]}
        queryFn={() => fetchEC2Subnets(vpcId)}
        filterPlaceholder="Find subnets"
        emptyTitle="No subnets"
        emptyDescription="This VPC has no subnets."
        rowKey={(row) => row.subnetId}
        tableTestId="vpc-subnets-table"
        errorTestId="vpc-subnets-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      <AwsResourceTable<EC2SecurityGroup>
        title="Security groups"
        headingVariant="h2"
        description="Security groups in this VPC."
        columns={securityGroupColumns}
        queryKey={["ec2-security-groups", vpcId]}
        queryFn={() => fetchEC2SecurityGroups(vpcId)}
        filterPlaceholder="Find security groups"
        emptyTitle="No security groups"
        emptyDescription="This VPC has no security groups."
        rowKey={(row) => row.groupId}
        tableTestId="vpc-security-groups-table"
        errorTestId="vpc-security-groups-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
    </>
  );
}
