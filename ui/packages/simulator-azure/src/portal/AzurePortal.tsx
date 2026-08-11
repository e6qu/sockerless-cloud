import { Fragment, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import {
  makeStyles,
  mergeClasses,
  shorthands,
  tokens,
  Toolbar,
  ToolbarButton,
  Button,
  Input,
  Popover,
  PopoverTrigger,
  PopoverSurface,
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbButton,
  BreadcrumbDivider,
  Badge,
  type BadgeProps,
  Accordion,
  AccordionItem,
  AccordionHeader,
  AccordionPanel,
  Link,
  Text,
  MessageBar,
  MessageBarBody,
  Spinner,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
} from "@fluentui/react-components";
import { AzureIcon, type AzureIconName } from "./icons.js";

/**
 * The portal shell and shared building blocks — all real
 * `@fluentui/react-components` (Fluent UI v9 / Fluent 2), the same design
 * system the real Azure portal is built on. This module wires them into the
 * shapes this simulator's pages already consume (`AzureCommandBar`,
 * `AzureEssentials`, `AzureStatus`, …) so every page renders through genuine
 * Fluent components without each page needing to know the library's own prop
 * shapes.
 */

export interface ServiceMenuItem {
  label: string;
  to: string;
  /** False marks a service the real Azure portal offers that this simulator
   *  does not implement. The item still links — to a small honest
   *  explanation — rather than disappearing or turning into a dead end that
   *  looks clickable but isn't; see AzureNotSupportedBadge. */
  supported?: boolean;
}

export interface ServiceMenuGroup {
  label: string;
  items: ServiceMenuItem[];
}

/** A command bar entry. The portal shows unavailable commands greyed rather
 *  than hidden, so the set of things a resource supports stays visible. */
export interface PortalCommand {
  label: string;
  icon: AzureIconName;
  onSelect?: () => void;
  disabled?: boolean;
  /** A structural-test hook rendered as data-testid on the command button. */
  testid?: string;
}

/** Shared Griffel styles for this module's own components and, via this
 *  export, for AzureApp.tsx's shell layout (`shell`/`body`/`main`) — one
 *  style sheet for the whole portal frame rather than a second `makeStyles`
 *  call duplicating the same layout tokens. */
export const useAzureStyles = makeStyles({
  shell: {
    minHeight: "100vh",
    width: "100%",
    display: "grid",
    gridTemplateRows: "auto auto 1fr",
    // A slightly darker "canvas" than FluentProvider's own root background
    // (colorNeutralBackground1, a card-white) — the same layered look the
    // real portal's grey working canvas beneath white cards/tables uses.
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground1,
    fontSize: tokens.fontSizeBase300,
  },

  // Global header ------------------------------------------------------
  header: {
    display: "flex",
    alignItems: "center",
    gap: "16px",
    padding: "4px 12px",
    minHeight: "44px",
    position: "relative",
    zIndex: 1,
    backgroundColor: tokens.colorBrandBackground,
    color: tokens.colorNeutralForegroundOnBrand,
  },
  headerBrand: { display: "flex", alignItems: "center", gap: "8px", flexShrink: 0 },
  wordmark: { color: tokens.colorNeutralForegroundOnBrand, fontSize: tokens.fontSizeBase300, whiteSpace: "nowrap" },
  headerSearch: { flex: 1, display: "flex", justifyContent: "center", minWidth: 0 },
  headerSearchInput: { width: "100%", maxWidth: "620px" },
  headerRight: { display: "flex", alignItems: "center", gap: "4px", flexShrink: 0, minWidth: 0 },
  onBrandButton: {
    color: tokens.colorNeutralForegroundOnBrand,
    ":hover": { backgroundColor: tokens.colorBrandBackgroundHover, color: tokens.colorNeutralForegroundOnBrand },
    ":hover:active": { color: tokens.colorNeutralForegroundOnBrand },
  },
  headerPopoverSurface: { maxWidth: "320px" },

  // Breadcrumbs ----------------------------------------------------------
  breadcrumbBar: {
    padding: "8px 16px",
    backgroundColor: tokens.colorNeutralBackground1,
    borderBottom: `1px solid ${tokens.colorNeutralStroke2}`,
  },

  // Working pane -----------------------------------------------------------
  body: {
    display: "grid",
    gridTemplateColumns: "260px minmax(0, 1fr)",
    alignItems: "start",
    backgroundColor: tokens.colorNeutralBackground1,
  },
  resourceTitle: {
    padding: "12px 20px 4px",
    gridColumn: "1 / -1",
    backgroundColor: tokens.colorNeutralBackground1,
  },
  resourceTitleMeta: { display: "flex", gap: "12px", marginTop: "2px" },
  resourceTitleDirectory: {
    ...shorthands.borderLeft("1px", "solid", tokens.colorNeutralStroke2),
    paddingInlineStart: "12px",
  },

  main: { padding: "16px 20px 48px", minWidth: 0 },

  // Service menu -------------------------------------------------------
  serviceMenu: {
    ...shorthands.borderRight("1px", "solid", tokens.colorNeutralStroke2),
    padding: "12px 0",
    minHeight: "calc(100vh - 160px)",
    backgroundColor: tokens.colorNeutralBackground1,
  },
  serviceSearch: { width: "calc(100% - 24px)", margin: "0 12px 8px" },
  serviceList: { listStyle: "none", margin: 0, padding: 0 },
  serviceRow: { display: "block" },
  serviceLink: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "8px",
    padding: "6px 12px 6px 20px",
    textDecorationLine: "none",
    ...shorthands.borderLeft("3px", "solid", "transparent"),
    width: "100%",
    boxSizing: "border-box",
  },
  serviceLinkActive: {
    backgroundColor: tokens.colorNeutralBackground1Selected,
    borderLeftColor: tokens.colorCompoundBrandStroke,
    fontWeight: tokens.fontWeightSemibold,
  },
  serviceLinkUnsupported: { color: tokens.colorNeutralForeground2 },
  serviceLabel: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" },
  serviceGroupPanel: { paddingInlineStart: "18px" },

  // Essentials -----------------------------------------------------------
  essentialsSection: {
    borderBottom: `1px solid ${tokens.colorNeutralStroke2}`,
    paddingBottom: "12px",
    marginBottom: "16px",
  },
  essentialsGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    gap: "4px 48px",
    margin: 0,
    maxWidth: "1200px",
  },
  essentialsPair: {
    display: "grid",
    gridTemplateColumns: "180px minmax(0, 1fr)",
    gap: "8px",
    alignItems: "baseline",
    fontSize: tokens.fontSizeBase200,
    lineHeight: tokens.lineHeightBase200,
    margin: 0,
  },
  essentialsLabel: { color: tokens.colorNeutralForeground2, fontWeight: tokens.fontWeightRegular, margin: 0 },
  essentialsValue: { overflowWrap: "anywhere", color: tokens.colorNeutralForeground1, margin: 0 },

  // States -----------------------------------------------------------------
  emptyState: { padding: "36px 12px", textAlign: "center", color: tokens.colorNeutralForeground2 },
  emptyTitle: { display: "block", color: tokens.colorNeutralForeground1, marginBottom: "4px" },
  emptySpinner: { marginBottom: "8px" },
});

