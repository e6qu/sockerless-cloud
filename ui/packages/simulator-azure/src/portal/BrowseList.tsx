import { useMemo, useState, type ReactNode } from "react";
import { Link as RouterLink } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Dropdown,
  Input,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Option,
  Popover,
  PopoverSurface,
  PopoverTrigger,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
  ToolbarButton,
} from "@fluentui/react-components";
import type { ArmGenericResource } from "../api.js";
import { deleteResource, listSubscriptions, parseResourceId, type Subscription } from "../azure.js";
import type { ArmPage } from "./arm.js";
import { CommandBar, ConfirmDialog, EmptyState, ErrorBar, errorText, type PortalCommand } from "./AzurePortal.js";
import { AzureIcon } from "./icons.js";
import { useNotifications } from "./notifications.js";
import { resourceRoute } from "./routes.js";

export interface BrowseColumn {
  id: string;
  header: string;
  value: (resource: ArmGenericResource, subscriptions: Subscription[]) => string;
  cell?: (resource: ArmGenericResource) => ReactNode;
}

export interface BrowseSource {
  /** Identifies the list in the query cache and in remembered column choices. */
  key: string;
  fetchPage: (subscriptionId: string, top: number, next?: string) => Promise<ArmPage<ArmGenericResource>>;
}

export interface BrowseListProps {
  source: BrowseSource;
  /** Every column the operator can show; the first is the name. */
  columns: BrowseColumn[];
  defaultColumns: string[];
  noun: string;
  createLabel?: string;
  onCreate?: () => void;
  /** Narrows rows the source returns, the way two blades split one type. */
  rowFilter?: (resource: ArmGenericResource) => boolean;
  /** Adds the Type filter pill (All resources). */
  typeFilter?: boolean;
  typeLabel?: (resource: ArmGenericResource) => string;
  emptyTitle: string;
  emptyDescription: string;
  testid: string;
}

const PAGE_SIZES = [10, 20, 50, 100];

interface Cursor {
  subscriptionIndex: number;
  next?: string;
}

function columnStorageKey(key: string) {
  return `azure-portal:columns:${key}`;
}

function loadColumns(key: string, fallback: string[]): string[] {
  try {
    const raw = window.localStorage.getItem(columnStorageKey(key));
    return raw ? (JSON.parse(raw) as string[]) : fallback;
  } catch {
    return fallback;
  }
}

function saveColumns(key: string, columns: string[]) {
  try {
    window.localStorage.setItem(columnStorageKey(key), JSON.stringify(columns));
  } catch {
    // Storage blocked: the choice holds for this page.
  }
}

function FilterPill({
  label,
  options,
  selected,
  onChange,
}: {
  label: string;
  options: { value: string; label: string }[];
  selected: string[];
  onChange: (values: string[]) => void;
}) {
  const summary = selected.length === 0 ? "all" : selected.length === 1 ? (options.find((option) => option.value === selected[0])?.label ?? selected[0]) : `${selected.length} selected`;
  return (
    <Popover positioning="below-start" trapFocus>
      <PopoverTrigger disableButtonEnhancement>
        <button type="button" className="az-pill">
          {label} <span className="az-pill-op">==</span> <strong>{summary}</strong>
        </button>
      </PopoverTrigger>
      <PopoverSurface aria-label={`${label} filter`} className="az-pill-surface">
        <Text weight="semibold" block>
          {label}
        </Text>
        <div className="az-pill-options" role="group" aria-label={label}>
          <Checkbox label="Select all" checked={selected.length === 0} onChange={() => onChange([])} />
          {options.map((option) => (
            <Checkbox
              key={option.value}
              label={option.label}
              checked={selected.includes(option.value)}
              onChange={(_, data) =>
                onChange(data.checked ? [...selected, option.value] : selected.filter((value) => value !== option.value))
              }
            />
          ))}
        </div>
      </PopoverSurface>
    </Popover>
  );
}

