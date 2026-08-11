import { useState } from "react";
import { createColumnHelper } from "@tanstack/react-table";
import { useContainers } from "../hooks/index.js";
import { DataTable } from "../components/DataTable.js";
import { PageHeading } from "../components/PageHeading.js";
import { StatusBadge } from "../components/StatusBadge.js";
import { Spinner } from "../components/Spinner.js";
import { RefreshButton } from "../components/RefreshButton.js";
import { InlineError } from "../components/InlineError.js";
import { Button } from "../components/Button.js";
import { ContainerDetailModal } from "../components/ContainerDetailModal.js";
import type { ContainerSummary } from "../api/index.js";

const col = createColumnHelper<ContainerSummary>();

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const columns: any[] = [
  col.accessor("id", {
    header: "ID",
    cell: (info) => (
      <span className="font-mono" style={{ color: "var(--color-fg-muted)" }}>
        {info.getValue().slice(0, 12)}
      </span>
    ),
  }),
  col.accessor("name", {
    header: "Name",
    cell: (info) => (
      <span style={{ color: "var(--color-fg)", fontWeight: 500 }}>
        {info.getValue()}
      </span>
    ),
  }),
  col.accessor("image", {
    header: "Image",
    cell: (info) => (
      <span className="font-mono" style={{ color: "var(--color-fg-muted)" }}>
        {info.getValue()}
      </span>
    ),
  }),
  col.accessor("state", {
    header: "State",
    cell: (info) => <StatusBadge status={info.getValue()} />,
  }),
  col.accessor("created", {
    header: "Created",
    cell: (info) => {
      const v = info.getValue();
      if (!v) return <span style={{ color: "var(--color-fg-subtle)" }}>—</span>;
      return new Date(v).toLocaleString();
    },
  }),
  col.accessor("pod_name", {
    header: "Pod",
    cell: (info) => (
      <span style={{ color: "var(--color-fg-muted)" }}>
        {info.getValue() || "—"}
      </span>
    ),
  }),
];

export function ContainersPage() {
  const { data, isLoading, refetch, isFetching, error, isError } = useContainers();
  const [selected, setSelected] = useState<ContainerSummary | null>(null);

  if (isLoading) return <Spinner label="loading containers" />;

  if (isError) {
    return (
      <div>
        <PageHeading
          kicker="backend · containers"
          title={<>Container roster</>}
          actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
        />
        <InlineError
          title="Failed to load containers"
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
        kicker="backend · containers"
        title={<>Container roster</>}
        meta={`${rows.length} container${rows.length === 1 ? "" : "s"}`}
        actions={<RefreshButton onClick={() => refetch()} loading={isFetching} />}
      />
      <DataTable
        data={rows}
        columns={columns}
        filterPlaceholder="Filter containers…"
        emptyMessage="No containers running."
        onRowClick={setSelected}
      />
      <ContainerDetailModal container={selected} onClose={() => setSelected(null)} />
    </div>
  );
}
