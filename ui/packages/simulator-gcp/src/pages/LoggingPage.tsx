import { useState } from "react";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { fetchLogEntries, type LogEntry, type LogSeverity } from "../api.js";
import { useProject } from "../console/project.js";

// Cloud Logging omits severity when it is the default; the console reads that
// as DEFAULT rather than a blank cell.
const severityOf = (row: LogEntry) => row.severity ?? "DEFAULT";

// The severity ladder Cloud Logging's query language orders `severity>=`
// comparisons on (logging/v2 LogSeverity), offered as the Logs Explorer's own
// "Minimum severity" picker composes it.
const SEVERITY_LEVELS: LogSeverity[] = [
  "DEFAULT",
  "DEBUG",
  "INFO",
  "NOTICE",
  "WARNING",
  "ERROR",
  "CRITICAL",
  "ALERT",
  "EMERGENCY",
];

const summaryOf = (row: LogEntry): string =>
  row.textPayload ?? (row.jsonPayload ? JSON.stringify(row.jsonPayload) : "");

// LogEntryDetailDialog is the entry-expansion the real Logs Explorer's
// clicked-row detail panel offers: the full entry — resource, insert ID, and
// either its text or JSON payload — rather than the one-line summary the
// table shows.
export function LogEntryDetailDialog({ entry, onClose }: { entry: LogEntry; onClose: () => void }) {
  const resourceLabels = Object.entries(entry.resource?.labels ?? {});
  return (
    <GcpDialog title="Log entry" testId="log-entry-dialog" onClose={onClose}>
      <dl className="gc-detail-grid">
        <div className="gc-detail-pair">
          <dt>Timestamp</dt>
          <dd>{formatTimestamp(entry.timestamp)}</dd>
        </div>
        <div className="gc-detail-pair">
          <dt>Severity</dt>
          <dd><GcpStatus status={severityOf(entry)} /></dd>
        </div>
        <div className="gc-detail-pair">
          <dt>Log name</dt>
          <dd>{entry.logName}</dd>
        </div>
        <div className="gc-detail-pair">
          <dt>Insert ID</dt>
          <dd>{entry.insertId ?? "—"}</dd>
        </div>
        <div className="gc-detail-pair">
          <dt>Resource type</dt>
          <dd>{entry.resource?.type ?? "—"}</dd>
        </div>
      </dl>
      {resourceLabels.length > 0 ? (
        <>
          <h2 className="gc-detail-heading">Resource labels</h2>
          <dl className="gc-detail-grid">
            {resourceLabels.map(([key, value]) => (
              <div className="gc-detail-pair" key={key}>
                <dt>{key}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </>
      ) : null}
      <h2 className="gc-detail-heading">Payload</h2>
      <pre className="gc-code" data-testid="log-entry-payload">
        {entry.jsonPayload ? JSON.stringify(entry.jsonPayload, null, 2) : entry.textPayload || "—"}
      </pre>
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-primary" onClick={onClose}>Close</button>
      </div>
    </GcpDialog>
  );
}

export function LoggingPage() {
  const { project } = useProject();
  const [severity, setSeverity] = useState<LogSeverity | "">("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<LogEntry | null>(null);

  // Composed exactly the way the real Logs Explorer's query box and severity
  // picker combine: AND-joined clauses in the Cloud Logging query language,
  // sent to the real `entries:list` filter field rather than filtered
  // client-side.
  const filter = [severity ? `severity>="${severity}"` : "", query].filter(Boolean).join(" AND ");

  const columns: GcpColumn<LogEntry>[] = [
    { id: "timestamp", header: "Timestamp", cell: (row) => formatTimestamp(row.timestamp), value: (row) => row.timestamp },
    { id: "severity", header: "Severity", cell: (row) => <GcpStatus status={severityOf(row)} />, value: severityOf },
    { id: "logName", header: "Log name", cell: (row) => shortName(row.logName), value: (row) => row.logName },
    { id: "message", header: "Summary", cell: (row) => summaryOf(row), value: summaryOf },
    {
      id: "details",
      header: "Details",
      cell: (row) => (
        <button
          type="button"
          className="gc-button-text"
          data-testid={`log-entry-details-${row.insertId ?? `${row.timestamp}-${row.logName}`}`}
          onClick={() => setSelected(row)}
        >
          View
        </button>
      ),
      value: () => "",
    },
  ];

  return (
    <>
      <form
        className="gc-log-query"
        data-testid="log-query-form"
        onSubmit={(event) => {
          event.preventDefault();
          setQuery(queryInput.trim());
        }}
      >
        <label className="gc-log-query-field" style={{ flex: 1 }}>
          Query
          <input
            type="text"
            data-testid="log-query-input"
            placeholder='resource.type="cloud_run_job" AND textPayload:"error"'
            value={queryInput}
            onChange={(event) => setQueryInput(event.target.value)}
          />
        </label>
        <label className="gc-log-query-field">
          Minimum severity
          <select
            data-testid="log-severity-select"
            value={severity}
            onChange={(event) => setSeverity(event.target.value as LogSeverity | "")}
          >
            <option value="">Any</option>
            {SEVERITY_LEVELS.map((level) => (
              <option key={level} value={level}>{level}</option>
            ))}
          </select>
        </label>
        <button type="submit" className="gc-button-primary" data-testid="log-query-run">Run query</button>
      </form>

      <GcpResourceTable<LogEntry>
        title="Logs Explorer"
        description="Search, filter, and inspect log entries across the project."
        columns={columns}
        queryKey={["log-entries-real", project, filter]}
        queryFn={() => fetchLogEntries(project, filter || undefined)}
        filterPlaceholder="Filter loaded entries"
        resourceNoun="log entries"
        empty={{
          headline: "No log entries match this query",
          description: "Entries written across the project appear here as it runs.",
          primaryLabel: "Refresh",
        }}
        rowKey={(row) => `${row.insertId ?? ""}|${row.timestamp}|${row.logName}|${row.textPayload ?? ""}`}
      />

      {selected ? <LogEntryDetailDialog entry={selected} onClose={() => setSelected(null)} /> : null}
    </>
  );
}
