import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  fetchEC2Instances,
  fetchEC2Volumes,
  rebootEC2Instances,
  startEC2Instances,
  stopEC2Instances,
  terminateEC2Instances,
  type EC2Instance,
  type EC2Volume,
} from "../api.js";

// Amazon Elastic Compute Cloud (EC2) — Instances. The table is the real
// DescribeInstances read (the Query protocol, Version 2016-11-15, flattened
// across reservations the way the real console's Instances table flattens
// them), and the Instance state actions are the real StartInstances,
// StopInstances, RebootInstances, and TerminateInstances operations.

const columns: AwsColumn<EC2Instance>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => row.name || "–",
    value: (row) => row.name,
  },
  {
    id: "instanceId",
    header: "Instance ID",
    cell: (row) => (
      <AwsRowLink to={`/ui/ec2/${encodeURIComponent(row.instanceId)}`}>{row.instanceId}</AwsRowLink>
    ),
    value: (row) => row.instanceId,
  },
  {
    id: "state",
    header: "Instance state",
    cell: (row) => <AwsStatus status={row.state} />,
    value: (row) => row.state,
  },
  { id: "instanceType", header: "Instance type", cell: (row) => row.instanceType, value: (row) => row.instanceType },
  {
    id: "privateIpAddress",
    header: "Private IPv4 address",
    cell: (row) => row.privateIpAddress || "–",
    value: (row) => row.privateIpAddress,
  },
  {
    id: "availabilityZone",
    header: "Availability Zone",
    cell: (row) => row.availabilityZone,
    value: (row) => row.availabilityZone,
  },
  {
    id: "launchTime",
    header: "Launch time",
    cell: (row) => formatTimestamp(row.launchTime),
    value: (row) => row.launchTime,
  },
];

// Amazon Elastic Block Store volumes live in the same EC2 API, and the real
// console lists them under EC2's own navigation — DescribeVolumes.
const volumeColumns: AwsColumn<EC2Volume>[] = [
  { id: "volumeId", header: "Volume ID", cell: (row) => row.volumeId, value: (row) => row.volumeId },
  { id: "state", header: "Volume state", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "size", header: "Size", cell: (row) => `${row.size} GiB`, value: (row) => String(row.size) },
  { id: "volumeType", header: "Volume type", cell: (row) => row.volumeType, value: (row) => row.volumeType },
  {
    id: "availabilityZone",
    header: "Availability Zone",
    cell: (row) => row.availabilityZone,
    value: (row) => row.availabilityZone,
  },
  {
    id: "encrypted",
    header: "Encrypted",
    cell: (row) => (row.encrypted ? "Yes" : "No"),
    value: (row) => (row.encrypted ? "Yes" : "No"),
  },
  {
    id: "createTime",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime),
    value: (row) => row.createTime,
  },
];

type InstanceAction = "start" | "stop" | "reboot" | "terminate";

const ACTION_LABELS: Record<InstanceAction, { title: string; verb: string; body: string }> = {
  start: { title: "Start instances", verb: "Start", body: "Starting a stopped instance resumes it on new host hardware." },
  stop: { title: "Stop instances", verb: "Stop", body: "Stopping an instance shuts down the guest operating system; its root volume and its instance ID are kept." },
  reboot: { title: "Reboot instances", verb: "Reboot", body: "Rebooting is equivalent to an operating-system restart; the instance keeps its public IPv4 address and its instance store." },
  terminate: { title: "Terminate instances", verb: "Terminate", body: "Terminating an instance is permanent. You cannot start a terminated instance again." },
};

const RUN_ACTION: Record<InstanceAction, (ids: string[]) => Promise<void>> = {
  start: startEC2Instances,
  stop: stopEC2Instances,
  reboot: rebootEC2Instances,
  terminate: terminateEC2Instances,
};

export function InstanceStateModal({
  action,
  instances,
  onClose,
  clearSelection,
}: {
  action: InstanceAction;
  instances: EC2Instance[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const labels = ACTION_LABELS[action];
  const change = useMutation({
    mutationFn: () => RUN_ACTION[action](instances.map((instance) => instance.instanceId)),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ec2-instances"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={labels.title}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="ec2-instance-state-confirm"
            disabled={change.isPending}
            onClick={() => change.mutate()}
          >
            {change.isPending ? `${labels.verb}ing…` : labels.verb}
          </AwsButton>
        </>
      }
    >
      <p>{labels.body}</p>
      <ul>
        {instances.map((instance) => (
          <li key={instance.instanceId}>
            <code>{instance.instanceId}</code>
            {instance.name ? ` (${instance.name})` : ""}
          </li>
        ))}
      </ul>
      {change.isError && (
        <AwsErrorAlert>
          <strong>Could not {labels.verb.toLowerCase()} the instances.</strong>{" "}
          {change.error instanceof Error ? change.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function EC2InstancesPage() {
  const navigate = useNavigate();
  const [pending, setPending] = useState<{
    action: InstanceAction;
    instances: EC2Instance[];
    clearSelection: () => void;
  } | null>(null);
  return (
    <>
      <AwsResourceTable<EC2Instance>
        title="Instances"
        description="EC2 instances in this account and Region."
        columns={columns}
        queryKey={["ec2-instances"]}
        queryFn={fetchEC2Instances}
        filterPlaceholder="Find instances"
        emptyTitle="No instances"
        emptyDescription="No EC2 instances exist in this account and Region."
        rowKey={(row) => row.instanceId}
        tableTestId="ec2-instances-table"
        errorTestId="ec2-instances-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="ec2-view-instance"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/ec2/${encodeURIComponent(selected[0].instanceId)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="ec2-start-instance"
              disabled={selected.length === 0}
              onClick={() => setPending({ action: "start", instances: selected, clearSelection })}
            >
              Start
            </AwsButton>
            <AwsButton
              data-testid="ec2-stop-instance"
              disabled={selected.length === 0}
              onClick={() => setPending({ action: "stop", instances: selected, clearSelection })}
            >
              Stop
            </AwsButton>
            <AwsButton
              data-testid="ec2-reboot-instance"
              disabled={selected.length === 0}
              onClick={() => setPending({ action: "reboot", instances: selected, clearSelection })}
            >
              Reboot
            </AwsButton>
            <AwsButton
              data-testid="ec2-terminate-instance"
              disabled={selected.length === 0}
              onClick={() => setPending({ action: "terminate", instances: selected, clearSelection })}
            >
              Terminate
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<EC2Volume>
        title="Volumes"
        headingVariant="h2"
        description="Amazon EBS volumes in this account and Region."
        columns={volumeColumns}
        queryKey={["ec2-volumes"]}
        queryFn={fetchEC2Volumes}
        filterPlaceholder="Find volumes"
        emptyTitle="No volumes"
        emptyDescription="No Amazon EBS volumes exist in this account and Region."
        rowKey={(row) => row.volumeId}
        tableTestId="ec2-volumes-table"
        errorTestId="ec2-volumes-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {pending && (
        <InstanceStateModal
          action={pending.action}
          instances={pending.instances}
          clearSelection={pending.clearSelection}
          onClose={() => setPending(null)}
        />
      )}
    </>
  );
}
