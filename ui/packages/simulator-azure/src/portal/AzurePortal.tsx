import { Fragment, useEffect, useState, type ReactNode } from "react";
import { Link as RouterLink, useLocation } from "react-router";
import {
  Badge,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Spinner,
  Text,
  Toolbar,
  ToolbarButton,
  ToolbarDivider,
  Tooltip,
  type BadgeProps,
} from "@fluentui/react-components";
import { AzureIcon, ServiceTile, type AzureIconName } from "./icons.js";
import { toggleFavorite, usePreferences } from "./preferences.js";

export interface Crumb {
  label: string;
  to?: string;
}

/** The page title the browser tab shows, as the portal titles it. */
export function usePageTitle(title: string) {
  useEffect(() => {
    document.title = title ? `${title} - Microsoft Azure` : "Microsoft Azure";
  }, [title]);
}

export function Breadcrumbs({ trail }: { trail: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="az-breadcrumb">
      <ol>
        {trail.map((crumb, index) => (
          <Fragment key={`${crumb.label}-${index}`}>
            <li>
              {crumb.to && index < trail.length - 1 ? (
                <RouterLink to={crumb.to}>{crumb.label}</RouterLink>
              ) : (
                <span aria-current={index === trail.length - 1 ? "page" : undefined}>{crumb.label}</span>
              )}
            </li>
            {index < trail.length - 1 ? (
              <li aria-hidden className="az-breadcrumb-sep">
                <AzureIcon name="chevron_right" size={12} />
              </li>
            ) : null}
          </Fragment>
        ))}
      </ol>
    </nav>
  );
}

export interface BladeProps {
  title: string;
  subtitle?: string;
  icon?: { name: AzureIconName; color: string };
  trail: Crumb[];
  /** Shows the favorite star for a service's browse page. */
  favoriteServiceId?: string;
  children: ReactNode;
  testid?: string;
}

/** A portal page: the breadcrumb, then the page header naming what is in
 *  focus and its type, then the working pane. */
export function Blade({ title, subtitle, icon, trail, favoriteServiceId, children, testid }: BladeProps) {
  usePageTitle(title);
  const { favorites } = usePreferences();
  const favorite = favoriteServiceId ? favorites.includes(favoriteServiceId) : false;
  return (
    <div className="az-blade" data-testid={testid}>
      <Breadcrumbs trail={trail} />
      <header className="az-blade-header">
        {icon ? <ServiceTile icon={icon.name} color={icon.color} size={36} /> : null}
        <div className="az-blade-titles">
          <div className="az-blade-title-row">
            <Text as="h1" size={600} weight="semibold" className="az-blade-title">
              {title}
            </Text>
            {favoriteServiceId ? (
              <Tooltip content={favorite ? "Remove from favorites" : "Add to favorites"} relationship="label">
                <Button
                  appearance="transparent"
                  size="small"
                  aria-pressed={favorite}
                  icon={<AzureIcon name={favorite ? "star_filled" : "star"} />}
                  onClick={() => toggleFavorite(favoriteServiceId)}
                />
              </Tooltip>
            ) : null}
          </div>
          {subtitle ? (
            <Text size={200} className="az-blade-subtitle">
              {subtitle}
            </Text>
          ) : null}
        </div>
      </header>
      <div className="az-blade-body">{children}</div>
    </div>
  );
}

export interface PortalCommand {
  label: string;
  icon: AzureIconName;
  onSelect?: () => void;
  disabled?: boolean;
  testid?: string;
  /** Starts a new group, drawn after a divider. */
  divider?: boolean;
}

export function CommandBar({ commands, label = "Commands" }: { commands: PortalCommand[]; label?: string }) {
  return (
    <Toolbar aria-label={label} className="az-command-bar" size="small">
      {commands.map((command) => (
        <Fragment key={command.label}>
          {command.divider ? <ToolbarDivider /> : null}
          <ToolbarButton
            appearance="subtle"
            icon={<AzureIcon name={command.icon} size={16} />}
            onClick={command.onSelect}
            disabled={command.disabled}
            data-testid={command.testid}
          >
            {command.label}
          </ToolbarButton>
        </Fragment>
      ))}
    </Toolbar>
  );
}

export interface MenuItem {
  label: string;
  to: string;
  icon: AzureIconName;
}