/**
 * The header's own theme control. Fluent ships its own light/dark themes
 * (`webLightTheme`/`webDarkTheme`, applied to `FluentProvider` in
 * AzureApp.tsx) with WCAG AA contrast in both — this toggle drives the shared
 * `useTheme` hook every simulator UI uses (which also flips the `.dark` class
 * the rest of this portal's residual, non-Fluent chrome still reads).
 */
function AzureThemeToggle({ isDark, onToggle }: { isDark: boolean; onToggle: () => void }) {
  const styles = useAzureStyles();
  const label = isDark ? "Switch to light theme" : "Switch to dark theme";
  return (
    <Button
      appearance="transparent"
      className={styles.onBrandButton}
      data-testid="az-theme-toggle"
      icon={<AzureIcon name={isDark ? "sun" : "moon"} size={18} />}
      onClick={onToggle}
      aria-label={label}
      title={label}
    />
  );
}

/** A header icon button that discloses a small panel rather than acting
 *  directly — the shape every header control that has no real backing
 *  feature in this simulator takes (Cloud Shell, Notifications, Help). A real
 *  Fluent `Popover` with `trapFocus`: focus moves into the panel on open,
 *  Escape and an outside pointer close it and return focus to the trigger —
 *  Fluent's own behaviour for exactly this "rich content flyout, not a
 *  command list" shape, which is why this uses `Popover` rather than `Menu`
 *  (Fluent's `Menu`/`MenuList` is for action lists with arrow-key
 *  navigation; these panels are prose, the same reason the real Fluent
 *  pattern for a Teams/Outlook-style app-bar disclosure is `Popover`, not a
 *  `Menu`). */
