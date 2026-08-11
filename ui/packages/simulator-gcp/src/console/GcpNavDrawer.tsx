import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { Link, NavLink } from "react-router";
import { Icon } from "./icons.js";
import { CATALOG } from "./catalog.js";

// NotSupportedChip is the reusable "not supported" affordance: a Material
// filled-tonal chip whose meaning is carried by its own text and aria-label,
// never by colour alone (WCAG 2.1 SC 1.4.1). Placed inside a link whose
// visible text already names the product, the two concatenate into an
// accessible name like "Compute Engine Not supported in this simulator".
export function NotSupportedChip() {
  return (
    <span className="gc-chip gc-chip-unsupported" aria-label="Not supported in this simulator">
      Not supported
    </span>
  );
}

// The real console's hamburger opens a navigation drawer listing the full
// Google Cloud product catalog, grouped into the console's own categories.
// This is that drawer: every product Google Cloud offers, the ones the
// simulator implements linking straight to their page, the rest carrying the
// NotSupportedChip and routing to a short honest explanation instead of being
// silently omitted or faked.
export function GcpNavDrawer({ open, onClose, triggerRef }: {
  open: boolean;
  onClose: () => void;
  triggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const [filter, setFilter] = useState("");

  // The real console's product menu filters the catalog live as the operator
  // types, matching the AWS console's services flyout. Groups with no match
  // drop out; an empty result says so rather than showing a blank panel.
  const groups = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    if (!needle) return CATALOG;
    return CATALOG.map((group) => ({
      ...group,
      items: group.items.filter((item) => item.name.toLowerCase().includes(needle)),
    })).filter((group) => group.items.length > 0);
  }, [filter]);

  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      triggerRef.current?.focus();
    };
    // Deliberately keyed on `open` alone: `onClose` is `() => setNavOpen(false)`,
    // whose body only calls a `useState` setter (identity-stable across
    // renders), and `triggerRef` is a ref object (identity-stable by
    // definition), so re-running this effect whenever the caller re-renders
    // would re-focus the close button and steal focus from wherever the
    // operator tabbed to inside the drawer.
  }, [open]);

  if (!open) return null;

  return (
    <div
      className="gc-drawer-overlay"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className="gc-drawer"
        role="dialog"
        aria-modal="true"
        aria-label="Google Cloud products"
        data-testid="nav-drawer"
      >
        <div className="gc-drawer-header">
          <span className="gc-drawer-title">Google Cloud products</span>
          <button
            ref={closeRef}
            type="button"
            className="gc-icon-button"
            aria-label="Close navigation"
            onClick={onClose}
          >
            <Icon name="chevron_left" />
          </button>
        </div>
        <p className="gc-drawer-note">
          This simulator implements a slice of Google Cloud. Supported products open their page; the
          rest are marked <NotSupportedChip />.
        </p>
        <div className="gc-drawer-search">
          <Icon name="search" size="1.25em" style={{ color: "var(--gc-fg-secondary)" }} />
          <input
            type="search"
            aria-label="Search products"
            placeholder="Search products"
            data-testid="nav-drawer-search"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          />
        </div>
        {groups.length === 0 ? (
          <p className="gc-drawer-note" data-testid="nav-drawer-empty">No products match “{filter}”.</p>
        ) : null}
        {groups.map((group) => (
          <div className="gc-drawer-group" key={group.label}>
            <h3 className="gc-drawer-group-label" id={`drawer-group-${group.label}`}>{group.label}</h3>
            <ul aria-labelledby={`drawer-group-${group.label}`}>
              {group.items.map((item) =>
                item.to ? (
                  <li key={item.id}>
                    <NavLink
                      to={item.to}
                      className={({ isActive }) =>
                        isActive ? "gc-drawer-link gc-drawer-link-active" : "gc-drawer-link"
                      }
                      onClick={onClose}
                      data-testid={`drawer-item-${item.id}`}
                    >
                      <Icon name={item.icon} size="1.25em" />
                      <span>{item.name}</span>
                    </NavLink>
                  </li>
                ) : (
                  <li key={item.id}>
                    <Link
                      to={`/ui/not-supported/${item.id}`}
                      className="gc-drawer-link gc-drawer-link-unsupported"
                      onClick={onClose}
                      data-testid={`drawer-item-${item.id}`}
                      aria-label={`${item.name}, not supported in this simulator`}
                    >
                      <Icon name={item.icon} size="1.25em" />
                      <span>{item.name}</span>
                      <NotSupportedChip />
                    </Link>
                  </li>
                ),
              )}
            </ul>
          </div>
        ))}
      </div>
    </div>
  );
}
