import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { useTheme } from "@sockerless/ui-core/hooks";
import { applyMode, Mode } from "@cloudscape-design/global-styles";
import Button, { type ButtonProps } from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Badge from "@cloudscape-design/components/badge";
import StatusIndicator, { type StatusIndicatorProps } from "@cloudscape-design/components/status-indicator";
import Modal, { type ModalProps } from "@cloudscape-design/components/modal";
import CopyToClipboard from "@cloudscape-design/components/copy-to-clipboard";
import KeyValuePairs, { type KeyValuePairsProps } from "@cloudscape-design/components/key-value-pairs";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Spinner from "@cloudscape-design/components/spinner";
import TopNavigation from "@cloudscape-design/components/top-navigation";
import BreadcrumbGroup from "@cloudscape-design/components/breadcrumb-group";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Input, { type InputProps } from "@cloudscape-design/components/input";
import Link from "@cloudscape-design/components/link";
import { filterNavGroups, type NavGroup } from "./serviceCatalog.js";

/**
 * The console shell and shared building blocks — all real
 * `@cloudscape-design/components`, the same design system the real AWS
 * Management Console is built on. This module wires them into the shapes
 * this simulator's pages already consume (`AwsButton`, `AwsModal`,
 * `AwsPageHeader`, …) so every page renders through genuine Cloudscape
 * components without each page needing to know the library's own prop
 * shapes.
 */

export type { NavGroup, NavService } from "./serviceCatalog.js";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * The console's theme control lives in the header. Cloudscape ships its own
 * light/dark modes (`applyMode`) with WCAG AA contrast in both — this toggle
 * drives that, on top of the shared `useTheme` hook every simulator UI uses
 * (which also flips the `.dark` class the shared, non-Cloudscape chrome
 * still reads). The glyph is decorative; the accessible name states the
 * action, and it changes with the state so a screen reader announces what
 * the press will do.
 */
function useAwsTheme() {
  const { theme, toggle } = useTheme();
  useEffect(() => {
    applyMode(theme === "dark" ? Mode.Dark : Mode.Light);
  }, [theme]);
  return { theme, toggle };
}

/**
 * The "Services" mega-menu: the real AWS console's global, from-anywhere way
 * to reach any service, distinct from the side navigation beside it (which
 * this simulator keeps as the current-section affordance). Cloudscape's
 * `TopNavigation` has no built-in multi-column, searchable flyout, so this
 * disclosure is assembled from Cloudscape primitives (`Input`, `Link`,
 * `Badge`) with the same focus-trap contract every dialog in this console
 * holds — the panel is a labelled dialog docked to the header's own bottom
 * edge and spanning its full width, the way the real console's flyout does,
 * reusing the exact NAV_GROUPS catalog the side navigation renders from
 * rather than a second copy of it.
 */
