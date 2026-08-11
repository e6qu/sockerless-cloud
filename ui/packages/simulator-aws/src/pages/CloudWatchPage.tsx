import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import Textarea from "@cloudscape-design/components/textarea";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatBytes, formatTimestamp } from "../console/format.js";
import {
  deleteCWAlarms,
  deleteCWDashboards,
  fetchCWAlarms,
  fetchCWDashboards,
  putCWDashboard,
  putCWMetricAlarm,
  putCWMetricData,
  setCWAlarmActions,
  type CWAlarm,
  type CWDashboard,
} from "../api.js";

// Amazon CloudWatch — Alarms and Dashboards. DescribeAlarms, DeleteAlarms, and
// ListDashboards on the real CloudWatch Query API (Version 2010-08-01), the API
// that sits beside the separate CloudWatch Logs API this console also reads.

const alarmColumns: AwsColumn<CWAlarm>[] = [
  { id: "name", header: "Name", cell: (row) => row.alarmName, value: (row) => row.alarmName },
  {
    id: "state",
    header: "State",
    cell: (row) => <AwsStatus status={row.stateValue} kind={row.stateValue === "ALARM" ? "error" : row.stateValue === "OK" ? "success" : "inactive"} />,
    value: (row) => row.stateValue,
  },
  {
    id: "condition",
    header: "Conditions",
    cell: (row) => `${row.statistic || ""} ${row.metricName} ${row.comparisonOperator} ${row.threshold}`.trim(),
    value: (row) => `${row.metricName} ${row.comparisonOperator} ${row.threshold}`,
  },
  { id: "namespace", header: "Namespace", cell: (row) => row.namespace, value: (row) => row.namespace },
  {
    id: "stateUpdatedTimestamp",
    header: "Last state update",
    cell: (row) => formatTimestamp(row.stateUpdatedTimestamp),
    value: (row) => row.stateUpdatedTimestamp,
  },
];

const dashboardColumns: AwsColumn<CWDashboard>[] = [
  { id: "name", header: "Name", cell: (row) => row.dashboardName, value: (row) => row.dashboardName },
  { id: "size", header: "Size", cell: (row) => formatBytes(row.size), value: (row) => String(row.size) },
  {
    id: "lastModified",
    header: "Last modified",
    cell: (row) => formatTimestamp(row.lastModified),
    value: (row) => row.lastModified,
  },
];

