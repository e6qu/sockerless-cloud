import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router";
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
import { formatTimestamp } from "../console/format.js";
import { fetchEC2Instance, type EC2Instance } from "../api.js";
import { InstanceStateModal } from "./EC2InstancesPage.js";

// Amazon EC2 — Instance detail. DescribeInstances narrowed to one instance ID
// (EC2 has no singular GetInstance operation), rendered as the summary block
// and task-oriented tabs the real console's instance page uses.

function DetailsTab({ instance }: { instance: EC2Instance }) {
  return (
    <AwsContainer>
      <AwsKeyValue
        ariaLabel="Instance details"
        items={[
          { label: "Instance ID", value: instance.instanceId },
          { label: "Instance state", value: <AwsStatus status={instance.state} /> },
          { label: "Instance type", value: instance.instanceType },
          { label: "AMI ID", value: instance.imageId || "–" },
          { label: "Architecture", value: instance.architecture || "–" },
          { label: "Key pair name", value: instance.keyName || "–" },
          { label: "Launch time", value: formatTimestamp(instance.launchTime) },
          { label: "Availability Zone", value: instance.availabilityZone || "–" },
        ]}
      />
    </AwsContainer>
  );
}

function NetworkingTab({ instance }: { instance: EC2Instance }) {
  return (
    <AwsContainer>
      <AwsKeyValue
        ariaLabel="Instance networking"
        items={[
          { label: "Private IPv4 address", value: instance.privateIpAddress || "–" },
          { label: "Public IPv4 address", value: instance.publicIpAddress || "–" },
          { label: "VPC ID", value: instance.vpcId || "–" },
          { label: "Subnet ID", value: instance.subnetId || "–" },
        ]}
      />
    </AwsContainer>
  );
}

function SecurityTab({ instance }: { instance: EC2Instance }) {
  return (
    <AwsContainer>
      {instance.securityGroups.length === 0 ? (
        <AwsEmptyState title="No security groups" description="This instance has no security groups attached." />
      ) : (
        <AwsKeyValue
          ariaLabel="Instance security groups"
          columns={1}
          items={instance.securityGroups.map((group) => ({
            label: group.groupId,
            value: group.groupName,
          }))}
        />
      )}
    </AwsContainer>
  );
}

export function EC2InstanceDetailPage() {
  const { instanceId = "" } = useParams();
  const [pending, setPending] = useState<"stop" | "terminate" | null>(null);
  const instance = useQuery({ queryKey: ["ec2-instance", instanceId], queryFn: () => fetchEC2Instance(instanceId) });

  return (
    <>
      <AwsPageHeader
        title={instanceId}
        description="EC2 instance in this account and Region."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton data-testid="ec2-instance-stop" disabled={!instance.isSuccess} onClick={() => setPending("stop")}>
              Stop
            </AwsButton>
            <AwsButton
              data-testid="ec2-instance-terminate"
              disabled={!instance.isSuccess}
              onClick={() => setPending("terminate")}
            >
              Terminate
            </AwsButton>
          </SpaceBetween>
        }
      />
      {instance.isError ? (
        <AwsContainer>
          <AwsErrorAlert testId="ec2-instance-error">
            <strong>Could not load the instance.</strong>{" "}
            {instance.error instanceof Error ? instance.error.message : "The request failed."}
          </AwsErrorAlert>
        </AwsContainer>
      ) : instance.isLoading ? (
        <AwsContainer>
          <AwsEmptyState title="Loading instance…" loading />
        </AwsContainer>
      ) : instance.data ? (
        <div data-testid="ec2-instance-summary">
          <AwsTabs
            ariaLabel="Instance details"
            tabs={[
              { id: "details", label: "Details", content: <DetailsTab instance={instance.data} /> },
              { id: "networking", label: "Networking", content: <NetworkingTab instance={instance.data} /> },
              { id: "security", label: "Security", content: <SecurityTab instance={instance.data} /> },
            ]}
          />
        </div>
      ) : null}
      {pending && instance.data && (
        <InstanceStateModal
          action={pending}
          instances={[instance.data]}
          clearSelection={() => void instance.refetch()}
          onClose={() => setPending(null)}
        />
      )}
    </>
  );
}
