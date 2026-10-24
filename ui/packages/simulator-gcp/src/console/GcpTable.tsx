import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, type GcpPageAction } from "./GcpConsole.js";
import { GcpDialog } from "./GcpDialog.js";
import { Icon } from "./icons.js";
import { MenuButton, useDismiss, type MenuEntry } from "./menu.js";

export interface GcpColumn<T> {
  id: string;
  header: string;
  help?: string;
  cell: (row: T) => ReactNode;
  /** Plain text the filter and the sort act on, so both act on what is shown. */
  value: (row: T) => string;
  /** Off until the operator turns it on in Column display options. */
  defaultHidden?: boolean;
}

export interface GcpEmptyState {
  headline: string;
  description: string;
  sideEffect?: string;
  primaryLabel?: string;
  onPrimary?: () => void;
  primaryTo?: string;
}

export interface FilterChip {
  column?: string;
  value: string;
}

function readStoredColumns(key: string): string[] | null {
  try {
    const raw = window.localStorage.getItem(key);
    const parsed: unknown = raw ? JSON.parse(raw) : null;
    return Array.isArray(parsed) ? parsed.filter((entry): entry is string => typeof entry === "string") : null;
  } catch {
    return null;
  }
}

function storeColumns(key: string, columns: string[]) {
  try {
    window.localStorage.setItem(key, JSON.stringify(columns));
  } catch {
    // Without storage the choice lasts for this page only.
  }
}

/** Rows whose shown values match every chip; a chip without a column matches any column. */
export function filterRows<T>(rows: T[], columns: GcpColumn<T>[], chips: FilterChip[]): T[] {
  if (chips.length === 0) return rows;
  return rows.filter((row) =>
    chips.every((chip) => {
      const needle = chip.value.toLowerCase();
      const candidates = chip.column ? columns.filter((column) => column.id === chip.column) : columns;
      return candidates.some((column) => column.value(row).toLowerCase().includes(needle));
    }),
  );
}

/** Parses "Property: value" against the columns' headers; anything else filters every column. */
export function parseChip<T>(text: string, columns: GcpColumn<T>[]): FilterChip | null {
  const trimmed = text.trim();
  if (!trimmed) return null;
  const colon = trimmed.indexOf(":");
  if (colon > 0) {
    const header = trimmed.slice(0, colon).trim().toLowerCase();
    const column = columns.find((candidate) => candidate.header.toLowerCase() === header);
    const value = trimmed.slice(colon + 1).trim();
    if (column && value) return { column: column.id, value };
  }
  return { value: trimmed };
}

