import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import Table, { type TableProps } from "@cloudscape-design/components/table";
import Header from "@cloudscape-design/components/header";
import PropertyFilter, { type PropertyFilterProps } from "@cloudscape-design/components/property-filter";
import Pagination from "@cloudscape-design/components/pagination";
import CollectionPreferences, { type CollectionPreferencesProps } from "@cloudscape-design/components/collection-preferences";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsEmptyState, AwsErrorAlert, AwsInfoLink, AwsRefreshButton, usePageTitle } from "./AwsConsole.js";

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
  /** Page-specific header actions. Without it the header carries a refresh
   * control only. */
  actions?: (context: AwsTableActions<T>) => ReactNode;
  /** The table's own heading level: `h1` for a page whose primary heading
   * this table's header is, `h2` for a sub-resource table beneath a detail
   * page's own `AwsPageHeader`. */
  headingVariant?: "h1" | "h2";
  /** "single" where the page's actions act on one resource at a time. */
  selectionType?: "single" | "multi";
  /** The action the empty state offers, the way the real console's empty
   * tables offer "Create …". */
  emptyAction?: ReactNode;
  /** Automation hooks: a stable id wrapping the table, and one on the
   * load-failure alert, for suites that drive the page. Individual rows are
   * found through their selection checkbox's accessible name
   * (`Select <id>`), the same way the real Cloudscape table testing pattern
   * does. */
  tableTestId?: string;
  errorTestId?: string;
}

const EMPTY_QUERY: PropertyFilterProps.Query = { tokens: [], operation: "and" };

interface StoredPreferences {
  pageSize: number;
  wrapLines: boolean;
  stripedRows: boolean;
  contentDensity: "comfortable" | "compact";
  contentDisplay: { id: string; visible: boolean }[];
}

function preferencesKey(title: string): string {
  return `aws-console:table:${title}`;
}

function loadPreferences(title: string, columnIds: string[]): StoredPreferences {
  const defaults: StoredPreferences = {
    pageSize: 10,
    wrapLines: false,
    stripedRows: false,
    contentDensity: "comfortable",
    contentDisplay: columnIds.map((id) => ({ id, visible: true })),
  };
  try {
    const raw = window.localStorage.getItem(preferencesKey(title));
    if (!raw) return defaults;
    const stored = { ...defaults, ...(JSON.parse(raw) as Partial<StoredPreferences>) };
    const known = stored.contentDisplay.filter((entry) => columnIds.includes(entry.id));
    const added = columnIds.filter((id) => !known.some((entry) => entry.id === id)).map((id) => ({ id, visible: true }));
    return { ...stored, contentDisplay: [...known, ...added] };
  } catch {
    return defaults;
  }
}

function savePreferences(title: string, preferences: StoredPreferences) {
  try {
    window.localStorage.setItem(preferencesKey(title), JSON.stringify(preferences));
  } catch {
    // Storage is unavailable (a private window); the preferences last for this page.
  }
}

function matchesToken<T>(row: T, token: PropertyFilterProps.Token, columns: AwsColumn<T>[]): boolean {
  const needle = String(token.value ?? "").toLowerCase();
  const test = (text: string) => {
    const value = text.toLowerCase();
    switch (token.operator) {
      case "=":
        return value === needle;
      case "!=":
        return value !== needle;
      case "^":
        return value.startsWith(needle);
      case "!^":
        return !value.startsWith(needle);
      case "!:":
        return !value.includes(needle);
      default:
        return value.includes(needle);
    }
  };
  if (token.propertyKey) {
    const column = columns.find((candidate) => candidate.id === token.propertyKey);
    return column ? test(column.value(row)) : true;
  }
  const negated = token.operator.startsWith("!");
  return negated ? columns.every((column) => test(column.value(row))) : columns.some((column) => test(column.value(row)));
}

/** Applies a PropertyFilter query the way the real console's client-side
 * collections do: every token against its column, joined by the query's
 * operation, with free text matched against every column. */