function AzureHeaderPopover({
  icon,
  label,
  testid,
  children,
}: {
  icon: AzureIconName;
  label: string;
  testid: string;
  children: ReactNode;
}) {
  const styles = useAzureStyles();
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={(_, data) => setOpen(data.open)} trapFocus positioning="below-end">
      <PopoverTrigger disableButtonEnhancement>
        <Button
          appearance="transparent"
          className={styles.onBrandButton}
          data-testid={testid}
          icon={<AzureIcon name={icon} size={18} />}
          aria-label={label}
          title={label}
        />
      </PopoverTrigger>
      <PopoverSurface aria-label={label} role="dialog" className={mergeClasses("az-header-panel", styles.headerPopoverSurface)}>
        {children}
      </PopoverSurface>
    </Popover>
  );
}

export function AzureHeader({
  account,
  isDark,
  onToggleTheme,
}: {
  account: ReactNode;
  isDark: boolean;
  onToggleTheme: () => void;
}) {
  const styles = useAzureStyles();
  return (
    <header className={mergeClasses("az-header", styles.header)}>
      <div className={styles.headerBrand}>
        <Button
          appearance="transparent"
          className={styles.onBrandButton}
          icon={<AzureIcon name="menu" size={18} />}
          aria-label="Show portal menu"
        />
        <Text className={mergeClasses("az-wordmark", styles.wordmark)}>Microsoft Azure</Text>
      </div>
      <div className={mergeClasses("az-header-search", styles.headerSearch)}>
        <Input
          className={styles.headerSearchInput}
          type="search"
          contentBefore={<AzureIcon name="search" size={16} />}
          aria-label="Search resources, services, and docs"
          placeholder="Search resources, services, and docs (G+/)"
        />
      </div>
      <div className={styles.headerRight}>
        {/* The real Azure portal header carries Cloud Shell, Notifications,
         * and Help alongside the account and Settings controls. This
         * simulator has no Cloud Shell runtime, no operation queue to
         * notify from, and no support surface — each stays a real, focusable
         * control rather than disappearing, disclosing that honestly instead
         * of faking the feature. Settings is the theme toggle below. */}
        <AzureHeaderPopover icon="cloud_shell" label="Cloud Shell" testid="az-cloud-shell">
          <Text as="h2" weight="semibold" block>
            Cloud Shell
          </Text>
          <Text as="p" block>
            Azure Cloud Shell isn&rsquo;t available in this simulator. Use the <code>az</code> CLI installed locally,
            pointed at this deployment&rsquo;s endpoint, for an equivalent browser-free shell.
          </Text>
        </AzureHeaderPopover>
        <AzureHeaderPopover icon="notifications" label="Notifications" testid="az-notifications">
          <Text as="h2" weight="semibold" block>
            Notifications
          </Text>
          <Text as="p" block>
            This simulator doesn&rsquo;t emit portal notifications — there&rsquo;s no background operation queue to
            report on. Command results and errors surface inline on each blade instead.
          </Text>
        </AzureHeaderPopover>
        <AzureHeaderPopover icon="help" label="Help and support" testid="az-help">
          <Text as="h2" weight="semibold" block>
            Help + support
          </Text>
          <Text as="p" block>
            Help and support isn&rsquo;t available in this simulator. See the real{" "}
            <a href="https://learn.microsoft.com/azure/" target="_blank" rel="noreferrer">
              Azure documentation
            </a>{" "}
            for the services it simulates.
          </Text>
        </AzureHeaderPopover>
        {account}
        <AzureThemeToggle isDark={isDark} onToggle={onToggleTheme} />
      </div>
    </header>
  );
}

