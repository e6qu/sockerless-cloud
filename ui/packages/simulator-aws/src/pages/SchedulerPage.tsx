import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import Textarea from "@cloudscape-design/components/textarea";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatTimestamp } from "../console/format.js";
import {
  createSchedule,
  createScheduleGroup,
  deleteSchedule,
  deleteScheduleGroup,
  fetchScheduleGroups,
  fetchSchedules,
  updateScheduleState,
  type Schedule,
  type ScheduleGroup,
} from "../api.js";

// Amazon EventBridge Scheduler — Schedules and Schedule groups. ListSchedules
// and ListScheduleGroups on the real Scheduler REST-JSON API.

const scheduleColumns: AwsColumn<Schedule>[] = [
  { id: "name", header: "Schedule name", cell: (row) => row.name, value: (row) => row.name },
  { id: "groupName", header: "Group", cell: (row) => row.groupName || "default", value: (row) => row.groupName },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "targetArn", header: "Target", cell: (row) => row.targetArn || "–", value: (row) => row.targetArn },
  {
    id: "creationDate",
    header: "Created",
    cell: (row) => formatTimestamp(row.creationDate),
    value: (row) => row.creationDate,
  },
];

const groupColumns: AwsColumn<ScheduleGroup>[] = [
  { id: "name", header: "Group name", cell: (row) => row.name, value: (row) => row.name },
  { id: "state", header: "State", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  {
    id: "creationDate",
    header: "Created",
    cell: (row) => formatTimestamp(row.creationDate),
    value: (row) => row.creationDate,
  },
];

function CreateScheduleModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [groupName, setGroupName] = useState("default");
  const [expression, setExpression] = useState("rate(5 minutes)");
  const [targetArn, setTargetArn] = useState("");
  const [roleArn, setRoleArn] = useState("");
  const [targetInput, setTargetInput] = useState("{}");
  const create = useMutation({
    mutationFn: () => createSchedule({ name, groupName, expression, targetArn, roleArn, targetInput }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["scheduler-schedules"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create schedule" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!name || !expression || !targetArn || !roleArn || create.isPending} onClick={() => create.mutate()}>Create schedule</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
        <FormField label="Schedule group"><Input value={groupName} onChange={(event) => setGroupName(event.detail.value)} /></FormField>
        <FormField label="Schedule expression"><Input value={expression} onChange={(event) => setExpression(event.detail.value)} /></FormField>
        <FormField label="Target ARN"><Input value={targetArn} onChange={(event) => setTargetArn(event.detail.value)} /></FormField>
        <FormField label="Execution role ARN"><Input value={roleArn} onChange={(event) => setRoleArn(event.detail.value)} /></FormField>
        <FormField label="Target input"><Textarea value={targetInput} onChange={(event) => setTargetInput(event.detail.value)} rows={5} /></FormField>
        {create.isError && <AwsErrorAlert>{create.error instanceof Error ? create.error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function CreateGroupModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => createScheduleGroup(name),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["scheduler-groups"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create schedule group" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Create group</AwsButton></>}>
      <FormField label="Name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
    </AwsModal>
  );
}

export function SchedulerPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [creatingGroup, setCreatingGroup] = useState(false);
  const state = useMutation({
    mutationFn: ({ schedules, enabled }: { schedules: Schedule[]; enabled: boolean }) =>
      Promise.all(schedules.map((schedule) => updateScheduleState(schedule, enabled ? "ENABLED" : "DISABLED"))),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["scheduler-schedules"] }),
  });
  const remove = useMutation({
    mutationFn: (schedules: Schedule[]) => Promise.all(schedules.map(deleteSchedule)),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["scheduler-schedules"] }),
  });
  const removeGroup = useMutation({
    mutationFn: (groups: ScheduleGroup[]) => Promise.all(groups.filter((group) => group.name !== "default").map((group) => deleteScheduleGroup(group.name))),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["scheduler-groups"] }),
  });
  return (
    <>
      <AwsResourceTable<Schedule>
        title="Schedules"
        description="EventBridge Scheduler schedules in this account and Region."
        columns={scheduleColumns}
        queryKey={["scheduler-schedules"]}
        queryFn={fetchSchedules}
        filterPlaceholder="Find schedules"
        emptyTitle="No schedules"
        emptyDescription="No EventBridge Scheduler schedules exist in this account and Region."
        rowKey={(row) => row.arn || `${row.groupName}/${row.name}`}
        tableTestId="scheduler-schedules-table"
        errorTestId="scheduler-schedules-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length === 0 || state.isPending} onClick={() => state.mutate({ schedules: selected, enabled: true })}>Enable</AwsButton>
            <AwsButton disabled={selected.length === 0 || state.isPending} onClick={() => state.mutate({ schedules: selected, enabled: false })}>Disable</AwsButton>
            <AwsButton disabled={selected.length === 0 || remove.isPending} onClick={() => remove.mutate(selected, { onSuccess: clearSelection })}>Delete</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create schedule</AwsButton>
          </>
        )}
      />
      <AwsResourceTable<ScheduleGroup>
        title="Schedule groups"
        headingVariant="h2"
        description="The groups schedules are organized into."
        columns={groupColumns}
        queryKey={["scheduler-groups"]}
        queryFn={fetchScheduleGroups}
        filterPlaceholder="Find schedule groups"
        emptyTitle="No schedule groups"
        emptyDescription="No EventBridge Scheduler schedule groups exist in this account and Region."
        rowKey={(row) => row.name}
        tableTestId="scheduler-groups-table"
        errorTestId="scheduler-groups-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length === 0 || selected.some((group) => group.name === "default") || removeGroup.isPending} onClick={() => removeGroup.mutate(selected, { onSuccess: clearSelection })}>Delete</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreatingGroup(true)}>Create schedule group</AwsButton>
          </>
        )}
      />
      {creating && <CreateScheduleModal onClose={() => setCreating(false)} />}
      {creatingGroup && <CreateGroupModal onClose={() => setCreatingGroup(false)} />}
    </>
  );
}