export function filterRows<T>(rows: T[], query: PropertyFilterProps.Query, columns: AwsColumn<T>[]): T[] {
  if (query.tokens.length === 0) return rows;
  return rows.filter((row) =>
    query.operation === "or"
      ? query.tokens.some((token) => matchesToken(row, token, columns))
      : query.tokens.every((token) => matchesToken(row, token, columns)),
  );
}

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
  selectionType = "multi",
  emptyAction,
  tableTestId,
  errorTestId,
}: AwsResourceTableProps<T>) {
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey,
    queryFn,
    refetchInterval: refreshInterval,
  });
  const pageHeading = headingVariant === "h1";
  usePageTitle(pageHeading ? title : "");
  const columnIds = columns.map((column) => column.id);
  const [query, setQuery] = useState<PropertyFilterProps.Query>(EMPTY_QUERY);
  const [sortingColumnId, setSortingColumnId] = useState<string | null>(null);
  const [sortingDescending, setSortingDescending] = useState(false);
  const [page, setPage] = useState(1);
  const [selectedItems, setSelectedItems] = useState<T[]>([]);
  const [preferences, setPreferences] = useState<StoredPreferences>(() => loadPreferences(title, columnIds));

  const columnDefinitions: TableProps.ColumnDefinition<T>[] = columns.map((column, index) => ({
    id: column.id,
    header: column.header,
    cell: column.cell,
    isRowHeader: index === 0,
    minWidth: 100,
    width: index === 0 ? 280 : Math.max(140, column.header.length * 9 + 64),
    sortingComparator: (left: T, right: T) =>
      column.value(left).localeCompare(column.value(right), undefined, { numeric: true }),
  }));

  const rows = useMemo(() => {
    const matched = filterRows(data ?? [], query, columns);
    const sortColumn = columns.find((candidate) => candidate.id === sortingColumnId);
    if (!sortColumn) return matched;
    return [...matched].sort((left, right) => {
      const comparison = sortColumn.value(left).localeCompare(sortColumn.value(right), undefined, { numeric: true });
      return sortingDescending ? -comparison : comparison;
    });
  }, [data, query, sortingColumnId, sortingDescending, columns]);

  const filteringOptions = useMemo(() => {
    const options: PropertyFilterProps.FilteringOption[] = [];
    for (const column of columns) {
      const seen = new Set<string>();
      for (const row of data ?? []) {
        const value = column.value(row);
        if (value && !seen.has(value) && seen.size < 50) {
          seen.add(value);
          options.push({ propertyKey: column.id, value });
        }
      }
    }
    return options;
  }, [data, columns]);

  const pageSize = preferences.pageSize;
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
  const currentPage = Math.min(page, pageCount);
  const visible = rows.slice((currentPage - 1) * pageSize, currentPage * pageSize);

  const selected = (data ?? []).filter((row) => selectedItems.some((item) => rowKey(item) === rowKey(row)));
  const clearSelection = () => setSelectedItems([]);
  const filtered = query.tokens.length > 0;

  const refresh = <AwsRefreshButton onClick={() => void refetch()} loading={isFetching} />;
  const headerActions = actions ? actions({ selected, clearSelection, refetch: () => void refetch(), isFetching }) : refresh;

  const sortingColumn = columnDefinitions.find((column) => column.id === sortingColumnId);
  const total = data?.length;
  const counter =
    total === undefined ? undefined : selected.length > 0 && selectionType === "multi" ? `(${selected.length}/${total})` : `(${total})`;

  const preferencesChange = (detail: CollectionPreferencesProps.Preferences) => {
    const next: StoredPreferences = {
      pageSize: detail.pageSize ?? preferences.pageSize,
      wrapLines: detail.wrapLines ?? preferences.wrapLines,
      stripedRows: detail.stripedRows ?? preferences.stripedRows,
      contentDensity: detail.contentDensity ?? preferences.contentDensity,
      contentDisplay: (detail.contentDisplay ?? preferences.contentDisplay).map((entry) => ({ id: entry.id, visible: entry.visible })),
    };
    setPreferences(next);
    savePreferences(title, next);
    setPage(1);
  };

  const table = (
    <Table<T>
      variant={pageHeading ? "full-page" : "container"}
      stickyHeader={pageHeading}
      resizableColumns
      items={visible}
      columnDefinitions={columnDefinitions}
      columnDisplay={preferences.contentDisplay}
      wrapLines={preferences.wrapLines}
      stripedRows={preferences.stripedRows}
      contentDensity={preferences.contentDensity}
      trackBy={(row) => rowKey(row)}
      selectionType={selectionType}
      selectedItems={selected}
      onSelectionChange={(event) => setSelectedItems([...event.detail.selectedItems])}
      sortingColumn={sortingColumn}
      sortingDescending={sortingDescending}
      onSortingChange={(event) => {
        setSortingColumnId((event.detail.sortingColumn as TableProps.ColumnDefinition<T>).id ?? null);
        setSortingDescending(event.detail.isDescending ?? false);
      }}
      loading={isLoading}
      loadingText={`Loading ${title.toLowerCase()}`}
      ariaLabels={{
        selectionGroupLabel: "Items selection",
        allItemsSelectionLabel: () => "Select all resources on this page",
        itemSelectionLabel: (_state, row) => `Select ${rowKey(row)}`,
        tableLabel: title,
      }}
      renderAriaLive={({ firstIndex, lastIndex, totalItemsCount }) =>
        `Displaying items ${firstIndex} to ${lastIndex} of ${totalItemsCount}`
      }
      header={
        <Header
          variant={pageHeading ? "awsui-h1-sticky" : "h2"}
          headingTagOverride={pageHeading ? "h1" : "h2"}
          description={description}
          counter={counter}
          info={pageHeading ? <AwsInfoLink /> : undefined}
          actions={<SpaceBetween direction="horizontal" size="xs">{headerActions}</SpaceBetween>}
        >
          {title}
        </Header>
      }
      filter={
        <PropertyFilter
          query={query}
          onChange={(event) => {
            setQuery(event.detail);
            setPage(1);
          }}
          filteringProperties={columns.map((column) => ({
            key: column.id,
            propertyLabel: column.header,
            groupValuesLabel: `${column.header} values`,
            operators: ["=", "!=", ":", "!:", "^", "!^"],
          }))}
          filteringOptions={filteringOptions}
          filteringPlaceholder={filterPlaceholder}
          filteringAriaLabel={filterPlaceholder}
          countText={filtered ? `${rows.length} match${rows.length === 1 ? "" : "es"}` : undefined}
          expandToViewport
          i18nStrings={{
            filteringAriaLabel: filterPlaceholder,
            filteringPlaceholder: filterPlaceholder,
            dismissAriaLabel: "Dismiss",
            clearAriaLabel: "Clear",
            groupValuesText: "Values",
            groupPropertiesText: "Properties",
            operatorsText: "Operators",
            operationAndText: "and",
            operationOrText: "or",
            operatorContainsText: "Contains",
            operatorDoesNotContainText: "Does not contain",
            operatorEqualsText: "Equals",
            operatorDoesNotEqualText: "Does not equal",
            operatorStartsWithText: "Starts with",
            operatorDoesNotStartWithText: "Does not start with",
            editTokenHeader: "Edit filter",
            propertyText: "Property",
            operatorText: "Operator",
            valueText: "Value",
            cancelActionText: "Cancel",
            applyActionText: "Apply",
            allPropertiesLabel: "All properties",
            tokenLimitShowMore: "Show more",
            tokenLimitShowFewer: "Show fewer",
            clearFiltersText: "Clear filters",
            removeTokenButtonAriaLabel: (token) => `Remove token ${token.propertyLabel} ${token.operator} ${token.value}`,
            enteredTextLabel: (text) => `Use: "${text}"`,
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
      preferences={
        <CollectionPreferences
          title="Preferences"
          confirmLabel="Confirm"
          cancelLabel="Cancel"
          preferences={preferences}
          onConfirm={(event) => preferencesChange(event.detail)}
          pageSizePreference={{
            title: "Page size",
            options: [10, 25, 50, 100].map((value) => ({ value, label: `${value} resources` })),
          }}
          wrapLinesPreference={{ label: "Wrap lines", description: "Select to see all the text and wrap the lines" }}
          stripedRowsPreference={{ label: "Striped rows", description: "Select to add alternating shaded rows" }}
          contentDensityPreference={{ label: "Compact mode", description: "Select to display content in a denser, more compact mode" }}
          contentDisplayPreference={{
            title: "Column preferences",
            description: "Customize the columns visibility and order.",
            options: columns.map((column) => ({ id: column.id, label: column.header, alwaysVisible: column.id === columns[0].id })),
          }}
        />
      }
      empty={
        isError ? (
          <AwsErrorAlert testId={errorTestId}>
            <strong>Could not load {title.toLowerCase()}.</strong>{" "}
            {error instanceof Error ? error.message : "The service did not respond."}
          </AwsErrorAlert>
        ) : filtered ? (
          <Box textAlign="center" color="inherit" padding={{ vertical: "l" }}>
            <Box variant="strong" color="inherit">
              No matches
            </Box>
            <Box variant="p" color="inherit" padding={{ bottom: "s" }}>
              No {title.toLowerCase()} match the filter.
            </Box>
            <Button onClick={() => setQuery(EMPTY_QUERY)}>Clear filter</Button>
          </Box>
        ) : (
          <AwsEmptyState title={emptyTitle} description={emptyDescription} action={emptyAction} />
        )
      }
    />
  );

  return tableTestId ? <div data-testid={tableTestId}>{table}</div> : table;
}
