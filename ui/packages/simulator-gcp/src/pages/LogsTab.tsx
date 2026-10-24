import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpStatus } from "../console/GcpConsole.js";
import { ResourceTableView, type GcpColumn } from "../console/GcpTable.js";
import { formatTimestamp } from "../console/format.js";
import { useProject, useProjectPath } from "../console/project.js";
import { fetchLogEntries, type LogEntry } from "../api.js";

export function logSummary(entry: LogEntry): string {
  if (entry.textPayload) return entry.textPayload;
  if (entry.protoPayload?.methodName) {
    return [entry.protoPayload.methodName, entry.protoPayload.resourceName].filter(Boolean).join(" ");
  }
  if (entry.jsonPayload) {
    const message = entry.jsonPayload.message;
    return typeof message === "string" ? message : JSON.stringify(entry.jsonPayload);
  }
  return "";
}

const columns: GcpColumn<LogEntry>[] = [
  {
    id: "severity",
    header: "Severity",
    cell: (row) => <GcpStatus status={row.severity ?? "DEFAULT"} kind={severityKind(row.severity)} />,
    value: (row) => row.severity ?? "DEFAULT",
  },
  { id: "timestamp", header: "Timestamp", cell: (row) => formatTimestamp(row.timestamp), value: (row) => row.timestamp },
  { id: "summary", header: "Summary", cell: (row) => <span className="gc-log-summary">{logSummary(row)}</span>, value: logSummary },
];

export function severityKind(severity: LogEntry["severity"]) {
  switch (severity) {
    case "ERROR":
    case "CRITICAL":
    case "ALERT":
    case "EMERGENCY":
      return "error" as const;
    case "WARNING":
      return "warning" as const;
    case "INFO":
    case "NOTICE":
      return "info" as const;
    default:
      return "inactive" as const;
  }
}

/** A resource's Logs tab: the Cloud Logging entries its filter selects, with a link to the same query in Logs Explorer. */
export function LogsTab({ filter, testId }: { filter: string; testId: string }) {
  const { project } = useProject();
  const path = useProjectPath();
  const query = useQuery({ queryKey: ["log-entries-real", project, filter], queryFn: () => fetchLogEntries(project, filter) });
  return (
    <>
      <p className="gc-tab-intro">
        <code className="gc-code-inline">{filter}</code>{" "}
        <Link to={path(`/ui/logs/query?query=${encodeURIComponent(filter)}`)}>View in Logs Explorer</Link>
      </p>
      <ResourceTableView<LogEntry>
        rows={query.data}
        isLoading={query.isLoading}
        error={query.error}
        columns={columns}
        rowKey={(row) => row.insertId ?? `${row.timestamp}-${logSummary(row)}`}
        resourceNoun="log entries"
        tableId={testId}
        empty={{ headline: "No log entries", description: "Entries this resource writes to Cloud Logging appear here." }}
      />
    </>
  );
}
