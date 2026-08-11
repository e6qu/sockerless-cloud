import { AzureResourceTable, type AzureColumn } from "../portal/index.js";
import { fetchMonitorLogs, type MonitorLogRow } from "../api.js";

// A Kusto result names its own columns; the simulator stores every value as a
// string, so the portal reads the ones a log query is expected to carry.
const timeOf = (row: MonitorLogRow) => row["TimeGenerated"] ?? "";
const sourceOf = (row: MonitorLogRow) => row["ContainerGroupName_s"] ?? row["AppRoleName"] ?? "";
const messageOf = (row: MonitorLogRow) => row["Log_s"] ?? row["Message"] ?? "";

const columns: AzureColumn<MonitorLogRow>[] = [
  { id: "time", header: "Time generated", cell: timeOf, value: timeOf },
  { id: "source", header: "Source", cell: sourceOf, value: sourceOf },
  { id: "message", header: "Message", cell: messageOf, value: messageOf },
];

export function MonitorPage() {
  return (
    <AzureResourceTable<MonitorLogRow>
      columns={columns}
      queryKey={["monitor-logs"]}
      queryFn={fetchMonitorLogs}
      filterPlaceholder="Filter by message or source"
      resourceNoun="log entries"
      emptyTitle="No log entries to display"
      emptyDescription="Entries written to this workspace appear here."
      rowKey={(row) => `${timeOf(row)}|${sourceOf(row)}|${messageOf(row)}`}
      essentials={(rows) => [
        { label: "Workspace", value: "Simulator" },
        { label: "Entries", value: String(rows.length) },
        { label: "Sources", value: String(new Set(rows.map(sourceOf).filter(Boolean)).size) },
        { label: "Query", value: "Logs across the workspace" },
      ]}
    />
  );
}