export function AzureBreadcrumbs({ trail }: { trail: { label: string; to?: string }[] }) {
  const styles = useAzureStyles();
  const navigate = useNavigate();
  return (
    <div className={mergeClasses("az-breadcrumbs", styles.breadcrumbBar)}>
      <Breadcrumb aria-label="Breadcrumbs">
        {trail.map((crumb, index) => (
          <Fragment key={crumb.label}>
            {index > 0 ? <BreadcrumbDivider /> : null}
            <BreadcrumbItem>
              {crumb.to && index < trail.length - 1 ? (
                <BreadcrumbButton
                  href={crumb.to}
                  onClick={(event) => {
                    event.preventDefault();
                    navigate(crumb.to!);
                  }}
                >
                  {crumb.label}
                </BreadcrumbButton>
              ) : (
                <BreadcrumbButton current>{crumb.label}</BreadcrumbButton>
              )}
            </BreadcrumbItem>
          </Fragment>
        ))}
      </Breadcrumb>
    </div>
  );
}

/** The portal titles a working pane with the resource, its type beneath, and
 *  the directory it belongs to — not with the service's name alone. It sits
 *  above the service menu and the main pane, spanning both, so it is its own
 *  landmark region — named from the same heading a sighted operator reads —
 *  rather than content orphaned outside every landmark on the page. */
export function AzureResourceTitle({ name, kind, directory }: { name: string; kind: string; directory: string }) {
  const styles = useAzureStyles();
  return (
    <div className={mergeClasses("az-resource-title", styles.resourceTitle)} role="region" aria-labelledby="az-resource-title-heading">
      <Text as="h1" id="az-resource-title-heading" size={500} weight="semibold" block>
        {name}
      </Text>
      <Text as="p" size={200} block className={styles.resourceTitleMeta}>
        <span>{kind}</span>
        <span className={styles.resourceTitleDirectory}>Directory: {directory}</span>
      </Text>
    </div>
  );
}

export function AzureCommandBar({ commands }: { commands: PortalCommand[] }) {
  return (
    <Toolbar aria-label="Commands" className="az-command-bar">
      {commands.map((command) => (
        <ToolbarButton
          key={command.label}
          className="az-command"
          icon={<AzureIcon name={command.icon} size={16} />}
          onClick={command.onSelect}
          disabled={command.disabled}
          {...(command.testid ? { "data-testid": command.testid } : {})}
        >
          {command.label}
        </ToolbarButton>
      ))}
    </Toolbar>
  );
}

/** A Fluent `Badge` marking a service the real Azure portal offers that this
 *  simulator does not implement. State never rests on colour alone: the
 *  badge carries its own icon and the words "Not supported" as real text, so
 *  it reads the same to a screen reader as to a sighted operator. */
export function AzureNotSupportedBadge() {
  return (
    <Badge
      appearance="tint"
      color="warning"
      size="small"
      className="az-badge-unsupported"
      icon={<AzureIcon name="not_supported" size={12} />}
    >
      Not supported
    </Badge>
  );
}

function ServiceMenuLink({ item, active }: { item: ServiceMenuItem; active: boolean }) {
  const styles = useAzureStyles();
  const navigate = useNavigate();
  const unsupported = item.supported === false;
  return (
    <li className={styles.serviceRow}>
      <Link
        href={item.to}
        appearance="subtle"
        // aria-label replaces the link's accessible name outright, so an
        // unsupported service announces why it's there rather than just that
        // it's there — "not supported in this simulator" reads the same for a
        // screen-reader operator as the visible badge reads for a sighted one.
        aria-label={unsupported ? `${item.label}, not supported in this simulator` : undefined}
        aria-current={active ? "page" : undefined}
        className={mergeClasses(
          "az-service-link",
          styles.serviceLink,
          active && mergeClasses("az-service-link-active", styles.serviceLinkActive),
          unsupported && mergeClasses("az-service-link-unsupported", styles.serviceLinkUnsupported),
        )}
        onClick={(event) => {
          event.preventDefault();
          navigate(item.to);
        }}
      >
        <span className={mergeClasses("az-service-label", styles.serviceLabel)}>{item.label}</span>
        {unsupported ? <AzureNotSupportedBadge /> : null}
      </Link>
    </li>
  );
}

/** The service menu carries its own search and expandable groups (a real
 *  Fluent `Accordion`, one item per catalog group), so a long menu stays
 *  navigable without leaving the resource. It lists the real Azure catalog —
 *  including services this simulator does not implement, marked rather than
 *  hidden — so the menu reads as the real portal's information
 *  architecture. */
