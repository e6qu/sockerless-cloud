import { type ReactNode } from "react";
import { BrowserRouter, Routes, useLocation } from "react-router";
import { ErrorBoundary, OperatorAccount } from "@sockerless/ui-core/components";
import { useTheme } from "@sockerless/ui-core/hooks";
import { FluentProvider, webDarkTheme, webLightTheme } from "@fluentui/react-components";
import type { Theme } from "@fluentui/react-components";

// The Azure portal's most recognizable surface is its blue global header and
// primary buttons — the iconic Azure blue #0078d4, not Fluent's stock brand
// #0f6cbd. Overriding just the brand-background tokens keeps every other Fluent
// behavior intact while making that signature colour faithful. Light mode uses
// the classic Azure blue and its documented hover/pressed shades; dark mode
// keeps Fluent's lighter brand ramp (a dark surface needs the lighter blue for
// AA contrast), only nudging the base to Azure's dark-portal blue.
const azureLightTheme: Theme = {
  ...webLightTheme,
  colorBrandBackground: "#0078d4",
  colorBrandBackgroundHover: "#106ebe",
  colorBrandBackgroundPressed: "#005a9e",
  colorBrandBackgroundSelected: "#005a9e",
};
const azureDarkTheme: Theme = {
  ...webDarkTheme,
  colorBrandBackground: "#0078d4",
  colorBrandBackgroundHover: "#2b88d8",
  colorBrandBackgroundPressed: "#005a9e",
  colorBrandBackgroundSelected: "#106ebe",
};
import { SERVICE_CATALOG, catalogItemForPath } from "../catalog.js";
import { SERVICE_BLADES } from "../services.js";
import {
  AzureBreadcrumbs,
  AzureHeader,
  AzureResourceTitle,
  AzureServiceMenu,
  useAzureStyles,
  type ServiceMenuGroup,
  type ServiceMenuItem,
} from "./AzurePortal.js";

/** The portal opens a resource on the items that apply to any resource, then
 *  groups the rest. The simulator's menu follows the same shape. The groups
 *  themselves come from the service catalog — the real Azure "All services"
 *  categories, carrying every service this simulator implements and a
 *  faithful representative set of the ones it doesn't (see catalog.ts). */
const FLAT_ITEMS: ServiceMenuItem[] = [{ label: "Overview", to: "/ui/" }];

const MENU_GROUPS: ServiceMenuGroup[] = SERVICE_CATALOG.map((group) => ({
  label: group.label,
  items: group.items.map((item) => ({ label: item.label, to: item.to, supported: item.supported })),
}));

interface Pane {
  crumb: string;
  title: string;
  kind: string;
  /** An intermediate breadcrumb for panes nested under a listing blade. */
  parent?: { label: string; to: string };
}

// Every descriptor-driven service blade contributes its own listing pane and,
// through paneFor below, its resource pane — so a blade added to services.ts
// carries a correct breadcrumb and resource title without a second table here
// to keep in step.
const BLADE_PANES: Record<string, Pane> = Object.fromEntries(
  SERVICE_BLADES.map((blade) => [`/ui/${blade.slug}`, { crumb: blade.label, title: blade.label, kind: blade.kind }]),
);

const PANE: Record<string, Pane> = {
  ...BLADE_PANES,
  "/ui/": { crumb: "Overview", title: "Simulator", kind: "Subscription" },
  "/ui/subscriptions": { crumb: "Subscriptions", title: "Subscriptions", kind: "Subscription" },
  "/ui/container-apps": { crumb: "Container App jobs", title: "Container App jobs", kind: "Container Apps job" },
  "/ui/functions": { crumb: "Function Apps", title: "Function Apps", kind: "Function App" },
  "/ui/acr": { crumb: "Container registries", title: "Container registries", kind: "Container registry" },
  "/ui/storage": { crumb: "Storage accounts", title: "Storage accounts", kind: "Storage account" },
  "/ui/monitor": { crumb: "Logs", title: "Logs", kind: "Log Analytics workspace" },
  "/ui/entra/app-registrations": {
    crumb: "App registrations",
    title: "App registrations",
    kind: "Microsoft Entra ID",
  },
};

