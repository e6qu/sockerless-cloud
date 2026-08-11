import { useMetrics, useStatus } from "../hooks/index.js";
import { DataTable } from "../components/DataTable.js";
import { MetricsCard } from "../components/MetricsCard.js";
import { PageHeading } from "../components/PageHeading.js";
import { Spinner } from "../components/Spinner.js";
import { RefreshButton } from "../components/RefreshButton.js";
import { InlineError } from "../components/InlineError.js";
import { Button } from "../components/Button.js";
import { createColumnHelper } from "@tanstack/react-table";

interface RequestRow {
  endpoint: string;
  count: number;
  p50: number | null;
  p95: number | null;
  p99: number | null;
}

const col = createColumnHelper<RequestRow>();

export function MetricsPage() {
  const { data: metrics, isLoading, refetch, isFetching, isError, error } = useMetrics();
  const { data: status, isError: statusError, isLoading: statusLoading } = useStatus();
  const statusUnknown = statusError || statusLoading;

  if (isLoading) return <Spinner label="loading metrics" />;

  if (isError) {
    return (
      <div>
        <PageHeading
          kicker="backend · metrics"
          title={<>Throughput &amp; latency</>}
          actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
        />
        <InlineError
          title="Failed to load metrics"
          detail={error}
          action={<Button variant="ghost" onClick={() => refetch()}>Retry</Button>}
        />
      </div>
    );
  }

  const rows: RequestRow[] = metrics
    ? Object.entries(metrics.requests)
        .map(([endpoint, count]) => {
          const lat = metrics.latency_ms[endpoint];
          return {
            endpoint,
            count,
            p50: lat?.p50 ?? null,
            p95: lat?.p95 ?? null,
            p99: lat?.p99 ?? null,
          };
        })
        .sort((a, b) => b.count - a.count)
    : [];

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const columns: any[] = [
    col.accessor("endpoint", {
      header: "Endpoint",
      cell: (info) => (
        <span className="font-mono" style={{ color: "var(--color-fg)" }}>
          {info.getValue()}
        </span>
      ),
    }),
    col.accessor("count", {
      header: "Count",
      cell: (info) => (
        <span className="tabular-nums" style={{ color: "var(--color-fg-muted)" }}>
          {info.getValue()}
        </span>
      ),
    }),
    col.accessor("p50", {
      header: "P50 ms",
      cell: (info) => (
        <span className="tabular-nums" style={{ color: "var(--color-fg-muted)" }}>
          {info.getValue() ?? "—"}
        </span>
      ),
    }),
    col.accessor("p95", {
      header: "P95 ms",
      cell: (info) => (
        <span className="tabular-nums" style={{ color: "var(--color-fg-muted)" }}>
          {info.getValue() ?? "—"}
        </span>
      ),
    }),
    col.accessor("p99", {
      header: "P99 ms",
      cell: (info) => {
        const v = info.getValue();
        return (
          <span
            className="tabular-nums"
            style={{
              color: v != null && v > 250 ? "var(--color-status-warn)" : "var(--color-fg-muted)",
            }}
          >
            {v ?? "—"}
          </span>
        );
      },
    }),
  ];

  return (
    <div>
      <PageHeading
        kicker="backend · metrics"
        title={<>Throughput &amp; latency</>}
        meta={
          metrics
            ? `${metrics.goroutines} goroutines · ${metrics.heap_alloc_mb.toFixed(1)} MB heap`
            : undefined
        }
        actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
      />

      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <MetricsCard title="Goroutines" value={metrics?.goroutines ?? 0} />
        <MetricsCard
          title="Heap"
          value={`${(metrics?.heap_alloc_mb ?? 0).toFixed(1)} MB`}
        />
        <MetricsCard
          title="Containers"
          value={statusUnknown ? "—" : (status?.containers ?? 0)}
          emphasized={!statusUnknown && (status?.containers ?? 0) > 0}
        />
        <MetricsCard
          title="Active resources"
          value={statusUnknown ? "—" : (status?.active_resources ?? 0)}
        />
      </div>

      <h3
        className="mb-3 text-[10px] uppercase tracking-[0.22em]"
        style={{ color: "var(--color-fg-subtle)" }}
      >
        Requests by endpoint
      </h3>
      <DataTable
        data={rows}
        columns={columns}
        filterPlaceholder="Filter endpoints…"
        emptyMessage="No requests recorded."
      />
    </div>
  );
}
