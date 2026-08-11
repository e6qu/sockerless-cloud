import { createColumnHelper } from "@tanstack/react-table";
import { useResources } from "../hooks/index.js";
import { DataTable } from "../components/DataTable.js";
import { PageHeading } from "../components/PageHeading.js";
import { StatusBadge } from "../components/StatusBadge.js";
import { Spinner } from "../components/Spinner.js";
import { RefreshButton } from "../components/RefreshButton.js";
import { InlineError } from "../components/InlineError.js";
import { Button } from "../components/Button.js";
import type { ResourceEntry } from "../api/index.js";

const col = createColumnHelper<ResourceEntry>();

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const columns: any[] = [
  col.accessor("containerId", {
    header: "Container",
    cell: (info) => (
      <span className="font-mono" style={{ color: "var(--color-fg-muted)" }}>
        {(info.getValue() as string).slice(0, 12)}
      </span>
    ),
  }),
  col.accessor("backend", {
    header: "Backend",
    cell: (info) => (
      <span style={{ color: "var(--color-accent)" }}>{info.getValue()}</span>
    ),
  }),
  col.accessor("resourceType", {
    header: "Type",
    cell: (info) => (
      <span
        className="font-mono uppercase tracking-[0.1em]"
        style={{ color: "var(--color-fg-subtle)", fontSize: "0.65rem" }}
      >
        {info.getValue()}
      </span>
    ),
  }),
  col.accessor("resourceId", {
    header: "Resource ID",
    cell: (info) => (
      <span className="font-mono" style={{ color: "var(--color-fg)" }}>
        {info.getValue()}
      </span>
    ),
  }),
  col.accessor("status", {
    header: "Status",
    cell: (info) => {
      const v = info.getValue();
      return v ? <StatusBadge status={v} /> : <span style={{ color: "var(--color-fg-subtle)" }}>—</span>;
    },
  }),
  col.accessor("createdAt", {
    header: "Created",
    cell: (info) => {
      const v = info.getValue();
      return v ? new Date(v).toLocaleString() : <span style={{ color: "var(--color-fg-subtle)" }}>—</span>;
    },
  }),
];

export function ResourcesPage() {
  const { data, isLoading, refetch, isFetching, isError, error } = useResources();

  if (isLoading) return <Spinner label="loading resources" />;

  if (isError) {
    return (
      <div>
        <PageHeading
          kicker="backend · resources"
          title={<>Cloud resources</>}
          actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
        />
        <InlineError
          title="Failed to load resources"
          detail={error}
          action={<Button variant="ghost" onClick={() => refetch()}>Retry</Button>}
        />
      </div>
    );
  }

  const rows = data ?? [];

  return (
    <div>
      <PageHeading
        kicker="backend · resources"
        title={<>Cloud resources</>}
        meta={`${rows.length} entr${rows.length === 1 ? "y" : "ies"}`}
        actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
      />
      <DataTable
        data={rows}
        columns={columns}
        filterPlaceholder="Filter resources…"
        emptyMessage="No cloud resources tracked."
      />
    </div>
  );
}