export interface MenuGroup {
  label: string;
  items: MenuItem[];
}

/** The service menu beside a resource: the items every resource has, then
 *  collapsible groups, with its own search box. */
export function ServiceMenu({ label, top, groups }: { label: string; top: MenuItem[]; groups: MenuGroup[] }) {
  const { pathname } = useLocation();
  const [filter, setFilter] = useState("");
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const needle = filter.trim().toLowerCase();
  const matches = (item: MenuItem) => !needle || item.label.toLowerCase().includes(needle);
  const link = (item: MenuItem) => {
    const active = pathname === item.to;
    return (
      <li key={item.to}>
        <RouterLink to={item.to} className={active ? "az-menu-link az-menu-link-active" : "az-menu-link"} aria-current={active ? "page" : undefined}>
          <AzureIcon name={item.icon} size={16} />
          <span>{item.label}</span>
        </RouterLink>
      </li>
    );
  };
  return (
    <nav aria-label={label} className="az-service-menu">
      <Input
        className="az-service-menu-search"
        size="small"
        type="search"
        value={filter}
        aria-label="Search the service menu"
        placeholder="Search"
        contentBefore={<AzureIcon name="search" size={14} />}
        onChange={(_, data) => setFilter(data.value)}
      />
      <ul className="az-menu-list">{top.filter(matches).map(link)}</ul>
      {groups.map((group) => {
        const items = group.items.filter(matches);
        if (items.length === 0) return null;
        const open = needle !== "" || !collapsed.includes(group.label);
        return (
          <div key={group.label} className="az-menu-group">
            <button
              type="button"
              className="az-menu-group-toggle"
              aria-expanded={open}
              onClick={() => setCollapsed((current) => (open ? [...current, group.label] : current.filter((entry) => entry !== group.label)))}
            >
              <AzureIcon name={open ? "chevron_down" : "chevron_right"} size={12} />
              {group.label}
            </button>
            {open ? <ul className="az-menu-list">{items.map(link)}</ul> : null}
          </div>
        );
      })}
    </nav>
  );
}

export interface EssentialsProperty {
  label: string;
  value: ReactNode;
}

/** The Essentials panel that leads a resource's Overview: its properties in
 *  two columns, collapsible, with JSON View beside it. */
export function Essentials({
  left,
  right,
  onJsonView,
}: {
  left: EssentialsProperty[];
  right: EssentialsProperty[];
  onJsonView?: () => void;
}) {
  const [open, setOpen] = useState(true);
  const column = (properties: EssentialsProperty[]) => (
    <dl className="az-essentials-column">
      {properties.map((property) => (
        <div className="az-essentials-pair" key={property.label}>
          <dt>{property.label}</dt>
          <dd>
            <span aria-hidden>: </span>
            {property.value === "" || property.value == null ? "—" : property.value}
          </dd>
        </div>
      ))}
    </dl>
  );
  return (
    <section className="az-essentials" aria-label="Essentials">
      <div className="az-essentials-head">
        <button type="button" className="az-essentials-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
          <AzureIcon name={open ? "chevron_down" : "chevron_right"} size={12} />
          Essentials
        </button>
        {onJsonView ? (
          <Button appearance="transparent" size="small" onClick={onJsonView}>
            JSON View
          </Button>
        ) : null}
      </div>
      {open ? (
        <div className="az-essentials-grid">
          {column(left)}
          {column(right)}
        </div>
      ) : null}
    </section>
  );
}

export type StatusKind = "success" | "error" | "warning" | "inactive";

const KIND_TO_BADGE: Record<StatusKind, { color: BadgeProps["color"]; icon: AzureIconName }> = {
  success: { color: "success", icon: "status_success" },
  error: { color: "danger", icon: "status_error" },
  warning: { color: "warning", icon: "status_warning" },
  inactive: { color: "informative", icon: "status_inactive" },
};

export function statusKind(status: string): StatusKind {
  // Whole words: "unavailable" contains "available" and "inactive" contains
  // "active", and a tick beside a failed resource misleads.
  const words = status.toLowerCase().split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  if (has("unavailable", "stopped", "failed", "failure", "error", "deleted", "inactive", "canceled", "cancelled", "disabled", "deallocated"))
    return "error";
  if (has("pending", "provisioning", "creating", "updating", "deleting", "starting", "stopping", "accepted", "inprogress")) return "warning";
  if (has("running", "active", "available", "succeeded", "success", "ok", "healthy", "ready", "enabled")) return "success";
  return "inactive";
}

