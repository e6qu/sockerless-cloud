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
import { formatBytes, formatEpoch } from "../console/format.js";
import { fetchEFSFileSystems, fetchEFSMountTargets, type EFSMountTarget } from "../api.js";

// Amazon EFS — File system detail. DescribeFileSystems for the summary and
// DescribeMountTargets for the network access table, the two reads the real
// console's file system page makes.

const mountTargetColumns: AwsColumn<EFSMountTarget>[] = [
  {
    id: "mountTargetId",
    header: "Mount target ID",
    cell: (row) => row.mountTargetId,
    value: (row) => row.mountTargetId,
  },
  {
    id: "lifeCycleState",
    header: "State",
    cell: (row) => <AwsStatus status={row.lifeCycleState} />,
    value: (row) => row.lifeCycleState,
  },
  {
    id: "availabilityZoneName",
    header: "Availability Zone",
    cell: (row) => row.availabilityZoneName || "–",
    value: (row) => row.availabilityZoneName,
  },
  { id: "subnetId", header: "Subnet ID", cell: (row) => row.subnetId, value: (row) => row.subnetId },
  { id: "ipAddress", header: "IP address", cell: (row) => row.ipAddress || "–", value: (row) => row.ipAddress },
];

export function EFSFileSystemDetailPage() {
  const { fileSystemId = "" } = useParams();
  const fileSystems = useQuery({ queryKey: ["efs-file-systems"], queryFn: fetchEFSFileSystems });
  const fileSystem = fileSystems.data?.find((candidate) => candidate.fileSystemId === fileSystemId);

  return (
    <>
      <AwsPageHeader title={fileSystemId} description="Amazon EFS file system in this account and Region." />
      <AwsContainer>
        {fileSystems.isError ? (
          <AwsErrorAlert testId="efs-file-system-error">
            <strong>Could not load the file system.</strong>{" "}
            {fileSystems.error instanceof Error ? fileSystems.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : fileSystems.isLoading ? (
          <AwsEmptyState title="Loading file system…" loading />
        ) : fileSystem ? (
          <div data-testid="efs-file-system-summary">
            <AwsKeyValue
              ariaLabel="File system details"
              items={[
                { label: "Name", value: fileSystem.name || "–" },
                { label: "State", value: <AwsStatus status={fileSystem.lifeCycleState} /> },
                { label: "Performance mode", value: fileSystem.performanceMode },
                { label: "Throughput mode", value: fileSystem.throughputMode },
                { label: "Size in EFS Standard", value: formatBytes(fileSystem.sizeInBytes) },
                { label: "Encrypted", value: fileSystem.encrypted ? "Yes" : "No" },
                { label: "Created", value: formatEpoch(fileSystem.creationTime) },
              ]}
            />
          </div>
        ) : (
          <AwsEmptyState
            title="File system not found"
            description={`No file system with ID ${fileSystemId} exists in this account and Region.`}
          />
        )}
      </AwsContainer>
      <AwsResourceTable<EFSMountTarget>
        title="Mount targets"
        headingVariant="h2"
        description="The network interfaces clients mount this file system through."
        columns={mountTargetColumns}
        queryKey={["efs-mount-targets", fileSystemId]}
        queryFn={() => fetchEFSMountTargets(fileSystemId)}
        filterPlaceholder="Find mount targets"
        emptyTitle="No mount targets"
        emptyDescription="This file system has no mount targets, so nothing can mount it yet."
        rowKey={(row) => row.mountTargetId}
        tableTestId="efs-mount-targets-table"
        errorTestId="efs-mount-targets-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
    </>
  );
}
