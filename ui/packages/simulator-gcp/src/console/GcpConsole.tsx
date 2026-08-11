import { useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink } from "react-router";
import { useTheme } from "@sockerless/ui-core/hooks";
import { Icon, type IconName } from "./icons.js";
import { GcpNavDrawer } from "./GcpNavDrawer.js";

export interface GcpNavItem {
  label: string;
  to: string;
  icon: IconName;
}

// The console's own theme control resolves the same way the real console's
// Light/Dark/Same-as-device setting does: an explicit prior choice
// (localStorage) wins, otherwise the OS's prefers-color-scheme is honoured,
// and only absent both does the console fall back to its own default — light,
// matching Google Cloud's light-first shell (see useTheme's resolution
// order). The toggle flips between the two explicit choices; there is no
// third "system" state to switch back to once chosen, matching the AWS and
// Azure consoles' own toggles.
function GcpThemeToggle() {
  const { theme, toggle } = useTheme("light");
  const dark = theme === "dark";
  const label = dark ? "Switch to light theme" : "Switch to dark theme";
  return (
    <button type="button" className="gc-icon-button" onClick={toggle} aria-label={label} title={label}>
      <Icon name={dark ? "light_mode" : "dark_mode"} />
    </button>
  );
}

export function GcpHeader({ picker, account }: { picker: ReactNode; account: ReactNode }) {
  const [navOpen, setNavOpen] = useState(false);
  const menuRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  // The real console's own "/" shortcut: pressing it anywhere outside a text
  // field jumps focus to the search box, exactly the affordance the
  // placeholder text names. Suppressed while a field or an editable element
  // already has focus, so it never steals a literal "/" a user is typing.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      const typing =
        target instanceof HTMLElement &&
        (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable);
      if (typing) return;
      event.preventDefault();
      searchRef.current?.focus();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  return (
    <header className="gc-header">
      <div className="gc-header-left">
        <button
          ref={menuRef}
          type="button"
          className="gc-icon-button"
          aria-label="Main menu"
          aria-haspopup="dialog"
          aria-expanded={navOpen}
          onClick={() => setNavOpen(true)}
        >
          <Icon name="menu" />
        </button>
        <GcpNavDrawer open={navOpen} onClose={() => setNavOpen(false)} triggerRef={menuRef} />
        <span className="gc-wordmark">Google Cloud</span>
        {picker}
      </div>
      <div className="gc-header-search">
        <Icon name="search" size="1.25em" style={{ color: "var(--gc-fg-secondary)" }} />
        <input
          ref={searchRef}
          type="search"
          aria-label="Search resources, docs, products, and more"
          placeholder="Search (/) for resources, docs, products, and more"
        />
      </div>
      <div className="gc-header-right">
        <button type="button" className="gc-icon-button" aria-label="Gemini">
          <Icon name="auto_awesome" />
        </button>
        <button type="button" className="gc-icon-button" aria-label="Activate Cloud Shell">
          <Icon name="terminal" />
        </button>
        <button type="button" className="gc-icon-button" aria-label="Notifications">
          <Icon name="notifications" />
        </button>
        <button type="button" className="gc-icon-button" aria-label="Support">
          <Icon name="help" />
        </button>
        <button type="button" className="gc-icon-button" aria-label="Settings and utilities">
          <Icon name="more_vert" />
        </button>
        <GcpThemeToggle />
        {account}
      </div>
    </header>
  );
}

/** The console leads a product with the product name and icon, then a flat
 *  navigation whose active item is a filled pill anchored to the left edge. */
export function GcpProductNav({ product, items }: { product: string; items: GcpNavItem[] }) {
  return (
    <nav aria-label="Product" className="gc-product-nav">
      <div className="gc-product-title">
        <Icon name="deployed_code" size="1.5em" style={{ color: "var(--gc-primary)" }} />
        {product}
      </div>
      <ul>
        {items.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.to === "/ui/"}
              className={({ isActive }) => (isActive ? "gc-nav-link gc-nav-link-active" : "gc-nav-link")}
            >
              <Icon name={item.icon} size="1.25em" />
              <span>{item.label}</span>
            </NavLink>
          </li>
        ))}
      </ul>
      <div className="gc-nav-footer">
        <a href="#" className="gc-nav-link" onClick={(event) => event.preventDefault()}>
          <Icon name="list_alt" size="1.25em" />
          <span>Release Notes</span>
        </a>
      </div>
    </nav>
  );
}

export interface GcpPageAction {
  label: string;
  icon?: IconName;
  onSelect?: () => void;
  disabled?: boolean;
  primary?: boolean;
  /** Distinguishes the header action from an empty-state primary button that
   *  carries the same label — both can be on screen at once (an empty list
   *  renders its own "Create …" button alongside this one). */
  testId?: string;
}

/** The console puts inline text actions beside the page title rather than in a
 *  button group, describes the resource in a sentence beneath, and pins a
 *  refresh at the right. */
export function GcpPageHeader({
  title,
  description,
  actions,
  onRefresh,
  refreshing,
}: {
  title: string;
  description?: string;
  actions?: GcpPageAction[];
  onRefresh?: () => void;
  refreshing?: boolean;
}) {
  return (
    <div className="gc-page-header">
      <div className="gc-page-header-row">
        <h1>{title}</h1>
        <div className="gc-page-actions">
          {actions?.map((action) => (
            <button
              key={action.label}
              type="button"
              className={action.primary ? "gc-text-action gc-text-action-primary" : "gc-text-action"}
              data-testid={action.testId}
              onClick={action.onSelect}
              disabled={action.disabled}
            >
              {action.icon ? <Icon name={action.icon} size="1.25em" /> : null}
              {action.label}
            </button>
          ))}
        </div>
        {onRefresh ? (
          <button type="button" className="gc-refresh" onClick={onRefresh} disabled={refreshing}>
            <Icon name="refresh" size="1.25em" />
            {refreshing ? "Refreshing" : "Refresh"}
          </button>
        ) : null}
      </div>
      {description ? <p className="gc-page-description">{description}</p> : null}
    </div>
  );
}

export type GcpStatusKind = "success" | "error" | "warning" | "inactive";

export function GcpStatus({ status, kind: explicitKind }: { status: string; kind?: GcpStatusKind }) {
  // Matched on whole words. A substring test reports success for failure
  // states, because "unavailable" contains "available" and "inactive" contains
  // "active", and a tick beside a failed resource stops an operator looking any
  // further.
  const words = status.toLowerCase().split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  // The failure and warning sets cover both resource states and Cloud Logging
  // severities, so a log entry's severity reads the same way a resource's does.
  const derived: GcpStatusKind = has(
    "unavailable",
    "stopped",
    "failed",
    "failure",
    "error",
    "deleting",
    "inactive",
    "unknown",
    "critical",
    "alert",
    "emergency",
  )
    ? "error"
    : has("pending", "provisioning", "creating", "deploying", "updating", "warning")
      ? "warning"
      : has("running", "active", "available", "succeeded", "success", "ok", "healthy", "ready", "enabled")
        ? "success"
        : "inactive";
  const kind = explicitKind ?? derived;
  const glyph = kind === "success" ? "✔" : kind === "error" ? "✖" : kind === "warning" ? "⧗" : "•";
  return (
    <span className={`gc-status gc-status-${kind}`}>
      <span aria-hidden>{glyph}</span>
      {status}
    </span>
  );
}