export function AwsServicesMenu({ groups }: { groups: NavGroup[] }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const panelId = useId();
  const titleId = useId();
  const buttonRef = useRef<ButtonProps.Ref>(null);
  const triggerWrapperRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<InputProps.Ref>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.stopPropagation();
        setOpen(false);
        buttonRef.current?.focus();
        return;
      }
      if (event.key !== "Tab") return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusables = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
      if (focusables.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Node;
      if (panelRef.current?.contains(target) || triggerWrapperRef.current?.contains(target)) return;
      setOpen(false);
    }
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("mousedown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("mousedown", onPointerDown);
    };
  }, [open]);

  // A fresh search every time the menu opens, rather than remembering the
  // last query, matches the real console's flyout and keeps the full catalog
  // as the first thing an operator who re-opens it sees.
  useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  const filtered = filterNavGroups(groups, query);

  function follow(to: string) {
    setOpen(false);
    navigate(to);
  }

  return (
    <div style={{ position: "relative" }}>
      <div ref={triggerWrapperRef}>
        <Button
          ref={buttonRef}
          variant="normal"
          iconName="angle-down"
          iconAlign="right"
          ariaHaspopup="dialog"
          ariaExpanded={open}
          ariaControls={open ? panelId : undefined}
          onClick={() => setOpen((current) => !current)}
        >
          Services
        </Button>
      </div>
      {open && (
        <div
          ref={panelRef}
          id={panelId}
          role="dialog"
          aria-labelledby={titleId}
          tabIndex={-1}
          style={{
            position: "fixed",
            insetInlineStart: 0,
            insetInlineEnd: 0,
            top: 40,
            zIndex: 1000,
            maxHeight: "calc(100vh - 80px)",
            overflowY: "auto",
            // Only layout (position/size/scroll) — the panel's own surface
            // (background, border, shadow) comes from the real Cloudscape
            // `Container` inside, which themes itself correctly in both
            // modes. Cloudscape's own colour tokens are exposed as
            // build-hashed custom properties private to the installed
            // package version, so this wrapper cannot reference them
            // directly (see tokens.css) — reaching for a real Container
            // avoids needing to.
          }}
        >
          <Container disableContentPaddings>
            <Box padding={{ horizontal: "xl", vertical: "l" }}>
              <Box variant="h2" fontSize="heading-m" id={titleId} margin={{ bottom: "m" }}>
                All services
              </Box>
              <Box margin={{ bottom: "l" }} className="aws-services-search">
                <Input
                  ref={searchRef}
                  type="search"
                  ariaLabel="Search services"
                  placeholder="Find services"
                  value={query}
                  onChange={(event) => setQuery(event.detail.value)}
                />
              </Box>
              {filtered.length === 0 ? (
                <Box color="text-body-secondary">No services match &quot;{query.trim()}&quot;.</Box>
              ) : (
              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
                  gap: "20px 24px",
                }}
              >
                {filtered.map((group) => (
                  <div key={group.label}>
                    <Box variant="h3" fontSize="body-s" fontWeight="bold" color="text-body-secondary" margin={{ bottom: "xs" }}>
                      {group.label}
                    </Box>
                    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                      {group.items.map((item) => {
                        const supported = item.supported !== false;
                        return (
                          <li key={item.to} style={{ padding: "4px 0" }}>
                            <Link
                              href={item.to}
                              ariaLabel={supported ? undefined : `${item.label}, not supported in this simulator`}
                              variant={supported ? undefined : "secondary"}
                              onFollow={(event) => {
                                event.preventDefault();
                                follow(item.to);
                              }}
                            >
                              {item.label}
                            </Link>
                            {!supported && (
                              <span style={{ marginInlineStart: 8 }}>
                                <Badge color="grey" nativeAttributes={{ "aria-hidden": true }}>
                                  Not supported
                                </Badge>
                              </span>
                            )}
                          </li>
                        );
                      })}
                    </ul>
                  </div>
                ))}
              </div>
              )}
            </Box>
          </Container>
        </div>
      )}
    </div>
  );
}

export function AwsHeader({
  region,
  account,
  services,
}: {
  region: string;
  account: ReactNode;
  services: NavGroup[];
}) {
  const navigate = useNavigate();
  const { theme, toggle } = useAwsTheme();
  const isDark = theme === "dark";
  return (
    <div className="aws-header">
      <div style={{ flex: "1 1 auto", minWidth: 0 }}>
        <TopNavigation
          identity={{
            href: "/ui/",
            title: "Simulator Console",
            onFollow: (event) => {
              event.preventDefault();
              navigate("/ui/");
            },
          }}
          search={
            <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
              <AwsServicesMenu groups={services} />
              <div style={{ flex: "1 1 auto", minWidth: 0, maxWidth: 640 }}>
                <Input type="search" ariaLabel="Search" placeholder="Search [Option+S]" value="" readOnly />
              </div>
            </div>
          }
          utilities={[
            { type: "button", iconName: "notification", ariaLabel: "Notifications" },
            { type: "button", iconName: "settings", ariaLabel: "Settings" },
            { type: "button", iconName: "support", ariaLabel: "Support" },
          ]}
          i18nStrings={{
            searchIconAriaLabel: "Search",
            searchDismissIconAriaLabel: "Close search",
            overflowMenuDismissIconAriaLabel: "Close menu",
            overflowMenuBackIconAriaLabel: "Back",
            overflowMenuTriggerText: "More",
            overflowMenuTitleText: "All",
          }}
        />
      </div>
      {/* TopNavigation renders its own real `<header>` (the banner
       * landmark), scoped to its own DOM — this cluster sits beside it, not
       * inside it, so it needs a landmark of its own rather than being
       * orphaned content (axe: region). `role="region"` rather than a
       * second banner: two banner landmarks on one page is its own
       * violation (landmark-no-duplicate-banner). */}
      <div className="aws-header-account" role="region" aria-label="Account settings">
        <span className="aws-header-region">{region}</span>
        {account}
        <Button
          variant="icon"
          iconName="light-dark"
          onClick={() => toggle()}
          ariaLabel={isDark ? "Switch to light theme" : "Switch to dark theme"}
        />
      </div>
    </div>
  );
}