export function AzureServiceMenu({ groups, flat }: { groups: ServiceMenuGroup[]; flat: ServiceMenuItem[] }) {
  const styles = useAzureStyles();
  const { pathname } = useLocation();
  const [filter, setFilter] = useState("");
  const [openGroups, setOpenGroups] = useState<string[]>(() => groups.map((group) => group.label));
  const needle = filter.trim().toLowerCase();
  const itemMatches = (item: ServiceMenuItem) => !needle || item.label.toLowerCase().includes(needle);

  const visibleGroups = groups
    .map((group) => ({ label: group.label, items: group.items.filter(itemMatches) }))
    .filter((group) => group.items.length > 0);
  // A search narrows to what matched, so a group is opened to show its
  // matches even when the operator had collapsed it.
  const openItems = needle ? visibleGroups.map((group) => group.label) : openGroups;

  return (
    <nav aria-label="Service" className={mergeClasses("az-service-menu", styles.serviceMenu)}>
      <Input
        className={styles.serviceSearch}
        type="search"
        value={filter}
        aria-label="Search the service menu"
        placeholder="Search"
        contentBefore={<AzureIcon name="search" size={14} />}
        onChange={(_, data) => setFilter(data.value)}
      />
      <ul className={styles.serviceList}>
        {flat.filter(itemMatches).map((item) => (
          <ServiceMenuLink key={item.to} item={item} active={pathname === item.to} />
        ))}
      </ul>
      <Accordion multiple collapsible openItems={openItems} onToggle={(_, data) => setOpenGroups(data.openItems as string[])}>
        {visibleGroups.map((group) => (
          <AccordionItem value={group.label} key={group.label}>
            <AccordionHeader expandIconPosition="start" size="small" button={{ className: "az-service-group-toggle" }}>
              {group.label}
            </AccordionHeader>
            <AccordionPanel className={styles.serviceGroupPanel}>
              <ul className={styles.serviceList}>
                {group.items.map((item) => (
                  <ServiceMenuLink key={item.to} item={item} active={pathname === item.to} />
                ))}
              </ul>
            </AccordionPanel>
          </AccordionItem>
        ))}
      </Accordion>
    </nav>
  );
}

export interface EssentialsProperty {
  label: string;
  value: ReactNode;
}

/** Essentials presents a resource's properties as a two-column key/value grid
 *  under a real Fluent `Accordion`, which is how the portal leads every
 *  resource page before its tabs. */
export function AzureEssentials({ properties }: { properties: EssentialsProperty[] }) {
  const styles = useAzureStyles();
  const [openItems, setOpenItems] = useState<string[]>(["essentials"]);
  return (
    <section className={styles.essentialsSection} aria-label="Essentials">
      <Accordion
        collapsible
        openItems={openItems}
        onToggle={(_, data) => setOpenItems(data.openItems as string[])}
      >
        <AccordionItem value="essentials">
          <AccordionHeader expandIconPosition="start" size="small">
            Essentials
          </AccordionHeader>
          <AccordionPanel>
            <dl className={styles.essentialsGrid}>
              {properties.map((property) => (
                <div className={mergeClasses("az-essentials-pair", styles.essentialsPair)} key={property.label}>
                  <dt className={styles.essentialsLabel}>{property.label}</dt>
                  <dd className={styles.essentialsValue}>{property.value}</dd>
                </div>
              ))}
            </dl>
          </AccordionPanel>
        </AccordionItem>
      </Accordion>
    </section>
  );
}

export type AzureStatusKind = "success" | "error" | "warning" | "inactive";

const KIND_TO_BADGE: Record<AzureStatusKind, { color: BadgeProps["color"]; icon: AzureIconName }> = {
  success: { color: "success", icon: "status_success" },
  error: { color: "danger", icon: "status_error" },
  warning: { color: "warning", icon: "status_warning" },
  inactive: { color: "informative", icon: "status_inactive" },
};