function EditColumnsDialog({
  open,
  columns,
  selected,
  onApply,
  onClose,
}: {
  open: boolean;
  columns: BrowseColumn[];
  selected: string[];
  onApply: (columns: string[]) => void;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState(selected);
  return (
    <Dialog open={open} onOpenChange={(_, data) => (data.open ? setDraft(selected) : onClose())}>
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Edit columns</DialogTitle>
          <DialogContent className="az-dialog-content">
            <Text>Choose the columns the list shows.</Text>
            <div className="az-column-choices" role="group" aria-label="Columns">
              {columns.map((column, index) => (
                <Checkbox
                  key={column.id}
                  label={column.header}
                  disabled={index === 0}
                  checked={index === 0 || draft.includes(column.id)}
                  onChange={(_, data) => setDraft(data.checked ? [...draft, column.id] : draft.filter((id) => id !== column.id))}
                />
              ))}
            </div>
          </DialogContent>
          <DialogActions>
            <Button appearance="primary" onClick={() => onApply(draft)}>
              Save
            </Button>
            <Button onClick={onClose}>Cancel</Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

function toCsv(columns: BrowseColumn[], rows: ArmGenericResource[], subscriptions: Subscription[]): string {
  const escape = (text: string) => (/[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text);
  return [columns.map((column) => escape(column.header)).join(","), ...rows.map((row) => columns.map((column) => escape(column.value(row, subscriptions))).join(","))].join("\n");
}

/**
 * The browse list every service and All resources open on: the command bar,
 * the filter box and filter pills, the grid with row selection, and paging
 * that follows the list operation's nextLink.
 */
export function BrowseList({
  source,
  columns,
  defaultColumns,
  noun,
  createLabel = "Create",
  onCreate,
  rowFilter,
  typeFilter,
  typeLabel,
  emptyTitle,
  emptyDescription,
  testid,
}: BrowseListProps) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const [text, setText] = useState("");
  const [subscriptionFilter, setSubscriptionFilter] = useState<string[]>([]);
  const [groupFilter, setGroupFilter] = useState<string[]>([]);
  const [locationFilter, setLocationFilter] = useState<string[]>([]);
  const [typeFilterValues, setTypeFilterValues] = useState<string[]>([]);
  const [visible, setVisible] = useState(() => loadColumns(source.key, defaultColumns));
  const [editingColumns, setEditingColumns] = useState(false);
  const [pageSize, setPageSize] = useState(50);
  const [cursors, setCursors] = useState<Cursor[]>([{ subscriptionIndex: 0 }]);
  const [selected, setSelected] = useState<string[]>([]);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [deleteBusy, setDeleteBusy] = useState(false);

  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const inScope = useMemo(
    () => (subscriptions.data ?? []).filter((sub) => subscriptionFilter.length === 0 || subscriptionFilter.includes(sub.subscriptionId)),
    [subscriptions.data, subscriptionFilter],
  );
  const cursor = cursors[cursors.length - 1];

  const page = useQuery({
    queryKey: ["browse", source.key, inScope.map((sub) => sub.subscriptionId), pageSize, cursor],
    enabled: Boolean(subscriptions.data),
    queryFn: async () => {
      // A page holds up to pageSize rows from the subscriptions in scope, in
      // order, following each list's nextLink before moving to the next.
      const rows: ArmGenericResource[] = [];
      let index = cursor.subscriptionIndex;
      let next = cursor.next;
      while (index < inScope.length && rows.length < pageSize) {
        const result = await source.fetchPage(inScope[index].subscriptionId, pageSize - rows.length, next);
        rows.push(...result.value);
        if (result.nextLink) {
          next = result.nextLink;
          if (rows.length >= pageSize) break;
        } else {
          index += 1;
          next = undefined;
        }
      }
      const more: Cursor | undefined = index < inScope.length ? { subscriptionIndex: index, next } : undefined;
      return { rows, more };
    },
  });

  const allRows = (page.data?.rows ?? []).filter((row) => !rowFilter || rowFilter(row));
  const needle = text.trim().toLowerCase();
  const shownColumns = columns.filter((column, index) => index === 0 || visible.includes(column.id));
  const subs = subscriptions.data ?? [];
  const rows = allRows.filter((row) => {
    const parsed = parseResourceId(row.id);
    if (groupFilter.length && !groupFilter.includes((parsed.resourceGroup ?? "").toLowerCase())) return false;
    if (locationFilter.length && !locationFilter.includes(row.location)) return false;
    if (typeFilterValues.length && !typeFilterValues.includes(row.type.toLowerCase())) return false;
    if (!needle) return true;
    return shownColumns.some((column) => column.value(row, subs).toLowerCase().includes(needle));
  });

  const unique = (values: string[]) => Array.from(new Set(values.filter(Boolean))).sort();
  const groups = unique(allRows.map((row) => (parseResourceId(row.id).resourceGroup ?? "").toLowerCase()));
  const locations = unique(allRows.map((row) => row.location));
  const types = unique(allRows.map((row) => row.type.toLowerCase()));
  const selectedRows = rows.filter((row) => selected.includes(row.id));
  const allSelected = rows.length > 0 && selectedRows.length === rows.length;
  const pageNumber = cursors.length;
  const firstRecord = (pageNumber - 1) * pageSize + 1;

  const resetPaging = () => {
    setCursors([{ subscriptionIndex: 0 }]);
    setSelected([]);
  };
  const refresh = () => {
    setSelected([]);
    void queryClient.invalidateQueries({ queryKey: ["browse", source.key] });
  };

  const confirmDelete = async () => {
    setDeleteBusy(true);
    setDeleteError(undefined);
    const targets = selectedRows;
    try {
      await track(
        {
          progress: `Deleting ${targets.length} ${targets.length === 1 ? "resource" : "resources"}`,
          success: `Deleted ${targets.length} ${targets.length === 1 ? "resource" : "resources"}`,
          failure: "Delete failed",
          description: targets.map((row) => row.name).join(", "),
        },
        () => Promise.all(targets.map((row) => deleteResource(row.id, row.kind))),
      );
      setDeleting(false);
      refresh();
    } catch (error) {
      setDeleteError(errorText(error));
    } finally {
      setDeleteBusy(false);
    }
  };

  const commands: PortalCommand[] = [
    ...(onCreate ? [{ label: createLabel, icon: "add" as const, onSelect: onCreate, testid: `${testid}-create` }] : []),
    { label: "Refresh", icon: "refresh", onSelect: refresh, testid: `${testid}-refresh` },
    {
      label: "Export to CSV",
      icon: "download",
      onSelect: () => {
        const blob = new Blob([toCsv(shownColumns, rows, subs)], { type: "text/csv" });
        const link = document.createElement("a");
        link.href = URL.createObjectURL(blob);
        link.download = `${noun.replace(/\s+/g, "-")}.csv`;
        link.click();
        URL.revokeObjectURL(link.href);
      },
    },
    {
      label: "Delete",
      icon: "delete",
      divider: true,
      disabled: selectedRows.length === 0,
      onSelect: () => setDeleting(true),
      testid: `${testid}-delete`,
    },
  ];

  return (
    <div className="az-browse" data-testid={testid}>
      <div className="az-command-row">
        <CommandBar commands={commands} />
        <Menu>
          <MenuTrigger disableButtonEnhancement>
            <ToolbarButton appearance="subtle" icon={<AzureIcon name="columns" size={16} />} className="az-manage-view">
              Manage view
            </ToolbarButton>
          </MenuTrigger>
          <MenuPopover>
            <MenuList>
              <MenuItem onClick={() => setEditingColumns(true)}>Edit columns</MenuItem>
              <MenuItem
                onClick={() => {
                  setVisible(defaultColumns);
                  saveColumns(source.key, defaultColumns);
                  setText("");
                  setGroupFilter([]);
                  setLocationFilter([]);
                  setTypeFilterValues([]);
                  setSubscriptionFilter([]);
                  resetPaging();
                }}
              >
                Reset to default view
              </MenuItem>
            </MenuList>
          </MenuPopover>
        </Menu>
      </div>
      <EditColumnsDialog
        open={editingColumns}
        columns={columns}
        selected={visible}
        onClose={() => setEditingColumns(false)}
        onApply={(next) => {
          setVisible(next);
          saveColumns(source.key, next);
          setEditingColumns(false);
        }}
      />
      <div className="az-filters">
        <Input
          className="az-filter-text"
          value={text}
          onChange={(_, data) => setText(data.value)}
          contentBefore={<AzureIcon name="filter" size={14} />}
          placeholder="Filter for any field..."
          aria-label="Filter for any field"
        />
        <FilterPill
          label="Subscription"
          options={subs.map((sub) => ({ value: sub.subscriptionId, label: sub.displayName }))}
          selected={subscriptionFilter}
          onChange={(values) => {
            setSubscriptionFilter(values);
            resetPaging();
          }}
        />
        <FilterPill label="Resource group" options={groups.map((value) => ({ value, label: value }))} selected={groupFilter} onChange={setGroupFilter} />
        <FilterPill label="Location" options={locations.map((value) => ({ value, label: value }))} selected={locationFilter} onChange={setLocationFilter} />
        {typeFilter ? (
          <FilterPill
            label="Type"
            options={types.map((value) => ({ value, label: typeLabel ? typeLabel(allRows.find((row) => row.type.toLowerCase() === value)!) : value }))}
            selected={typeFilterValues}
            onChange={setTypeFilterValues}
          />
        ) : null}
      </div>
      <Text size={200} block className="az-records" role="status">
        {page.isLoading
          ? "Loading…"
          : rows.length === 0
            ? "No records."
            : page.data?.more
              ? `Showing ${firstRecord} to ${firstRecord + rows.length - 1} records. More records follow.`
              : `Showing ${firstRecord} to ${firstRecord + rows.length - 1} of ${firstRecord + rows.length - 1} records.`}
      </Text>
      {page.isError || subscriptions.isError ? (
        <ErrorBar title={`The ${noun} could not be listed.`} testid={`${testid}-error`}>
          {errorText(page.error ?? subscriptions.error)}
        </ErrorBar>
      ) : null}
      <div className="az-grid-wrap">
        <Table aria-label={noun} size="small" className="az-grid" data-testid={`${testid}-table`}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell className="az-grid-select">
                <Checkbox
                  aria-label="Select all"
                  checked={allSelected ? true : selectedRows.length > 0 ? "mixed" : false}
                  onChange={() => setSelected(allSelected ? [] : rows.map((row) => row.id))}
                />
              </TableHeaderCell>
              {shownColumns.map((column) => (
                <TableHeaderCell key={column.id}>{column.header}</TableHeaderCell>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.id} aria-selected={selected.includes(row.id)}>
                <TableCell className="az-grid-select">
                  <Checkbox
                    aria-label={`Select ${row.name}`}
                    checked={selected.includes(row.id)}
                    onChange={(_, data) => setSelected(data.checked ? [...selected, row.id] : selected.filter((id) => id !== row.id))}
                  />
                </TableCell>
                {shownColumns.map((column, index) => (
                  <TableCell key={column.id}>
                    {index === 0 ? (
                      <RouterLink to={resourceRoute(row.id)} className="az-grid-name">
                        {column.cell ? column.cell(row) : column.value(row, subs)}
                      </RouterLink>
                    ) : column.cell ? (
                      column.cell(row)
                    ) : (
                      column.value(row, subs) || "—"
                    )}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {page.isLoading ? <EmptyState title={`Loading ${noun}…`} loading /> : null}
        {!page.isLoading && !page.isError && rows.length === 0 ? (
          <EmptyState
            title={allRows.length === 0 ? emptyTitle : `No ${noun} match the filters`}
            description={allRows.length === 0 ? emptyDescription : "Change or clear the filters to see more."}
            action={
              allRows.length === 0 && onCreate ? (
                <Button appearance="primary" onClick={onCreate}>
                  {createLabel === "Create" ? `Create ${noun.replace(/s$/, "")}` : createLabel}
                </Button>
              ) : undefined
            }
          />
        ) : null}
      </div>
      <div className="az-paging">
        <Button size="small" disabled={cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>
          Previous
        </Button>
        <Text size={200}>Page {pageNumber}</Text>
        <Button size="small" disabled={!page.data?.more} onClick={() => page.data?.more && setCursors([...cursors, page.data.more])}>
          Next
        </Button>
        <label className="az-page-size">
          <Text size={200}>Page size</Text>
          <Dropdown
            size="small"
            aria-label="Page size"
            value={String(pageSize)}
            selectedOptions={[String(pageSize)]}
            onOptionSelect={(_, data) => {
              setPageSize(Number(data.optionValue));
              resetPaging();
            }}
          >
            {PAGE_SIZES.map((size) => (
              <Option key={size} value={String(size)}>
                {String(size)}
              </Option>
            ))}
          </Dropdown>
        </label>
      </div>
      <ConfirmDialog
        open={deleting}
        title={`Delete ${selectedRows.length === 1 ? selectedRows[0]?.name : `${selectedRows.length} resources`}`}
        busy={deleteBusy}
        testid={`${testid}-delete-dialog`}
        error={deleteError}
        confirmText={selectedRows.length > 1 ? "delete" : undefined}
        onConfirm={() => void confirmDelete()}
        onCancel={() => {
          setDeleting(false);
          setDeleteError(undefined);
        }}
      >
        <Text block>Deleting removes these resources and the data they hold. This action can't be undone.</Text>
        <ul className="az-delete-list">
          {selectedRows.map((row) => (
            <li key={row.id}>{row.name}</li>
          ))}
        </ul>
      </ConfirmDialog>
    </div>
  );
}