function DeleteAlarmsModal({
  alarms,
  onClose,
  clearSelection,
}: {
  alarms: CWAlarm[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    // DeleteAlarms takes the whole list in one call, which is what the real
    // console's delete does for a multi-row selection.
    mutationFn: () => deleteCWAlarms(alarms.map((alarm) => alarm.alarmName)),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["cw-alarms"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={alarms.length === 1 ? `Delete ${alarms[0].alarmName}?` : `Delete ${alarms.length} alarms?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="cloudwatch-delete-alarm-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting an alarm stops it evaluating its metric and removes its history.</p>
      <ul>
        {alarms.map((alarm) => (
          <li key={alarm.alarmName}>
            <code>{alarm.alarmName}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function CreateAlarmModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [alarmName, setAlarmName] = useState("");
  const [namespace, setNamespace] = useState("Custom/Application");
  const [metricName, setMetricName] = useState("Requests");
  const [threshold, setThreshold] = useState("1");
  const [alarmAction, setAlarmAction] = useState("");
  const create = useMutation({
    mutationFn: () => putCWMetricAlarm({ alarmName, namespace, metricName, threshold: Number(threshold), alarmAction }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["cw-alarms"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create metric alarm" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!alarmName || !namespace || !metricName || !Number.isFinite(Number(threshold)) || create.isPending} onClick={() => create.mutate()}>Create alarm</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Alarm name"><Input value={alarmName} onChange={(event) => setAlarmName(event.detail.value)} /></FormField>
        <FormField label="Namespace"><Input value={namespace} onChange={(event) => setNamespace(event.detail.value)} /></FormField>
        <FormField label="Metric name"><Input value={metricName} onChange={(event) => setMetricName(event.detail.value)} /></FormField>
        <FormField label="Threshold"><Input value={threshold} type="number" onChange={(event) => setThreshold(event.detail.value)} /></FormField>
        <FormField label="Alarm action" description="Optional Amazon SNS topic ARN."><Input value={alarmAction} onChange={(event) => setAlarmAction(event.detail.value)} /></FormField>
      </SpaceBetween>
    </AwsModal>
  );
}

function PutMetricModal({ onClose }: { onClose: () => void }) {
  const [namespace, setNamespace] = useState("Custom/Application");
  const [metricName, setMetricName] = useState("Requests");
  const [value, setValue] = useState("1");
  const put = useMutation({ mutationFn: () => putCWMetricData(namespace, metricName, Number(value)) });
  return (
    <AwsModal title="Put metric data" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Close</AwsButton><AwsButton variant="primary" disabled={!namespace || !metricName || !Number.isFinite(Number(value)) || put.isPending} onClick={() => put.mutate()}>Put metric</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Namespace"><Input value={namespace} onChange={(event) => setNamespace(event.detail.value)} /></FormField>
        <FormField label="Metric name"><Input value={metricName} onChange={(event) => setMetricName(event.detail.value)} /></FormField>
        <FormField label="Value"><Input value={value} type="number" onChange={(event) => setValue(event.detail.value)} /></FormField>
        {put.isSuccess && <p>Metric datum accepted.</p>}
      </SpaceBetween>
    </AwsModal>
  );
}

function CreateDashboardModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [body, setBody] = useState('{"widgets":[]}');
  const create = useMutation({
    mutationFn: () => putCWDashboard(name, body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["cw-dashboards"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create dashboard" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!name || !body || create.isPending} onClick={() => create.mutate()}>Create dashboard</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Dashboard name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
        <FormField label="Dashboard body"><Textarea value={body} onChange={(event) => setBody(event.detail.value)} rows={8} /></FormField>
      </SpaceBetween>
    </AwsModal>
  );
}

export function CloudWatchPage() {
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<{ alarms: CWAlarm[]; clearSelection: () => void } | null>(null);
  const [creating, setCreating] = useState(false);
  const [puttingMetric, setPuttingMetric] = useState(false);
  const [creatingDashboard, setCreatingDashboard] = useState(false);
  const actions = useMutation({
    mutationFn: ({ alarms, enabled }: { alarms: CWAlarm[]; enabled: boolean }) =>
      setCWAlarmActions(alarms.map((alarm) => alarm.alarmName), enabled),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["cw-alarms"] }),
  });
  const removeDashboards = useMutation({
    mutationFn: (dashboards: CWDashboard[]) => deleteCWDashboards(dashboards.map((dashboard) => dashboard.dashboardName)),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["cw-dashboards"] }),
  });
  return (
    <>
      <AwsResourceTable<CWAlarm>
        title="Alarms"
        description="CloudWatch alarms in this account and Region."
        columns={alarmColumns}
        queryKey={["cw-alarms"]}
        queryFn={fetchCWAlarms}
        filterPlaceholder="Find alarms"
        emptyTitle="No alarms"
        emptyDescription="No CloudWatch alarms exist in this account and Region."
        rowKey={(row) => row.alarmName}
        tableTestId="cloudwatch-alarms-table"
        errorTestId="cloudwatch-alarms-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length === 0 || actions.isPending} onClick={() => actions.mutate({ alarms: selected, enabled: true })}>Enable actions</AwsButton>
            <AwsButton disabled={selected.length === 0 || actions.isPending} onClick={() => actions.mutate({ alarms: selected, enabled: false })}>Disable actions</AwsButton>
            <AwsButton
              data-testid="cloudwatch-delete-alarm"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ alarms: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={() => setPuttingMetric(true)}>Put metric data</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create alarm</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<CWDashboard>
        title="Dashboards"
        headingVariant="h2"
        description="CloudWatch dashboards in this account."
        columns={dashboardColumns}
        queryKey={["cw-dashboards"]}
        queryFn={fetchCWDashboards}
        filterPlaceholder="Find dashboards"
        emptyTitle="No dashboards"
        emptyDescription="No CloudWatch dashboards exist in this account."
        rowKey={(row) => row.dashboardName}
        tableTestId="cloudwatch-dashboards-table"
        errorTestId="cloudwatch-dashboards-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length === 0 || removeDashboards.isPending} onClick={() => removeDashboards.mutate(selected, { onSuccess: clearSelection })}>Delete</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreatingDashboard(true)}>Create dashboard</AwsButton>
          </>
        )}
      />
      {deleting && (
        <DeleteAlarmsModal alarms={deleting.alarms} clearSelection={deleting.clearSelection} onClose={() => setDeleting(null)} />
      )}
      {creating && <CreateAlarmModal onClose={() => setCreating(false)} />}
      {puttingMetric && <PutMetricModal onClose={() => setPuttingMetric(false)} />}
      {creatingDashboard && <CreateDashboardModal onClose={() => setCreatingDashboard(false)} />}
    </>
  );
}