export function AwsBreadcrumbs({ trail }: { trail: { label: string; to?: string }[] }) {
  const navigate = useNavigate();
  return (
    <BreadcrumbGroup
      ariaLabel="Breadcrumbs"
      items={trail.map((crumb) => ({ text: crumb.label, href: crumb.to ?? "#" }))}
      onFollow={(event) => {
        event.preventDefault();
        const href = event.detail.href;
        if (href && href !== "#") navigate(href);
      }}
    />
  );
}

/**
 * The service navigation beside the working area, built from Cloudscape's
 * `Link` and `Badge` primitives rather than the packaged `SideNavigation`
 * composite. `SideNavigation`'s own declarative `items` API renders an
 * item's `info` badge as a DOM sibling of the link, not inside it — real
 * Cloudscape's own convention for a "New" badge — which also means it has no
 * way to give one link a distinct accessible name from its visible text.
 * That is fine for a decorative "New" tag; it is not fine for "not
 * supported", which this console states in the accessible name itself
 * (`Link.ariaLabel`, which `SideNavigation.Link` does not expose), so this
 * nav is assembled from `Link` directly, keeping the same reading-order
 * badge alongside it.
 */
export function AwsSideNavigation({
  groups,
  activeHref,
}: {
  groups: NavGroup[];
  activeHref: string;
}) {
  const navigate = useNavigate();
  function follow(to: string) {
    navigate(to);
  }
  return (
    <Box padding={{ vertical: "m" }}>
      <Box padding={{ horizontal: "l", bottom: "m" }} fontWeight="bold" fontSize="heading-m">
        <Link
          href="/ui/"
          onFollow={(event) => {
            event.preventDefault();
            follow("/ui/");
          }}
        >
          Simulator
        </Link>
      </Box>
      <SpaceBetween size="l">
        {groups.map((group) => (
          <div key={group.label}>
            <Box padding={{ horizontal: "l", bottom: "xs" }} fontSize="body-s" fontWeight="bold" color="text-body-secondary">
              {group.label}
            </Box>
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {group.items.map((item) => {
                const supported = item.supported !== false;
                const isActive = item.to === activeHref;
                return (
                  <li key={item.to} style={{ padding: "2px 24px" }}>
                    <Link
                      href={item.to}
                      variant={supported ? undefined : "secondary"}
                      ariaLabel={supported ? undefined : `${item.label}, not supported in this simulator`}
                      nativeAttributes={isActive ? { "aria-current": "page" } : undefined}
                      onFollow={(event) => {
                        event.preventDefault();
                        follow(item.to);
                      }}
                    >
                      {isActive ? (
                        // Box always sets an explicit text colour (it does
                        // not simply inherit the Link's), which is exactly
                        // what the current-page item needs — plain body
                        // text instead of link-blue — but wrapping every
                        // item this way would flatten every other, genuinely
                        // clickable link to that same plain colour too.
                        <Box fontWeight="bold" color="text-label">
                          {item.label}
                        </Box>
                      ) : (
                        item.label
                      )}
                    </Link>
                    {!supported && (
                      <span style={{ marginInlineStart: 8 }}>
                        <Badge color="grey" nativeAttributes={{ "aria-hidden": true }}>
                          Not supported
                        </Badge>
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </SpaceBetween>
    </Box>
  );
}

/**
 * `Resources (count)` with actions — the pattern every Cloudscape list page
 * uses, whether as a table's own header or a stand-alone detail-page title.
 * Every page in this console renders exactly one top-level heading, so this
 * is also the one place that sets the browser tab's title — the real AWS
 * console updates it per page (an operator with a dozen console tabs open
 * depends on this to tell them apart), and a single console-wide "Simulator
 * Console" title would not.
 */
export function AwsPageHeader({
  title,
  count,
  description,
  actions,
}: {
  title: string;
  count?: number;
  description?: string;
  actions?: ReactNode;
}) {
  useEffect(() => {
    document.title = `${title} - Simulator Console`;
  }, [title]);
  return (
    <Header variant="h1" description={description} actions={actions} counter={count !== undefined ? `(${count})` : undefined}>
      {title}
    </Header>
  );
}

export function AwsButton({
  children,
  variant = "normal",
  onClick,
  disabled,
  "data-testid": testId,
}: {
  children: ReactNode;
  variant?: "normal" | "primary";
  onClick?: () => void;
  disabled?: boolean;
  "data-testid"?: string;
}) {
  return (
    <Button
      variant={variant}
      disabled={disabled}
      onClick={onClick ? () => onClick() : undefined}
      nativeButtonAttributes={testId ? { "data-testid": testId } : undefined}
    >
      {children}
    </Button>
  );
}

export function AwsContainer({ children }: { children: ReactNode }) {
  return <Container>{children}</Container>;
}

/**
 * A table cell that navigates to a resource's detail route — every list
 * page's row-header column. A real Cloudscape `Link`, routed through
 * react-router the same way the console's other navigation controls are.
 */
export function AwsRowLink({ to, children }: { to: string; children: ReactNode }) {
  const navigate = useNavigate();
  return (
    <Link
      href={to}
      onFollow={(event) => {
        event.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </Link>
  );
}

/**
 * The pill the navigation and the "not supported" page use for a service
 * sockerless has not implemented. Used on its own, on the "not supported"
 * page itself, it carries its own accessible name naming the service; inside
 * the side navigation (`decorative`) the fact is instead conveyed by reading
 * order (see `AwsSideNavigation`), so this copy is hidden from assistive
 * tech to avoid double-announcing it.
 */
export function AwsNotSupportedBadge({ serviceName, decorative = false }: { serviceName: string; decorative?: boolean }) {
  return (
    <Badge
      color="grey"
      nativeAttributes={
        decorative ? { "aria-hidden": true } : { "aria-label": `${serviceName} is not supported in this simulator` }
      }
    >
      Not supported
    </Badge>
  );
}

/**
 * A Cloudscape `Modal`: it already traps focus, closes on Escape, on an
 * overlay click, and on its own close control — the same contract the real
 * console's "Access key created" dialog holds (an operator can dismiss it
 * early and lose the one-time secret, exactly as the real console allows),
 * so every caller wires the same handler to `onDismiss` as to any Cancel/Done
 * footer action. Cloudscape's own focus lock moves focus to the first
 * focusable element in the dialog on open — structurally, the dialog's own
 * close control, ahead of any form field in the body — and returns it to
 * whatever opened the dialog on close; this is Cloudscape's real behaviour,
 * not something a caller can retarget to a specific field.
 */
export function AwsModal({
  title,
  onDismiss,
  footer,
  size,
  children,
}: {
  title: string;
  onDismiss: () => void;
  footer: ReactNode;
  size?: ModalProps.Size;
  children: ReactNode;
}) {
  return (
    <Modal visible header={title} footer={footer} size={size} onDismiss={onDismiss} closeAriaLabel="Close">
      {children}
    </Modal>
  );
}

/**
 * Copy-to-clipboard with spoken and visible feedback, the control Cloudscape
 * places beside every credential and identifier.
 */
export function AwsCopyButton({ value, label }: { value: string; label: string }) {
  return (
    <CopyToClipboard
      variant="icon"
      textToCopy={value}
      copyButtonAriaLabel={label}
      copySuccessText="Copied"
      copyErrorText="Copy failed"
    />
  );
}

export type AwsStatusKind = "success" | "error" | "warning" | "inactive";

const KIND_TO_INDICATOR: Record<AwsStatusKind, StatusIndicatorProps.Type> = {
  success: "success",
  error: "error",
  warning: "in-progress",
  inactive: "stopped",
};

/**
 * Where the caller knows the meaning it passes `kind`, because inferring it
 * from wording is guesswork that fails quietly. The inference below exists
 * only for values that arrive from the service as free text.
 */
export function AwsStatus({ status, kind: explicitKind }: { status: string; kind?: AwsStatusKind }) {
  const value = status.toLowerCase();
  // Failure terms are tested first and matched on whole words. Substring tests
  // are the trap here: "unavailable" contains "available", and "inactive"
  // contains "active", so a looser check reports a green tick for a failure.
  const words = value.split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  const kind: AwsStatusKind = has(
    "unavailable",
    "stopped",
    "stopping",
    "failed",
    "failure",
    "error",
    "deactivated",
    "inactive",
    "deleted",
  )
    ? "error"
    : has("pending", "provisioning", "creating", "updating", "deleting")
      ? "warning"
      : has("running", "active", "activated", "available", "succeeded", "success", "ok", "healthy")
        ? "success"
        : "inactive";
  const resolved = explicitKind ?? kind;
  return <StatusIndicator type={KIND_TO_INDICATOR[resolved]}>{status}</StatusIndicator>;
}

/**
 * A resource's key-value property block — Cloudscape's own `KeyValuePairs`,
 * replacing the hand-built `<dl>` every detail page used to draw. Callers
 * build a flat list of `{label, value}` pairs (skipping any conditional
 * field simply by not including it), the same shape the JSX `<dt>`/`<dd>`
 * pairs held before.
 */
export function AwsKeyValue({
  items,
  columns = 3,
  ariaLabel,
}: {
  items: { label: string; value: ReactNode }[];
  columns?: number;
  ariaLabel?: string;
}) {
  const pairs: KeyValuePairsProps.Item[] = items.map((item) => ({ type: "pair", label: item.label, value: item.value }));
  return <KeyValuePairs columns={columns} items={pairs} ariaLabel={ariaLabel} />;
}

/**
 * A load failure, rendered as a real Cloudscape `Alert`. The `role="alert"`
 * wrapper is this console's own addition on top of Cloudscape's own markup
 * (Cloudscape's Alert relies on its visual presentation and does not itself
 * assert an ARIA live-region role) so a screen reader announces a failed
 * read the moment it happens, matching this console's accessibility bar.
 */
export function AwsErrorAlert({
  children,
  testId,
}: {
  children: ReactNode;
  testId?: string;
}) {
  return (
    <div role="alert" data-testid={testId}>
      <Alert type="error">{children}</Alert>
    </div>
  );
}

/**
 * A resource's empty or loading placeholder, replacing the hand-built
 * `.aws-empty` block. `role="status"` while loading lets assistive tech pick
 * up the change without the operator having to go looking for it.
 */
export function AwsEmptyState({
  title,
  description,
  loading,
}: {
  title: string;
  description?: string;
  loading?: boolean;
}) {
  const body = (
    <Box textAlign="center" color="text-body-secondary" padding={{ vertical: "xxl" }}>
      {loading && (
        <Box margin={{ bottom: "s" }}>
          <Spinner size="normal" />
        </Box>
      )}
      <Box variant="strong" color="text-body-secondary" fontSize="heading-s">
        {title}
      </Box>
      {description && <Box margin={{ top: "xxs" }}>{description}</Box>}
    </Box>
  );
  return loading ? <div role="status">{body}</div> : body;
}
