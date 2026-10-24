import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Icon, type IconName } from "./icons.js";
import { useProjectPath } from "./project.js";

export interface GcpPageAction {
  label: string;
  icon?: IconName;
  onSelect?: () => void;
  /** A console path the action opens, as the real console's create actions open a create page. */
  to?: string;
  disabled?: boolean;
  primary?: boolean;
  testId?: string;
}

function ActionButton({ action }: { action: GcpPageAction }) {
  const path = useProjectPath();
  const className = action.primary ? "gc-action gc-action-primary" : "gc-action";
  const content = (
    <>
      {action.icon ? <Icon name={action.icon} size="1.25em" /> : null}
      {action.label}
    </>
  );
  if (action.to && !action.disabled) {
    return (
      <Link className={className} to={path(action.to)} data-testid={action.testId}>
        {content}
      </Link>
    );
  }
  return (
    <button
      type="button"
      className={className}
      data-testid={action.testId}
      onClick={action.onSelect}
      disabled={action.disabled}
    >
      {content}
    </button>
  );
}

/** A back arrow to the page the console came from: the resource list for a detail or create page. */
export function BackButton({ to, label = "Back" }: { to: string; label?: string }) {
  const path = useProjectPath();
  return (
    <Link className="gc-icon-button gc-back" to={path(to)} aria-label={label} title={label}>
      <Icon name="arrow_back" />
    </Link>
  );
}

/** The page title row the real console puts above every page: an optional back
 *  arrow, the title, then the page's actions as text buttons with an icon,
 *  Refresh among them. */
export function GcpPageHeader({
  title,
  description,
  actions,
  onRefresh,
  refreshing,
  backTo,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: GcpPageAction[];
  onRefresh?: () => void;
  refreshing?: boolean;
  backTo?: string;
  children?: ReactNode;
}) {
  return (
    <div className="gc-page-header">
      <div className="gc-page-header-row">
        {backTo ? <BackButton to={backTo} /> : null}
        <h1>{title}</h1>
        <div className="gc-page-actions">
          {actions?.map((action) => <ActionButton key={action.label} action={action} />)}
          {onRefresh ? (
            <button type="button" className="gc-action" onClick={onRefresh} disabled={refreshing}>
              <Icon name="refresh" size="1.25em" className={refreshing ? "gc-rotating" : undefined} />
              Refresh
            </button>
          ) : null}
        </div>
        {children ? <div className="gc-page-header-end">{children}</div> : null}
      </div>
      {description ? <p className="gc-page-description">{description}</p> : null}
    </div>
  );
}

export type GcpStatusKind = "success" | "error" | "warning" | "progress" | "inactive" | "info";

// Whole words, because "unavailable" contains "available" and "inactive"
// contains "active": a substring test would tick a failed resource.
export function statusKind(status: string): GcpStatusKind {
  const words = status.toLowerCase().split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  if (
    has("unavailable", "failed", "failure", "error", "unknown", "critical", "alert", "emergency", "unhealthy", "cancelled")
  ) {
    return "error";
  }
  if (has("warning", "suspended", "degraded")) return "warning";
  if (
    has(
      "pending",
      "provisioning",
      "creating",
      "deploying",
      "updating",
      "staging",
      "starting",
      "stopping",
      "deleting",
      "repairing",
      "queued",
      "working",
      "reconciling",
      "suspending",
      "maintenance",
    )
  ) {
    return "progress";
  }
  if (
    has("running", "active", "available", "succeeded", "success", "ok", "healthy", "ready", "enabled", "runnable", "done")
  ) {
    return "success";
  }
  return "inactive";
}

const STATUS_ICON: Record<Exclude<GcpStatusKind, "progress">, IconName> = {
  success: "check_circle_fill",
  error: "error_fill",
  warning: "warning_fill",
  inactive: "stop_circle_fill",
  info: "info",
};

/** A resource state the way the console shows it: the state's own icon beside its name. */
export function GcpStatus({ status, kind: explicitKind, label }: { status: string; kind?: GcpStatusKind; label?: string }) {
  const kind = explicitKind ?? statusKind(status);
  return (
    <span className={`gc-status gc-status-${kind}`}>
      {kind === "progress" ? (
        <span className="gc-spinner gc-spinner-small" aria-hidden />
      ) : (
        <Icon name={STATUS_ICON[kind]} size="1.15em" />
      )}
      <span className="gc-status-text">{label ?? status}</span>
    </span>
  );
}

/** The copy-to-clipboard button the console puts beside resource names and IDs. */
export function CopyButton({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);
  const name = label ?? `Copy ${value} to clipboard`;
  return (
    <button
      type="button"
      className="gc-copy"
      aria-label={name}
      title={copied ? "Copied" : "Copy to clipboard"}
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => setCopied(true));
      }}
    >
      <Icon name={copied ? "check" : "content_copy"} size="1.1em" />
    </button>
  );
}

/** A value with the copy button beside it. */
export function Copyable({ value, children }: { value: string; children?: ReactNode }) {
  return (
    <span className="gc-copyable">
      <span>{children ?? value}</span>
      <CopyButton value={value} />
    </span>
  );
}