export function StatusBadge({ status, kind }: { status: string; kind?: StatusKind }) {
  const resolved = kind ?? statusKind(status);
  const { color, icon } = KIND_TO_BADGE[resolved];
  return (
    <Badge appearance="tint" color={color} className={`az-status az-status-${resolved}`} icon={<AzureIcon name={icon} size={14} />}>
      {status}
    </Badge>
  );
}

export function ErrorBar({ title, children, testid }: { title: string; children?: ReactNode; testid?: string }) {
  return (
    <div role="alert" data-testid={testid} className="az-message">
      <MessageBar intent="error">
        <MessageBarBody>
          <MessageBarTitle>{title}</MessageBarTitle>
          {children}
        </MessageBarBody>
      </MessageBar>
    </div>
  );
}

export function InfoBar({
  intent = "info",
  title,
  children,
  testid,
}: {
  intent?: "info" | "warning" | "success";
  title?: string;
  children: ReactNode;
  testid?: string;
}) {
  return (
    <div role="status" data-testid={testid} className="az-message">
      <MessageBar intent={intent}>
        <MessageBarBody>
          {title ? <MessageBarTitle>{title}</MessageBarTitle> : null}
          {children}
        </MessageBarBody>
      </MessageBar>
    </div>
  );
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Azure Resource Manager did not respond.";
}

export function EmptyState({
  title,
  description,
  loading,
  action,
}: {
  title: string;
  description?: string;
  loading?: boolean;
  action?: ReactNode;
}) {
  const body = (
    <div className="az-empty">
      {loading ? <Spinner size="tiny" aria-label={title} /> : null}
      <Text as="p" weight="semibold" block>
        {title}
      </Text>
      {description ? (
        <Text as="p" block className="az-empty-description">
          {description}
        </Text>
      ) : null}
      {action}
    </div>
  );
  return loading ? <div role="status">{body}</div> : body;
}

/**
 * The delete confirmation. With `confirmText` the operator types it before
 * Delete enables, as the portal asks for a resource group's name or for
 * "delete" before a bulk delete.
 */
export function ConfirmDialog({
  open,
  title,
  confirmLabel = "Delete",
  busyLabel = "Deleting…",
  busy,
  testid,
  error,
  confirmText,
  onConfirm,
  onCancel,
  children,
}: {
  open: boolean;
  title: string;
  confirmLabel?: string;
  busyLabel?: string;
  busy: boolean;
  testid: string;
  error?: string;
  confirmText?: string;
  onConfirm: () => void;
  onCancel: () => void;
  children: ReactNode;
}) {
  const [typed, setTyped] = useState("");
  useEffect(() => {
    if (!open) setTyped("");
  }, [open]);
  const confirmed = !confirmText || typed === confirmText;
  return (
    <Dialog
      open={open}
      onOpenChange={(event, data) => {
        if (!data.open && busy) {
          event.preventDefault();
          return;
        }
        if (!data.open) onCancel();
      }}
    >
      <DialogSurface data-testid={testid}>
        <DialogBody>
          <DialogTitle>{title}</DialogTitle>
          <DialogContent className="az-dialog-content">
            {children}
            {confirmText ? (
              <Field label={`Enter "${confirmText}" to confirm deletion`} required>
                <Input value={typed} onChange={(_, data) => setTyped(data.value)} data-testid={`${testid}-confirm-text`} />
              </Field>
            ) : null}
            {error ? <ErrorBar title="The delete failed." testid={`${testid}-error`}>{error}</ErrorBar> : null}
          </DialogContent>
          <DialogActions>
            <Button appearance="primary" data-testid={`${testid}-confirm`} disabled={busy || !confirmed} onClick={onConfirm}>
              {busy ? busyLabel : confirmLabel}
            </Button>
            <Button data-testid={`${testid}-cancel`} onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

/** Formats an ISO timestamp the way the portal's grids show times. */
export function formatTime(value: string | number | undefined): string {
  if (value === undefined || value === "") return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString(undefined, { dateStyle: "short", timeStyle: "medium" });
}
