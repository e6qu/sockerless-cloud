import { useState } from "react";
import { useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { LoadError, Loading } from "../console/GcpResourceDetail.js";
import { Icon } from "../console/icons.js";
import { formatTimestamp } from "../console/format.js";
import { useProject } from "../console/project.js";
import { fetchLogEntries, type LogEntry, type LogSeverity } from "../api.js";
import { logSummary, severityKind } from "./LogsTab.js";

// The severity ladder Cloud Logging orders severity>= comparisons on.
const SEVERITY_LEVELS: LogSeverity[] = ["DEFAULT", "DEBUG", "INFO", "NOTICE", "WARNING", "ERROR", "CRITICAL", "ALERT", "EMERGENCY"];

const logId = (logName: string) => decodeURIComponent(logName.slice(logName.indexOf("/logs/") + 6));

function LogRow({ entry }: { entry: LogEntry }) {
  const [open, setOpen] = useState(false);
  const severity = entry.severity ?? "DEFAULT";
  return (
    <li className="gc-log-row">
      <button
        type="button"
        className="gc-log-row-toggle"
        aria-expanded={open}
        data-testid={`log-entry-${entry.insertId ?? entry.timestamp}`}
        onClick={() => setOpen((value) => !value)}
      >
        <Icon name={open ? "expand_more" : "chevron_right"} size="1.25em" />
        <GcpStatus status={severity} kind={severityKind(entry.severity)} label={severity} />
        <span className="gc-log-time">{formatTimestamp(entry.timestamp)}</span>
        <span className="gc-log-name">{logId(entry.logName)}</span>
        <span className="gc-log-summary">{logSummary(entry)}</span>
      </button>
      {open ? (
        <pre className="gc-code gc-log-json" data-testid="log-entry-payload">
          {JSON.stringify(entry, null, 2)}
        </pre>
      ) : null}
    </li>
  );
}

// Logs Explorer: a Cloud Logging query and a minimum severity, sent as the
// entries:list filter, and the matching entries newest first, each one
// expanding to the full LogEntry.
export function LoggingPage() {
  const { project } = useProject();
  const [params, setParams] = useSearchParams();
  const initial = params.get("query") ?? "";
  const [queryInput, setQueryInput] = useState(initial);
  const [query, setQuery] = useState(initial);
  const [severity, setSeverity] = useState<LogSeverity | "">("");
  const filter = [severity ? `severity>=${severity}` : "", query].filter(Boolean).join(" AND ");
  const entries = useQuery({
    queryKey: ["log-entries-real", project, filter],
    queryFn: () => fetchLogEntries(project, filter || undefined),
  });

  return (
    <>
      <GcpPageHeader title="Logs Explorer" onRefresh={() => void entries.refetch()} refreshing={entries.isFetching} />
      <form
        className="gc-log-query"
        data-testid="log-query-form"
        onSubmit={(event) => {
          event.preventDefault();
          const next = queryInput.trim();
          setQuery(next);
          const updated = new URLSearchParams(params);
          if (next) updated.set("query", next);
          else updated.delete("query");
          setParams(updated, { replace: true });
        }}
      >
        <label className="gc-log-query-field gc-log-query-editor">
          Query
          <textarea
            rows={3}
            data-testid="log-query-input"
            placeholder='resource.type="gce_instance" AND textPayload:"error"'
            value={queryInput}
            onChange={(event) => setQueryInput(event.target.value)}
          />
        </label>
        <label className="gc-log-query-field">
          Severity
          <select data-testid="log-severity-select" value={severity} onChange={(event) => setSeverity(event.target.value as LogSeverity | "")}>
            <option value="">All severities</option>
            {SEVERITY_LEVELS.map((level) => (
              <option key={level} value={level}>
                {level.charAt(0) + level.slice(1).toLowerCase()} and above
              </option>
            ))}
          </select>
        </label>
        <button type="submit" className="gc-button-primary" data-testid="log-query-run">
          <Icon name="play_arrow" size="1.25em" />
          Run query
        </button>
      </form>
      <section className="gc-table-card" aria-labelledby="log-results-heading">
        <div className="gc-log-results-header">
          <h2 id="log-results-heading">Query results</h2>
          <span>{entries.data ? `${entries.data.length} log entries` : ""}</span>
        </div>
        {entries.isError ? (
          <LoadError error={entries.error} noun="log entries" />
        ) : entries.isLoading ? (
          <Loading noun="log entries" />
        ) : (entries.data ?? []).length === 0 ? (
          <div className="gc-empty">
            <p className="gc-empty-headline">No data found</p>
            <p className="gc-empty-description">No log entries match the query. Edit the query or widen the severity.</p>
          </div>
        ) : (
          <ul className="gc-log-list" data-testid="log-results">
            {(entries.data ?? []).map((entry, index) => (
              <LogRow key={`${entry.insertId ?? ""}-${entry.timestamp}-${index}`} entry={entry} />
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
