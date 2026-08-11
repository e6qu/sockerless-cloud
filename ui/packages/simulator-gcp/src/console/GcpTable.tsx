import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, type GcpPageAction } from "./GcpConsole.js";
import { Icon } from "./icons.js";

export interface GcpColumn<T> {
  id: string;
  header: string;
  /** Some console headers carry an inline help affordance. */
  help?: string;
  cell: (row: T) => ReactNode;
  /** Plain text used for filtering and sorting, so both act on what is shown. */
  value: (row: T) => string;
}

export interface GcpEmptyState {
  headline: string;
  description: string;
  /** The console names the side effect of the primary action. */
  sideEffect?: string;
  primaryLabel: string;
  /** Wired on pages whose create flow exists; absent, the button is inert. */
  onPrimary?: () => void;
}

export interface GcpResourceTableProps<T> {
  title: string;
  description: string;
  actions?: GcpPageAction[];
  columns: GcpColumn<T>[];
  queryKey: unknown[];
  queryFn: () => Promise<T[]>;
  filterPlaceholder: string;
  empty: GcpEmptyState;
  rowKey: (row: T) => string;
  resourceNoun: string;
}

const PAGE_SIZE = 10;

export function GcpResourceTable<T>({
  title,
  description,
  actions,
  columns,
  queryKey,
  queryFn,
  filterPlaceholder,
  empty,
  rowKey,
  resourceNoun,
}: GcpResourceTableProps<T>) {
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({ queryKey, queryFn });
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<{ column: string; ascending: boolean } | null>(null);
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const rows = useMemo(() => {
    const all = data ?? [];
    const needle = filter.trim().toLowerCase();
    const matched = needle
      ? all.filter((row) => columns.some((column) => column.value(row).toLowerCase().includes(needle)))
      : all;
    if (!sort) return matched;
    const column = columns.find((candidate) => candidate.id === sort.column);
    if (!column) return matched;
    return [...matched].sort((left, right) => {
      const comparison = column.value(left).localeCompare(column.value(right), undefined, { numeric: true });
      return sort.ascending ? comparison : -comparison;
    });
  }, [data, filter, sort, columns]);

  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount - 1);
  const visible = rows.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE);
  const allVisibleSelected = visible.length > 0 && visible.every((row) => selected.has(rowKey(row)));

  function toggleSort(columnId: string) {
    setSort((current) =>
      current && current.column === columnId
        ? { column: columnId, ascending: !current.ascending }
        : { column: columnId, ascending: true },
    );
  }

  return (
    <>
      <GcpPageHeader
        title={title}
        description={description}
        actions={actions}
        onRefresh={() => void refetch()}
        refreshing={isFetching}
      />
      <div className="gc-table-tools">
        <span className="gc-filter-chip">
          <Icon name="filter_list" size="1.25em" />
          Filter
        </span>
        <input
          className="gc-filter-input"
          type="search"
          value={filter}
          placeholder={filterPlaceholder}
          aria-label={filterPlaceholder}
          onChange={(event) => {
            setFilter(event.target.value);
            setPage(0);
          }}
        />
        <div className="gc-table-tools-right">
          <button type="button" className="gc-icon-button" aria-label="Help" title="Help">
            <Icon name="help" size="1.25em" />
          </button>
          <button type="button" className="gc-icon-button" aria-label="Column display options" title="Column display options">
            <Icon name="view_column" size="1.25em" />
          </button>
        </div>
      </div>

      {/* The console keeps the column headers whatever the body holds, so what
       * the resource is described by stays readable while it is loading,
       * empty, or failed — the empty state names the side effect of creating
       * one rather than reporting a blank. */}
      <div className="gc-table-wrap">
        <table className="gc-table">
          <thead>
            <tr>
              <th className="gc-table-select">
                <input
                  type="checkbox"
                  aria-label="Select all resources on this page"
                  checked={allVisibleSelected}
                  disabled={visible.length === 0}
                  onChange={() =>
                    setSelected((current) => {
                      const next = new Set(current);
                      for (const row of visible) {
                        if (allVisibleSelected) next.delete(rowKey(row));
                        else next.add(rowKey(row));
                      }
                      return next;
                    })
                  }
                />
              </th>
              {columns.map((column) => {
                const active = sort?.column === column.id;
                return (
                  <th key={column.id} aria-sort={active ? (sort.ascending ? "ascending" : "descending") : "none"}>
                    <button type="button" onClick={() => toggleSort(column.id)}>
                      {column.header}
                      {column.help ? (
                        <span className="gc-header-help" title={column.help} aria-label={column.help}>ⓘ</span>
                      ) : null}
                      <span aria-hidden className="gc-sort">{active ? (sort.ascending ? "↑" : "↓") : ""}</span>
                    </button>
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {isError ? (
              <tr>
                <td className="gc-table-state" colSpan={columns.length + 1}>
                  <div className="gc-message gc-message-error" role="alert">
                    <strong>Couldn't load {resourceNoun}.</strong>{" "}
                    {error instanceof Error ? error.message : "The simulator did not respond."}
                  </div>
                </td>
              </tr>
            ) : isLoading ? (
              <tr>
                <td className="gc-table-state" colSpan={columns.length + 1}>
                  <div className="gc-loading" role="status">Loading {resourceNoun}…</div>
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td className="gc-table-state" colSpan={columns.length + 1}>
                  <div className="gc-empty">
                    <svg className="gc-empty-illustration" viewBox="0 0 160 96" role="img" aria-label="No resources illustration">
                      <path
                        d="M44 74 a20 20 0 0 1 2 -40 a26 26 0 0 1 50 -6 a18 18 0 0 1 18 22 a16 16 0 0 1 -4 24 z"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="2"
                        strokeDasharray="6 6"
                        strokeLinejoin="round"
                      />
                    </svg>
                    <p className="gc-empty-headline">{empty.headline}</p>
                    <p className="gc-empty-description">
                      {empty.description}{" "}
                      <a href="#" onClick={(event) => event.preventDefault()}>
                        Learn more <Icon name="deployed_code" size="0.9em" />
                      </a>
                    </p>
                    {empty.sideEffect ? <p className="gc-empty-sideeffect">{empty.sideEffect}</p> : null}
                    <div className="gc-empty-actions">
                      <button
                        type="button"
                        className="gc-button-primary"
                        disabled={!empty.onPrimary}
                        onClick={empty.onPrimary}
                      >
                        {empty.primaryLabel}
                      </button>
                      <a href="#" className="gc-empty-quickstart" onClick={(event) => event.preventDefault()}>Take the quickstart</a>
                    </div>
                  </div>
                </td>
              </tr>
            ) : (
              visible.map((row) => {
                const id = rowKey(row);
                return (
                  <tr key={id} className={selected.has(id) ? "gc-row-selected" : undefined}>
                    <td className="gc-table-select">
                      <input
                        type="checkbox"
                        aria-label={`Select ${id}`}
                        checked={selected.has(id)}
                        onChange={() =>
                          setSelected((current) => {
                            const next = new Set(current);
                            if (next.has(id)) next.delete(id);
                            else next.add(id);
                            return next;
                          })
                        }
                      />
                    </td>
                    {columns.map((column) => (
                      <td key={column.id}>{column.cell(row)}</td>
                    ))}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {rows.length > 0 ? (
        <div className="gc-pagination">
          <span>
            {`${currentPage * PAGE_SIZE + 1}–${currentPage * PAGE_SIZE + visible.length} of ${rows.length}`}
          </span>
          <button type="button" aria-label="Previous page" disabled={currentPage === 0} onClick={() => setPage(currentPage - 1)}>‹</button>
          <span aria-current="page">{currentPage + 1}</span>
          <button type="button" aria-label="Next page" disabled={currentPage >= pageCount - 1} onClick={() => setPage(currentPage + 1)}>›</button>
        </div>
      ) : null}
    </>
  );
}
