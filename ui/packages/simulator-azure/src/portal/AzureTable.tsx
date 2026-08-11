import { useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  makeStyles,
  tokens,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  TableSelectionCell,
  Input,
  Button,
  Text,
} from "@fluentui/react-components";
import { ChevronLeftRegular, ChevronRightRegular } from "@fluentui/react-icons";
import {
  AzureCommandBar,
  AzureConfirmDialog,
  AzureEssentials,
  AzureEmptyState,
  AzureErrorMessage,
  type EssentialsProperty,
  type PortalCommand,
} from "./AzurePortal.js";

export interface AzureColumn<T> {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Plain text used for filtering and sorting, so both act on what is shown. */
  value: (row: T) => string;
}

export interface AzureResourceTableProps<T> {
  columns: AzureColumn<T>[];
  queryKey: unknown[];
  queryFn: () => Promise<T[]>;
  filterPlaceholder: string;
  emptyTitle: string;
  emptyDescription: string;
  rowKey: (row: T) => string;
  /** Resource-level properties the portal leads the pane with. */
  essentials: (rows: T[]) => EssentialsProperty[];
  resourceNoun: string;
  /** Extra command-bar entries a page contributes ahead of the table's own
   *  Refresh/Delete/Assign tags/Export/Feedback set — e.g. a page's own
   *  "Create" entry point. Without it the bar carries only the standard
   *  commands, exactly as it did before this prop existed. */
  extraCommands?: PortalCommand[];
  /** Content rendered between Essentials and the filter/table — a page's own
   *  inline create form or mutation-error banner. */
  banner?: ReactNode;
  /** Deletes the given rows through the resource's real ARM DELETE
   *  operation — one call per row, the same shape the real portal's
   *  multi-select delete and `az` bulk delete both use. When provided, the
   *  table's Delete command becomes real: it opens a confirm dialog naming
   *  the selected rows, then on confirm calls this, clears the selection,
   *  and refetches the list. Omitted keeps Delete disabled, exactly as it
   *  was before this prop existed. */
  onDelete?: (rows: T[]) => Promise<void>;
  /** The consequence copy the confirm dialog shows beneath its title —
   *  specific to what deleting this resource destroys (e.g. "removes every
   *  repository and image it holds"). Required alongside `onDelete`. */
  deleteWarning?: ReactNode;
  /** The base test id for the delete command and its confirm dialog
   *  (`{deleteTestId}`, `{deleteTestId}-confirm`, `{deleteTestId}-cancel`,
   *  `{deleteTestId}-error`). Required alongside `onDelete`. */
  deleteTestId?: string;
  /** How the confirm dialog names a row; defaults to the first column's
   *  cell text (every caller's first column is the resource name). */
  rowLabel?: (row: T) => string;
}

const PAGE_SIZE = 10;

const useStyles = makeStyles({
  tools: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: "16px", marginBottom: "8px" },
  filter: { flex: 1, maxWidth: "420px" },
  pagination: { display: "flex", alignItems: "center", gap: "4px", color: tokens.colorNeutralForeground2 },
  stateCell: { padding: 0 },
});

/** The three states every sub-resource table on a detail blade renders
 *  instead of its rows — a load failure, the initial load, or a genuinely
 *  empty resource — each a single wide cell spanning every column, exactly
 *  the shape the hand-built `<table>` ternary used to draw, now built from
 *  `AzureErrorMessage`/`AzureEmptyState` (real Fluent `MessageBar`/`Spinner`)
 *  inside a real `TableRow`/`TableCell`. */
export function AzureTableErrorRow({
  colSpan,
  testid,
  children,
}: {
  colSpan: number;
  testid?: string;
  children: ReactNode;
}) {
  const styles = useStyles();
  return (
    <TableRow>
      <TableCell className={styles.stateCell} colSpan={colSpan}>
        <AzureErrorMessage testid={testid}>{children}</AzureErrorMessage>
      </TableCell>
    </TableRow>
  );
}

