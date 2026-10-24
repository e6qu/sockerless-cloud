import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ButtonDropdown from "@cloudscape-design/components/button-dropdown";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsRefreshButton,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  errorText,
  useNotify,
  type AwsColumn,
  type AwsStatusKind,
} from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  fetchEC2Instances,
  rebootEC2Instances,
  startEC2Instances,
  stopEC2Instances,
  terminateEC2Instances,
  type EC2Instance,
} from "../api.js";

// Amazon Elastic Compute Cloud (EC2) — Instances. The table is the real
// DescribeInstances read (the Query protocol, Version 2016-11-15, flattened
// across reservations the way the real console's Instances table flattens
// them), and the Instance state actions are the real StartInstances,
// StopInstances, RebootInstances, and TerminateInstances operations.

/** EC2's instance-state names, with the status indicator the real console
 * draws for each. */
const INSTANCE_STATE_KIND: Record<string, AwsStatusKind> = {
  pending: "warning",
  running: "success",
  "shutting-down": "warning",
  stopping: "warning",
  stopped: "inactive",
  terminated: "inactive",
};

export function InstanceState({ state }: { state: string }) {
  return <AwsStatus status={state ? state[0].toUpperCase() + state.slice(1) : "–"} kind={INSTANCE_STATE_KIND[state] ?? "inactive"} />;
}

const columns: AwsColumn<EC2Instance>[] = [
  { id: "name", header: "Name", cell: (row) => row.name || "–", value: (row) => row.name },
  {
    id: "instanceId",
    header: "Instance ID",
    cell: (row) => <AwsRowLink to={`/ui/ec2/${encodeURIComponent(row.instanceId)}`}>{row.instanceId}</AwsRowLink>,
    value: (row) => row.instanceId,
  },
  { id: "state", header: "Instance state", cell: (row) => <InstanceState state={row.state} />, value: (row) => row.state },
  { id: "instanceType", header: "Instance type", cell: (row) => row.instanceType, value: (row) => row.instanceType },
  { id: "availabilityZone", header: "Availability Zone", cell: (row) => row.availabilityZone, value: (row) => row.availabilityZone },
  { id: "publicIpAddress", header: "Public IPv4 address", cell: (row) => row.publicIpAddress || "–", value: (row) => row.publicIpAddress },
  { id: "privateIpAddress", header: "Private IPv4 address", cell: (row) => row.privateIpAddress || "–", value: (row) => row.privateIpAddress },
  {
    id: "securityGroups",
    header: "Security group name",
    cell: (row) => row.securityGroups.map((group) => group.groupName).join(", ") || "–",
    value: (row) => row.securityGroups.map((group) => group.groupName).join(", "),
  },
  { id: "keyName", header: "Key name", cell: (row) => row.keyName || "–", value: (row) => row.keyName },
  { id: "launchTime", header: "Launch time", cell: (row) => formatTimestamp(row.launchTime), value: (row) => row.launchTime },
];

type InstanceAction = "start" | "stop" | "reboot" | "terminate";

const ACTION_LABELS: Record<InstanceAction, { title: string; verb: string; done: string; body: string }> = {
  start: { title: "Start instance?", verb: "Start", done: "Successfully started", body: "Starting a stopped instance resumes it on new host hardware." },
  stop: {
    title: "Stop instance?",
    verb: "Stop",
    done: "Successfully stopped",
    body: "Stopping an instance shuts down the guest operating system; its root volume and its instance ID are kept.",
  },
  reboot: {
    title: "Reboot instance?",
    verb: "Reboot",
    done: "Successfully rebooted",
    body: "Rebooting is equivalent to an operating-system restart; the instance keeps its public IPv4 address and its instance store.",
  },
  terminate: {
    title: "Terminate (delete) instance?",
    verb: "Terminate (delete)",
    done: "Successfully initiated termination of",
    body: "Terminating an instance is permanent. You cannot start a terminated instance again.",
  },
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
  const notify = useNotify();
  const labels = ACTION_LABELS[action];
  const ids = instances.map((instance) => instance.instanceId);
  const change = useMutation({
    mutationFn: () => RUN_ACTION[action](ids),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["ec2-instances"] }),
    onSuccess: () => {
      notify("success", `${labels.done} ${ids.join(", ")}`);
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
          <AwsButton variant="link" onClick={onClose}>
            Cancel
          </AwsButton>
          <AwsButton variant="primary" data-testid="ec2-instance-state-confirm" loading={change.isPending} onClick={() => change.mutate()}>
            {labels.verb}
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
          <strong>Failed to {labels.verb.toLowerCase()} the instances.</strong> {errorText(change.error)}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

/** The Instance state menu the real console's Instances page carries. */
export function InstanceStateDropdown({
  selected,
  onAction,
}: {
  selected: EC2Instance[];
  onAction: (action: InstanceAction) => void;
}) {
  const none = selected.length === 0;
  const allIn = (...states: string[]) => !none && selected.every((instance) => states.includes(instance.state));
  return (
    <ButtonDropdown
      disabled={none}
      items={[
        { id: "stop", text: "Stop instance", disabled: !allIn("running", "pending") },
        { id: "start", text: "Start instance", disabled: !allIn("stopped") },
        { id: "reboot", text: "Reboot instance", disabled: !allIn("running") },
        { id: "terminate", text: "Terminate (delete) instance", disabled: allIn("terminated", "shutting-down") },
      ]}
      onItemClick={(event) => onAction(event.detail.id as InstanceAction)}
    >
      Instance state
    </ButtonDropdown>
  );
}

export function EC2InstancesPage() {
  const [pending, setPending] = useState<{
    action: InstanceAction;
    instances: EC2Instance[];
    clearSelection: () => void;
  } | null>(null);
  return (
    <>
      <AwsResourceTable<EC2Instance>
        title="Instances"
        columns={columns}
        queryKey={["ec2-instances"]}
        queryFn={fetchEC2Instances}
        filterPlaceholder="Find Instance by attribute or tag (case-sensitive)"
        emptyTitle="No instances"
        emptyDescription="You do not have any instances in this Region."
        rowKey={(row) => row.instanceId}
        tableTestId="ec2-instances-table"
        errorTestId="ec2-instances-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsRefreshButton onClick={refetch} loading={isFetching} />
            <InstanceStateDropdown
              selected={selected}
              onAction={(action) => setPending({ action, instances: selected, clearSelection })}
            />
          </>
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