export function AzureStatus({ status, kind: explicitKind }: { status: string; kind?: AzureStatusKind }) {
  // Matched on whole words. A substring test reports success for failure
  // states, because "unavailable" contains "available" and "inactive" contains
  // "active", and a tick beside a failed resource stops an operator looking
  // any further.
  const words = status.toLowerCase().split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  const derived: AzureStatusKind = has(
    "unavailable",
    "stopped",
    "stopping",
    "failed",
    "failure",
    "error",
    "deleted",
    "inactive",
    "canceled",
    "cancelled",
  )
    ? "error"
    : has("pending", "provisioning", "creating", "updating", "deleting", "starting")
      ? "warning"
      : has("running", "active", "available", "succeeded", "success", "ok", "healthy", "ready")
        ? "success"
        : "inactive";
  const kind = explicitKind ?? derived;
  const { color, icon } = KIND_TO_BADGE[kind];
  return (
    <Badge appearance="tint" color={color} className={`az-status-${kind}`} icon={<AzureIcon name={icon} size={14} />}>
      {status}
    </Badge>
  );
}

/** A load failure, rendered as a real Fluent `MessageBar`. The `role="alert"`
 *  wrapper is this portal's own addition on top of Fluent's own markup
 *  (MessageBar's default role is `group`, not an ARIA live region) so a
 *  screen reader announces a failed read the moment it happens. */
export function AzureErrorMessage({ children, testid }: { children: ReactNode; testid?: string }) {
  return (
    <div role="alert" data-testid={testid} className="az-message az-message-error">
      <MessageBar intent="error">
        <MessageBarBody>{children}</MessageBarBody>
      </MessageBar>
    </div>
  );
}

/** A non-failure notice — the admin-user-disabled and one-time-secret
 *  notices this portal shows — as a real Fluent `MessageBar` with
 *  `role="status"` so assistive tech picks up the change without the
 *  operator going looking for it. */
export function AzureWarningMessage({ children, testid }: { children: ReactNode; testid?: string }) {
  return (
    <div role="status" data-testid={testid} className="az-message az-message-warning">
      <MessageBar intent="warning">
        <MessageBarBody>{children}</MessageBarBody>
      </MessageBar>
    </div>
  );
}

/** A resource's empty or loading placeholder. `role="status"` while loading
 *  lets assistive tech pick up the change without the operator having to go
 *  looking for it. */
export function AzureEmptyState({
  title,
  description,
  loading,
}: {
  title: string;
  description?: string;
  loading?: boolean;
}) {
  const styles = useAzureStyles();
  const body = (
    <div className={mergeClasses("az-empty", styles.emptyState)}>
      {loading ? <Spinner size="tiny" aria-label={title} className={styles.emptySpinner} /> : null}
      <Text as="strong" weight="semibold" block className={styles.emptyTitle}>
        {title}
      </Text>
      {description ? (
        <Text as="p" block>
          {description}
        </Text>
      ) : null}
    </div>
  );
  return loading ? <div role="status">{body}</div> : body;
}

/**
 * The delete-confirm surface every resource-DELETE flow in this portal uses —
 * a real Fluent `Dialog`, which traps focus on open, closes on Escape or an
 * overlay click via `onOpenChange`, and returns focus to whatever opened it
 * on close, all Fluent's own behavior rather than something this portal
 * re-implements. Once Azure Resource Manager has returned an error, backdrop
 * clicks leave the actionable error open until the operator explicitly
 * cancels or presses Escape. `title` is expected to name the resource (or the
 * count, when several are selected) and `children` to state the consequence
 * and that it can't be undone — the caller's copy, not a generic warning this
 * component supplies, since what a delete destroys differs per resource.
 */
export function AzureConfirmDialog({
  open,
  title,
  confirmLabel = "Delete",
  busy,
  testid,
  error,
  onConfirm,
  onCancel,
  children,
}: {
  open: boolean;
  title: string;
  confirmLabel?: string;
  busy: boolean;
  testid: string;
  error?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
  children: ReactNode;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={(event, data) => {
        if (!data.open && (busy || (error && data.type === "backdropClick"))) {
          event.preventDefault();
          return;
        }
        if (!data.open) onCancel();
      }}
    >
      <DialogSurface data-testid={testid} backdrop={{ id: `${testid}-backdrop` }}>
        <DialogBody>
          <DialogTitle>{title}</DialogTitle>
          <DialogContent>
            {children}
            {error ? <AzureErrorMessage testid={`${testid}-error`}>{error}</AzureErrorMessage> : null}
          </DialogContent>
          <DialogActions>
            <Button
              appearance="primary"
              data-testid={`${testid}-confirm`}
              disabled={busy}
              onClick={onConfirm}
            >
              {busy ? "Deleting…" : confirmLabel}
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