export function AzureTableLoadingRow({ colSpan, label }: { colSpan: number; label: string }) {
  const styles = useStyles();
  return (
    <TableRow>
      <TableCell className={styles.stateCell} colSpan={colSpan}>
        <AzureEmptyState title={label} loading />
      </TableCell>
    </TableRow>
  );
}

export function AzureTableEmptyRow({
  colSpan,
  title,
  description,
}: {
  colSpan: number;
  title: string;
  description?: string;
}) {
  const styles = useStyles();
  return (
    <TableRow>
      <TableCell className={styles.stateCell} colSpan={colSpan}>
        <AzureEmptyState title={title} description={description} />
      </TableCell>
    </TableRow>
  );
}

/** The real Fluent `Table` primitives (not the packaged `DataGrid`
 *  orchestrator) composed by hand, so this portal keeps exact control over
 *  the accessible names Fluent's own `DataGrid` selection wiring doesn't
 *  expose a hook for (`Select <id>`, `Select all resources on this page`) —
 *  the same shape the hand-built table held, now built from genuine
 *  `Table`/`TableRow`/`TableSelectionCell` components rather than a raw
 *  `<table>`. */
export function AzureResourceTable<T>({
  columns,
  queryKey,
  queryFn,
  filterPlaceholder,
  emptyTitle,
  emptyDescription,
  rowKey,
  essentials,
  resourceNoun,
  extraCommands,
  banner,
  onDelete,
  deleteWarning,
  deleteTestId,
  rowLabel,
}: AzureResourceTableProps<T>) {
  const styles = useStyles();
  const queryClient = useQueryClient();
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({ queryKey, queryFn });
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<{ column: string; ascending: boolean } | null>(null);
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState(false);

  const labelOf = rowLabel ?? ((row: T) => columns[0]?.value(row) ?? rowKey(row));
  const selectedRows = useMemo(() => (data ?? []).filter((row) => selected.has(rowKey(row))), [data, selected, rowKey]);

  const remove = useMutation({
    mutationFn: async () => {
      if (!onDelete) return;
      await onDelete(selectedRows);
    },
    onSuccess: () => {
      setSelected(new Set());
      setDeleting(false);
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: () => {
      // A failed Azure Resource Manager deletion remains actionable in the
      // confirmation surface even if Fluent received a concurrent dismiss
      // event while the request was settling.
      setDeleting(true);
    },
  });

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
      <AzureCommandBar
        commands={[
          ...(extraCommands ?? []),
          { label: "Refresh", icon: "refresh", onSelect: () => void refetch(), disabled: isFetching },
          {
            label: "Delete",
            icon: "delete",
            testid: deleteTestId,
            disabled: selected.size === 0 || !onDelete,
            onSelect: onDelete
              ? () => {
                  remove.reset();
                  setDeleting(true);
                }
              : undefined,
          },
          { label: "Assign tags", icon: "tag", disabled: selected.size === 0 },
          { label: "Export to CSV", icon: "download", disabled: rows.length === 0 },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      <div className="az-main">
        <AzureEssentials properties={essentials(data ?? [])} />
        {banner}
        <div className={styles.tools}>
          <Input
            className={styles.filter}
            type="search"
            value={filter}
            placeholder={filterPlaceholder}
            aria-label={filterPlaceholder}
            onChange={(_, changeData) => {
              setFilter(changeData.value);
              setPage(0);
            }}
          />
          <div className={styles.pagination}>
            <Text>
              {rows.length === 0
                ? `No ${resourceNoun}`
                : `${currentPage * PAGE_SIZE + 1}–${currentPage * PAGE_SIZE + visible.length} of ${rows.length}`}
            </Text>
            <Button
              appearance="subtle"
              icon={<ChevronLeftRegular />}
              aria-label="Previous page"
              disabled={currentPage === 0}
              onClick={() => setPage(currentPage - 1)}
            />
            <Text aria-current="page">{currentPage + 1}</Text>
            <Button
              appearance="subtle"
              icon={<ChevronRightRegular />}
              aria-label="Next page"
              disabled={currentPage >= pageCount - 1}
              onClick={() => setPage(currentPage + 1)}
            />
          </div>
        </div>

        <Table aria-label={resourceNoun} size="small" className="az-table">
          <TableHeader>
            <TableRow>
              <TableSelectionCell
                type="checkbox"
                checked={allVisibleSelected}
                checkboxIndicator={{ "aria-label": "Select all resources on this page" }}
                onClick={() =>
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
              {columns.map((column) => {
                const active = sort?.column === column.id;
                return (
                  <TableHeaderCell
                    key={column.id}
                    sortable
                    sortDirection={active ? (sort.ascending ? "ascending" : "descending") : undefined}
                    onClick={() => toggleSort(column.id)}
                  >
                    {column.header}
                  </TableHeaderCell>
                );
              })}
            </TableRow>
          </TableHeader>
          <TableBody>
            {isError ? (
              <TableRow>
                <TableCell className={styles.stateCell} colSpan={columns.length + 1}>
                  <AzureErrorMessage>
                    <strong>Could not load {resourceNoun}.</strong>{" "}
                    {error instanceof Error ? error.message : "The simulator did not respond."}
                  </AzureErrorMessage>
                </TableCell>
              </TableRow>
            ) : isLoading ? (
              <TableRow>
                <TableCell className={styles.stateCell} colSpan={columns.length + 1}>
                  <AzureEmptyState title={`Loading ${resourceNoun}…`} loading />
                </TableCell>
              </TableRow>
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell className={styles.stateCell} colSpan={columns.length + 1}>
                  <AzureEmptyState title={emptyTitle} description={emptyDescription} />
                </TableCell>
              </TableRow>
            ) : (
              visible.map((row) => {
                const id = rowKey(row);
                return (
                  <TableRow key={id} appearance={selected.has(id) ? "neutral" : undefined}>
                    <TableSelectionCell
                      type="checkbox"
                      checked={selected.has(id)}
                      checkboxIndicator={{ "aria-label": `Select ${id}` }}
                      onClick={() =>
                        setSelected((current) => {
                          const next = new Set(current);
                          if (next.has(id)) next.delete(id);
                          else next.add(id);
                          return next;
                        })
                      }
                    />
                    {columns.map((column) => (
                      <TableCell key={column.id}>{column.cell(row)}</TableCell>
                    ))}
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>
      {onDelete && deleteTestId ? (
        <AzureConfirmDialog
          // The surface gets its own id rather than the trigger's: two elements
          // answering to one test id leaves a test unable to say which it
          // meant, which is what pushed these assertions onto `*ByRole`
          // lookups that Fluent's re-render makes flaky by momentarily hiding
          // the retiring surface from the accessibility tree. The detail pages
          // already name their dialogs this way.
          open={deleting}
          title={
            selectedRows.length === 1
              ? `Delete ${labelOf(selectedRows[0])}?`
              : `Delete ${selectedRows.length} ${resourceNoun}?`
          }
          busy={remove.isPending}
          testid={`${deleteTestId}-dialog`}
          error={
            remove.isError ? (
              <>
                <strong>Could not delete.</strong>{" "}
                {remove.error instanceof Error ? remove.error.message : "Azure Resource Manager did not respond."}
              </>
            ) : undefined
          }
          onConfirm={() => remove.mutate()}
          onCancel={() => {
            if (remove.isPending) return;
            setDeleting(false);
            remove.reset();
          }}
        >
          <Text as="p">{deleteWarning}</Text>
          <ul>
            {selectedRows.map((row) => (
              <li key={rowKey(row)}>
                <code>{labelOf(row)}</code>
              </li>
            ))}
          </ul>
        </AzureConfirmDialog>
      ) : null}
    </>
  );
}