function paneFor(pathname: string): Pane {
  const exact = PANE[pathname];
  if (exact) return exact;
  for (const blade of SERVICE_BLADES) {
    if (pathname.startsWith(`/ui/${blade.slug}/`)) {
      return {
        crumb: blade.detailKind,
        title: blade.detailKind,
        kind: blade.kind,
        parent: { label: blade.label, to: `/ui/${blade.slug}` },
      };
    }
  }
  if (pathname.startsWith("/ui/subscriptions/")) {
    return {
      crumb: "Subscription",
      title: "Subscription",
      kind: "Subscription",
      parent: { label: "Subscriptions", to: "/ui/subscriptions" },
    };
  }
  if (pathname.startsWith("/ui/container-apps/")) {
    return {
      crumb: "Job",
      title: "Container Apps job",
      kind: "Container Apps job",
      parent: { label: "Container App jobs", to: "/ui/container-apps" },
    };
  }
  if (pathname.startsWith("/ui/functions/")) {
    return {
      crumb: "Function App",
      title: "Function App",
      kind: "Function App",
      parent: { label: "Function Apps", to: "/ui/functions" },
    };
  }
  if (pathname.startsWith("/ui/acr/")) {
    return {
      crumb: "Registry",
      title: "Container registry",
      kind: "Container registry",
      parent: { label: "Container registries", to: "/ui/acr" },
    };
  }
  if (pathname.startsWith("/ui/storage/")) {
    return {
      crumb: "Storage account",
      title: "Storage account",
      kind: "Storage account",
      parent: { label: "Storage accounts", to: "/ui/storage" },
    };
  }
  if (pathname.startsWith("/ui/entra/app-registrations/")) {
    return {
      crumb: "Certificates & secrets",
      title: "App registration",
      kind: "Microsoft Entra ID",
      parent: { label: "App registrations", to: "/ui/entra/app-registrations" },
    };
  }
  if (pathname.startsWith("/ui/not-supported/")) {
    const item = catalogItemForPath(pathname);
    const label = item?.label ?? "Service";
    return { crumb: label, title: label, kind: item?.kind ?? "Service" };
  }
  return PANE["/ui/"];
}

function PortalFrame({
  children,
  isDark,
  onToggleTheme,
}: {
  children: ReactNode;
  isDark: boolean;
  onToggleTheme: () => void;
}) {
  const styles = useAzureStyles();
  const { pathname } = useLocation();
  const pane = paneFor(pathname);
  const trail = [
    { label: "Home", to: "/ui/" },
    ...(pane.parent ? [{ label: pane.parent.label, to: pane.parent.to }] : []),
    { label: pane.crumb },
  ];
  return (
    <div className={`azure ${styles.shell}`}>
      <a href="#main-content" className="sl-skip-link">Skip to main content</a>
      <AzureHeader account={<OperatorAccount />} isDark={isDark} onToggleTheme={onToggleTheme} />
      <AzureBreadcrumbs trail={trail} />
      <div className={`az-body ${styles.body}`}>
        <AzureResourceTitle name={pane.title} kind={pane.kind} directory="Simulator" />
        <AzureServiceMenu flat={FLAT_ITEMS} groups={MENU_GROUPS} />
        <main id="main-content" tabIndex={-1}>
          {children}
        </main>
      </div>
    </div>
  );
}

/**
 * `useAzureTheme` drives the shared `useTheme` hook every simulator UI uses
 * — the same hook that flips the `.dark` class the residual, non-Fluent
 * chrome still reads — and additionally selects between Fluent's own real
 * `webLightTheme`/`webDarkTheme`, which is the design system the real Azure
 * portal is built on and ships WCAG AA contrast in both modes.
 */
function useAzureTheme() {
  const { theme, toggle } = useTheme();
  return { theme, toggle };
}

export function AzureApp({ children }: { children: ReactNode }) {
  const { theme, toggle } = useAzureTheme();
  return (
    <ErrorBoundary>
      <FluentProvider theme={theme === "dark" ? azureDarkTheme : azureLightTheme}>
        <BrowserRouter>
          <PortalFrame isDark={theme === "dark"} onToggleTheme={toggle}>
            <Routes>{children}</Routes>
          </PortalFrame>
        </BrowserRouter>
      </FluentProvider>
    </ErrorBoundary>
  );
}