/** The filter bar above every resource table: "Filter  Enter property name or value". */
export function FilterBar<T>({
  columns,
  chips,
  onChange,
}: {
  columns: GcpColumn<T>[];
  chips: FilterChip[];
  onChange: (chips: FilterChip[]) => void;
}) {
  const [text, setText] = useState("");
  const [suggest, setSuggest] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const listId = useId();
  useDismiss(wrapper, suggest, () => setSuggest(false));
  const properties = columns.filter((column) => column.header && !text.includes(":"));
  const shownProperties = properties.filter((column) => column.header.toLowerCase().startsWith(text.trim().toLowerCase()));

  function commit() {
    const chip = parseChip(text, columns);
    if (chip) onChange([...chips, chip]);
    setText("");
    setSuggest(false);
  }

  return (
    <div className="gc-filter-bar" ref={wrapper}>
      <Icon name="filter_list" size="1.25em" />
      <span className="gc-filter-label">Filter</span>
      <ul className="gc-chips" aria-label="Active filters">
        {chips.map((chip, index) => {
          const header = columns.find((column) => column.id === chip.column)?.header;
          const label = header ? `${header}: ${chip.value}` : chip.value;
          return (
            <li key={`${label}-${index}`} className="gc-filter-chip">
              <span>{label}</span>
              <button
                type="button"
                aria-label={`Remove filter ${label}`}
                onClick={() => onChange(chips.filter((_, candidate) => candidate !== index))}
              >
                <Icon name="close" size="1em" />
              </button>
            </li>
          );
        })}
      </ul>
      <input
        ref={input}
        className="gc-filter-input"
        type="text"
        role="combobox"
        aria-label="Filter"
        aria-expanded={suggest && shownProperties.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        placeholder="Enter property name or value"
        value={text}
        onFocus={() => setSuggest(true)}
        onBlur={() => setSuggest(false)}
        onChange={(event) => {
          setText(event.target.value);
          setSuggest(true);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          } else if (event.key === "Backspace" && text === "" && chips.length > 0) {
            onChange(chips.slice(0, -1));
          } else if (event.key === "Escape") {
            setSuggest(false);
          }
        }}
      />
      {chips.length > 0 ? (
        <button type="button" className="gc-icon-button gc-icon-button-small" aria-label="Clear filters" title="Clear filters" onClick={() => onChange([])}>
          <Icon name="close" size="1.25em" />
        </button>
      ) : null}
      {suggest && shownProperties.length > 0 ? (
        <ul className="gc-filter-suggest" role="listbox" id={listId} aria-label="Properties">
          {shownProperties.map((column) => (
            <li
              key={column.id}
              role="option"
              aria-selected={false}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => {
                setText(`${column.header}: `);
                input.current?.focus();
              }}
            >
              {column.header}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function ColumnOptionsDialog<T>({
  columns,
  visible,
  onClose,
  onSave,
}: {
  columns: GcpColumn<T>[];
  visible: string[];
  onClose: () => void;
  onSave: (visible: string[]) => void;
}) {
  const [draft, setDraft] = useState(visible);
  return (
    <GcpDialog title="Column display options" testId="column-options" onClose={onClose}>
      <fieldset className="gc-checklist">
        <legend className="gc-visually-hidden">Columns</legend>
        {columns.map((column, index) => (
          <label key={column.id} className="gc-checkbox">
            <input
              type="checkbox"
              checked={draft.includes(column.id)}
              disabled={index === 0}
              onChange={(event) =>
                setDraft((current) =>
                  event.target.checked ? [...current, column.id] : current.filter((id) => id !== column.id),
                )
              }
            />
            {column.header}
          </label>
        ))}
      </fieldset>
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          onClick={() => onSave(columns.filter((column) => draft.includes(column.id)).map((column) => column.id))}
        >
          OK
        </button>
      </div>
    </GcpDialog>
  );
}

const PAGE_SIZES = [10, 25, 50, 100];

export function EmptyState({ empty }: { empty: GcpEmptyState }) {
  return (
    <div className="gc-empty">
      <p className="gc-empty-headline">{empty.headline}</p>
      <p className="gc-empty-description">{empty.description}</p>
      {empty.sideEffect ? <p className="gc-empty-sideeffect">{empty.sideEffect}</p> : null}
    </div>
  );
}

export interface ResourceTableViewProps<T> {
  rows: T[] | undefined;
  isLoading: boolean;
  error: unknown;
  columns: GcpColumn<T>[];
  rowKey: (row: T) => string;
  resourceNoun: string;
  empty: GcpEmptyState;
  /** Identifies the table for test selectors and the remembered column choice. */
  tableId: string;
  selectable?: boolean;
  selected?: Set<string>;
  onSelectedChange?: (selected: Set<string>) => void;
  rowActions?: (row: T) => MenuEntry[];
  rowLabel?: (row: T) => string;
}

/** The filterable, sortable, paginated table body every resource list uses. */
export function ResourceTableView<T>({
  rows: data,
  isLoading,
  error,
  columns,
  rowKey,
  resourceNoun,
  empty,
  tableId,
  selectable,
  selected = new Set(),
  onSelectedChange,
  rowActions,
  rowLabel,
}: ResourceTableViewProps<T>) {
  const storageKey = `gcp-console-columns:${tableId}`;
  const [chips, setChips] = useState<FilterChip[]>([]);
  const [sort, setSort] = useState<{ column: string; ascending: boolean } | null>(null);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(PAGE_SIZES[1]);
  const [visibleIds, setVisibleIds] = useState<string[]>(
    () => readStoredColumns(storageKey) ?? columns.filter((column) => !column.defaultHidden).map((column) => column.id),
  );
  const [columnOptions, setColumnOptions] = useState(false);
  const visibleColumns = columns.filter((column) => visibleIds.includes(column.id));

  const rows = useMemo(() => {
    const matched = filterRows(data ?? [], columns, chips);
    const column = sort ? columns.find((candidate) => candidate.id === sort.column) : undefined;
    if (!sort || !column) return matched;
    return [...matched].sort((left, right) => {
      const comparison = column.value(left).localeCompare(column.value(right), undefined, { numeric: true });
      return sort.ascending ? comparison : -comparison;
    });
  }, [data, chips, sort, columns]);

  useEffect(() => setPage(0), [chips, pageSize]);

  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const visible = rows.slice(currentPage * pageSize, currentPage * pageSize + pageSize);
  const allVisibleSelected = visible.length > 0 && visible.every((row) => selected.has(rowKey(row)));
  const columnCount = visibleColumns.length + (selectable ? 1 : 0) + (rowActions ? 1 : 0);
  const label = rowLabel ?? rowKey;

  function toggle(ids: string[], on: boolean) {
    const next = new Set(selected);
    for (const id of ids) {
      if (on) next.add(id);
      else next.delete(id);
    }
    onSelectedChange?.(next);
  }

  return (
    <div className="gc-table-card">
      <div className="gc-table-tools">
        <FilterBar columns={columns} chips={chips} onChange={setChips} />
        <button
          type="button"
          className="gc-icon-button"
          aria-label="Column display options"
          title="Column display options"
          onClick={() => setColumnOptions(true)}
        >
          <Icon name="view_column" size="1.25em" />
        </button>
      </div>
      <div className="gc-table-wrap">
        <table className="gc-table" data-testid={tableId}>
          <thead>
            <tr>
              {selectable ? (
                <th className="gc-table-select" scope="col">
                  <input
                    type="checkbox"
                    aria-label={`Select all ${resourceNoun} on this page`}
                    checked={allVisibleSelected}
                    disabled={visible.length === 0}
                    onChange={() => toggle(visible.map(rowKey), !allVisibleSelected)}
                  />
                </th>
              ) : null}
              {visibleColumns.map((column) => {
                const active = sort?.column === column.id;
                return (
                  <th key={column.id} scope="col" aria-sort={active ? (sort.ascending ? "ascending" : "descending") : undefined}>
                    {column.header ? (
                      <button
                        type="button"
                        onClick={() =>
                          setSort((current) =>
                            current?.column === column.id
                              ? { column: column.id, ascending: !current.ascending }
                              : { column: column.id, ascending: true },
                          )
                        }
                      >
                        {column.header}
                        {column.help ? (
                          <span className="gc-header-help" title={column.help}>
                            <Icon name="help" size="1em" />
                            <span className="gc-visually-hidden">{column.help}</span>
                          </span>
                        ) : null}
                        <Icon
                          name={active && !sort.ascending ? "arrow_downward" : "arrow_upward"}
                          size="1.1em"
                          className={active ? "gc-sort gc-sort-active" : "gc-sort"}
                        />
                      </button>
                    ) : (
                      <span className="gc-visually-hidden">{column.id}</span>
                    )}
                  </th>
                );
              })}
              {rowActions ? (
                <th scope="col" className="gc-table-actions">
                  <span className="gc-visually-hidden">Actions</span>
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {error ? (
              <tr>
                <td className="gc-table-state" colSpan={columnCount}>
                  <div className="gc-message gc-message-error" role="alert">
                    <Icon name="error_fill" />
                    <span>
                      <strong>Couldn't load {resourceNoun}.</strong>{" "}
                      {error instanceof Error ? error.message : "The API did not respond."}
                    </span>
                  </div>
                </td>
              </tr>
            ) : isLoading ? (
              <tr>
                <td className="gc-table-state" colSpan={columnCount}>
                  <div className="gc-loading" role="status">
                    <span className="gc-spinner" aria-hidden />
                    Loading {resourceNoun}…
                  </div>
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td className="gc-table-state" colSpan={columnCount}>
                  {chips.length > 0 ? (
                    <p className="gc-table-nomatch">No rows to display</p>
                  ) : (
                    <EmptyState empty={empty} />
                  )}
                </td>
              </tr>
            ) : (
              visible.map((row) => {
                const id = rowKey(row);
                const isSelected = selected.has(id);
                return (
                  <tr key={id} className={isSelected ? "gc-row-selected" : undefined}>
                    {selectable ? (
                      <td className="gc-table-select">
                        <input
                          type="checkbox"
                          aria-label={`Select ${label(row)}`}
                          checked={isSelected}
                          onChange={() => toggle([id], !isSelected)}
                        />
                      </td>
                    ) : null}
                    {visibleColumns.map((column) => (
                      <td key={column.id}>{column.cell(row)}</td>
                    ))}
                    {rowActions ? (
                      <td className="gc-table-actions">
                        <MenuButton label={`Actions for ${label(row)}`} icon="more_vert" entries={rowActions(row)} />
                      </td>
                    ) : null}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      {rows.length > 0 ? (
        <div className="gc-pagination">
          <label>
            Rows per page:
            <select value={pageSize} onChange={(event) => setPageSize(Number(event.target.value))}>
              {PAGE_SIZES.map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </select>
          </label>
          <span>{`${currentPage * pageSize + 1} – ${currentPage * pageSize + visible.length} of ${rows.length}`}</span>
          <button
            type="button"
            className="gc-icon-button gc-icon-button-small"
            aria-label="Previous page"
            disabled={currentPage === 0}
            onClick={() => setPage(currentPage - 1)}
          >
            <Icon name="chevron_left" />
          </button>
          <button
            type="button"
            className="gc-icon-button gc-icon-button-small"
            aria-label="Next page"
            disabled={currentPage >= pageCount - 1}
            onClick={() => setPage(currentPage + 1)}
          >
            <Icon name="chevron_right" />
          </button>
        </div>
      ) : null}
      {columnOptions ? (
        <ColumnOptionsDialog
          columns={columns}
          visible={visibleIds}
          onClose={() => setColumnOptions(false)}
          onSave={(next) => {
            setVisibleIds(next);
            storeColumns(storageKey, next);
            setColumnOptions(false);
          }}
        />
      ) : null}
    </div>
  );
}

export interface GcpResourceTableProps<T> {
  title?: string;
  description?: ReactNode;
  actions?: GcpPageAction[];
  /** Actions on the checked rows, shown in the action bar and enabled once a row is checked. */
  selectionActions?: (selected: T[], clear: () => void) => GcpPageAction[];
  rowActions?: (row: T) => MenuEntry[];
  columns: GcpColumn<T>[];
  queryKey: unknown[];
  queryFn: () => Promise<T[]>;
  /** Kept for callers that name the resource in their filter; the bar's own prompt is the real console's. */
  filterPlaceholder?: string;
  empty: GcpEmptyState;
  rowKey: (row: T) => string;
  rowLabel?: (row: T) => string;
  resourceNoun: string;
  testId?: string;
  backTo?: string;
  children?: ReactNode;
}

/** A product's resource list page: the title and action bar, then the table. */
export function GcpResourceTable<T>({
  title,
  description,
  actions,
  selectionActions,
  rowActions,
  columns,
  queryKey,
  queryFn,
  empty,
  rowKey,
  rowLabel,
  resourceNoun,
  testId,
  backTo,
  children,
}: GcpResourceTableProps<T>) {
  const { data, isLoading, error, refetch, isFetching } = useQuery({ queryKey, queryFn });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const selectedRows = (data ?? []).filter((row) => selected.has(rowKey(row)));
  const clear = () => setSelected(new Set());
  const bulk = selectionActions
    ? selectionActions(selectedRows, clear).map((action) => ({
        ...action,
        disabled: action.disabled || selectedRows.length === 0,
      }))
    : [];
  const tableId = testId ?? `${resourceNoun.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}-table`;
  return (
    <>
      {title ? (
        <GcpPageHeader
          title={title}
          description={description}
          actions={[...(actions ?? []), ...bulk]}
          onRefresh={() => void refetch()}
          refreshing={isFetching}
          backTo={backTo}
        />
      ) : null}
      {children}
      <ResourceTableView
        rows={data}
        isLoading={isLoading}
        error={error}
        columns={columns}
        rowKey={rowKey}
        rowLabel={rowLabel}
        resourceNoun={resourceNoun}
        empty={empty}
        tableId={tableId}
        selectable={Boolean(selectionActions)}
        selected={selected}
        onSelectedChange={setSelected}
        rowActions={rowActions}
      />
    </>
  );
}
