import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import Table, { type TableProps } from "@cloudscape-design/components/table";
import Header from "@cloudscape-design/components/header";
import TextFilter from "@cloudscape-design/components/text-filter";
import Pagination from "@cloudscape-design/components/pagination";
import { AwsButton, AwsEmptyState, AwsErrorAlert } from "./AwsConsole.js";

export interface AwsColumn<T> {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Plain text used for filtering and sorting, so both act on what is shown. */
  value: (row: T) => string;
}

/** What a page's own header actions get to act on: the currently selected rows,
 * plus the levers a mutation needs afterwards — clearing a selection that no
 * longer exists and refetching the rows it changed. */
export interface AwsTableActions<T> {
  selected: T[];
  clearSelection: () => void;
  refetch: () => void;
  isFetching: boolean;
}

export interface AwsResourceTableProps<T> {
  title: string;
  description?: string;
  columns: AwsColumn<T>[];
  queryKey: unknown[];
  queryFn: () => Promise<T[]>;
  filterPlaceholder: string;
  /** Cloudscape empty states name the resource and offer the way to make one. */
  emptyTitle: string;
  emptyDescription: string;
  rowKey: (row: T) => string;
  /** Optional polling cadence for resources whose public cloud API exposes an
   * asynchronous lifecycle (for example AWS CodeBuild builds). */
  refreshInterval?: number;
  /** Page-specific header actions. Without it the header carries the standard
   * selection-aware controls. */
  actions?: (context: AwsTableActions<T>) => ReactNode;
  /** The table's own heading level: `h1` for a page whose primary heading
   * this table's header is, `h2` for a sub-resource table beneath a detail
   * page's own `AwsPageHeader`. */
  headingVariant?: "h1" | "h2";
  /** Automation hooks: a stable id wrapping the table, and one on the
   * load-failure alert, for suites that drive the page. Individual rows are
   * found through their selection checkbox's accessible name
   * (`Select <id>`), the same way the real Cloudscape table testing pattern
   * does. */
  tableTestId?: string;
  errorTestId?: string;
}

const PAGE_SIZE = 10;

export function AwsResourceTable<T>({
  title,
  description,
  columns,
  queryKey,
  queryFn,
  filterPlaceholder,
  emptyTitle,
  emptyDescription,
  rowKey,
  refreshInterval,
  actions,
  headingVariant = "h1",
  tableTestId,
  errorTestId,
}: AwsResourceTableProps<T>) {
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey,
    queryFn,
    refetchInterval: refreshInterval,
  });
  const [filter, setFilter] = useState("");
  const [sortingColumnId, setSortingColumnId] = useState<string | null>(null);
  const [sortingDescending, setSortingDescending] = useState(false);
  const [page, setPage] = useState(1);
  const [selectedItems, setSelectedItems] = useState<T[]>([]);

  const columnDefinitions: TableProps.ColumnDefinition<T>[] = columns.map((column) => ({
    id: column.id,
    header: column.header,
    cell: column.cell,
    sortingComparator: (left: T, right: T) =>
      column.value(left).localeCompare(column.value(right), undefined, { numeric: true }),
  }));

  const rows = useMemo(() => {
    const all = data ?? [];
    const needle = filter.trim().toLowerCase();
    const matched = needle
      ? all.filter((row) => columns.some((column) => column.value(row).toLowerCase().includes(needle)))
      : all;
    const sortColumn = columns.find((candidate) => candidate.id === sortingColumnId);
    if (!sortColumn) return matched;
    return [...matched].sort((left, right) => {
      const comparison = sortColumn.value(left).localeCompare(sortColumn.value(right), undefined, { numeric: true });
      return sortingDescending ? -comparison : comparison;
    });
  }, [data, filter, sortingColumnId, sortingDescending, columns]);

  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const visible = rows.slice((currentPage - 1) * PAGE_SIZE, (currentPage - 1) * PAGE_SIZE + PAGE_SIZE);

  const selected = (data ?? []).filter((row) => selectedItems.some((item) => rowKey(item) === rowKey(row)));
  const clearSelection = () => setSelectedItems([]);

  const headerActions = actions
    ? actions({ selected, clearSelection, refetch: () => void refetch(), isFetching })
    : (
      <>
        <AwsButton disabled={selected.length !== 1}>View details</AwsButton>
        <AwsButton disabled={selected.length === 0}>Delete</AwsButton>
        <AwsButton onClick={() => void refetch()} disabled={isFetching}>
          {isFetching ? "Refreshing…" : "Refresh"}
        </AwsButton>
      </>
    );

  const sortingColumn = columnDefinitions.find((column) => column.id === sortingColumnId);

  const table = (
    <Table<T>
      variant="container"
      items={visible}
      columnDefinitions={columnDefinitions}
      trackBy={(row) => rowKey(row)}
      selectionType="multi"
      selectedItems={selected}
      onSelectionChange={(event) => setSelectedItems([...event.detail.selectedItems])}
      sortingColumn={sortingColumn}
      sortingDescending={sortingDescending}
      onSortingChange={(event) => {
        setSortingColumnId((event.detail.sortingColumn as TableProps.ColumnDefinition<T>).id ?? null);
        setSortingDescending(event.detail.isDescending ?? false);
      }}
      loading={isLoading}
      loadingText={`Loading ${title.toLowerCase()}…`}
      ariaLabels={{
        selectionGroupLabel: "Select",
        allItemsSelectionLabel: () => "Select all resources on this page",
        itemSelectionLabel: (_state, row) => `Select ${rowKey(row)}`,
      }}
      header={
        <Header
          variant={headingVariant}
          description={description}
          counter={data !== undefined ? `(${data.length})` : undefined}
          actions={headerActions}
        >
          {title}
        </Header>
      }
      filter={
        <TextFilter
          filteringText={filter}
          filteringPlaceholder={filterPlaceholder}
          filteringAriaLabel={filterPlaceholder}
          filteringClearAriaLabel="Clear"
          countText={filter.trim() ? `${rows.length} match${rows.length === 1 ? "" : "es"}` : undefined}
          onChange={(event) => {
            setFilter(event.detail.filteringText);
            setPage(1);
          }}
        />
      }
      pagination={
        <Pagination
          currentPageIndex={currentPage}
          pagesCount={pageCount}
          onChange={(event) => setPage(event.detail.currentPageIndex)}
          ariaLabels={{
            paginationLabel: `${title} pagination`,
            previousPageLabel: "Previous page",
            nextPageLabel: "Next page",
            pageLabel: (pageNumber) => `Page ${pageNumber}`,
          }}
        />
      }
      empty={
        isError ? (
          <AwsErrorAlert testId={errorTestId}>
            <strong>Could not load {title.toLowerCase()}.</strong>{" "}
            {error instanceof Error ? error.message : "The simulator did not respond."}
          </AwsErrorAlert>
        ) : (
          <AwsEmptyState title={emptyTitle} description={emptyDescription} />
        )
      }
    />
  );

  return tableTestId ? <div data-testid={tableTestId}>{table}</div> : table;
}
