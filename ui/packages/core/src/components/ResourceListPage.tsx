import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { type ColumnDef } from "@tanstack/react-table";
import { type ReactNode } from "react";
import { Button } from "./Button.js";
import { DataTable } from "./DataTable.js";
import { InlineError } from "./InlineError.js";
import { PageHeading } from "./PageHeading.js";
import { Spinner } from "./Spinner.js";

// `ColumnDef`'s second type parameter (cell value type) defaults to
// `unknown`. Allowing `any` here matches the existing DataTable
// signature so callers can keep using `accessorKey` columns without
// repeating generics.
//
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyColumns<T> = ColumnDef<T, any>[];

export interface ResourceListPageProps<T> {
  /** Small uppercase label above the title, e.g. "aws · simulator · ecs". */
  kicker?: string;
  /** Page title — rendered in the editorial italic display voice. */
  title: ReactNode;
  /** Optional override for the meta line. Defaults to "{N} {countNoun}". */
  meta?: ReactNode;
  /** Singular noun for the default meta count, pluralised with -s. */
  countNoun?: string;
  /** Right-aligned actions (refresh button etc.). */
  actions?: ReactNode;

  /** Columns passed through to DataTable. */
  columns: AnyColumns<T>;

  /** TanStack Query key + fn. */
  queryKey: readonly unknown[];
  queryFn: () => Promise<T[]>;
  /** Polling interval in ms. Default 5000. Pass 0 to disable. */
  refetchInterval?: number | false;

  /** DataTable filter input placeholder. */
  filterPlaceholder?: string;
  /** DataTable empty-state message. */
  emptyMessage?: string;
  /** Optional row-click handler. */
  onRowClick?: (row: T) => void;
}

/**
 * ResourceListPage — shared "fetch list, render table" page used by
 * every per-service sim page (ECS tasks, Lambda functions, S3 buckets,
 * Cloud Run jobs, …) and any future flat-list admin views.
 *
 * Owns the `useQuery` so each call site collapses to props. Renders
 * Spinner on initial load, InlineError with retry on failure, and
 * DataTable on success. Defaults to 5-second polling — change via
 * `refetchInterval` (or pass `false` / `0` to disable).
 */
export function ResourceListPage<T>({
  kicker,
  title,
  meta,
  countNoun = "row",
  actions,
  columns,
  queryKey,
  queryFn,
  refetchInterval = 5000,
  filterPlaceholder,
  emptyMessage,
  onRowClick,
}: ResourceListPageProps<T>) {
  const query = useQuery<T[]>({
    queryKey,
    queryFn,
    refetchInterval: refetchInterval === false ? false : refetchInterval,
  });

  const rows = query.data ?? [];
  const resolvedMeta = meta ?? defaultMeta(rows.length, countNoun, query);

  return (
    <div className="flex flex-col gap-4">
      <PageHeading
        kicker={kicker}
        title={title}
        meta={resolvedMeta}
        actions={actions}
      />
      <ResourceListBody
        query={query}
        rows={rows}
        columns={columns}
        filterPlaceholder={filterPlaceholder}
        emptyMessage={emptyMessage}
        onRowClick={onRowClick}
      />
    </div>
  );
}

function ResourceListBody<T>({
  query,
  rows,
  columns,
  filterPlaceholder,
  emptyMessage,
  onRowClick,
}: {
  query: UseQueryResult<T[]>;
  rows: T[];
  columns: AnyColumns<T>;
  filterPlaceholder?: string;
  emptyMessage?: string;
  onRowClick?: (row: T) => void;
}) {
  if (query.isLoading) {
    return <Spinner label="loading" />;
  }
  if (query.isError) {
    return (
      <InlineError
        title="Failed to load"
        detail={query.error instanceof Error ? query.error : "request failed"}
        action={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              void query.refetch();
            }}
          >
            retry
          </Button>
        }
      />
    );
  }
  return (
    <DataTable
      data={rows}
      columns={columns}
      filterPlaceholder={filterPlaceholder}
      emptyMessage={emptyMessage}
      onRowClick={onRowClick}
    />
  );
}

function defaultMeta(
  count: number,
  noun: string,
  query: UseQueryResult<unknown[]>,
): ReactNode {
  if (query.isLoading) return "loading…";
  if (query.isError) return "error";
  return `${count} ${count === 1 ? noun : pluralize(noun)}`;
}

function pluralize(noun: string): string {
  if (/[^aeiou]y$/i.test(noun)) return `${noun.slice(0, -1)}ies`;
  if (/(s|x|z|ch|sh)$/i.test(noun)) return `${noun}es`;
  return `${noun}s`;
}
