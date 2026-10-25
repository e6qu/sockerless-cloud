import { useEffect, useState, type ReactNode } from "react";
import { BrowserRouter, Link as RouterLink, Routes, useLocation, useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import {
  Avatar,
  Button,
  CounterBadge,
  Divider,
  DrawerBody,
  DrawerHeader,
  DrawerHeaderTitle,
  FluentProvider,
  Link,
  OverlayDrawer,
  Popover,
  PopoverSurface,
  PopoverTrigger,
  Radio,
  RadioGroup,
  Text,
  Tooltip,
  webDarkTheme,
  webLightTheme,
  type Theme,
} from "@fluentui/react-components";
import { ErrorBoundary } from "@sockerless/ui-core/components";
import { useTheme } from "@sockerless/ui-core/hooks";
import { SERVICE_BY_ID } from "../catalog.js";
import { listTenants } from "../azure.js";
import { AzureIcon, ServiceTile } from "./icons.js";
import { GlobalSearch } from "./GlobalSearch.js";
import { NotificationsProvider, useNotifications, type PortalNotification } from "./notifications.js";
import { usePreferences } from "./preferences.js";
import { formatTime, StatusBadge } from "./AzurePortal.js";

// The portal's brand blue, #0078d4, on the header and primary buttons in the
// light theme, over Fluent 2's own web themes. The dark theme keeps Fluent's
// lighter brand ramp, which a dark surface needs for contrast.
const azureLightTheme: Theme = {
  ...webLightTheme,
  colorBrandBackground: "#0078d4",
  colorBrandBackgroundHover: "#106ebe",
  colorBrandBackgroundPressed: "#005a9e",
  colorBrandBackgroundSelected: "#005a9e",
  colorBrandForeground1: "#0067b8",
  colorBrandForegroundLink: "#0067b8",
  colorBrandForegroundLinkHover: "#005a9e",
};
const azureDarkTheme: Theme = {
  ...webDarkTheme,
};

interface UIConfig {
  identityEndpoint?: string;
  logoutEndpoint?: string;
  federationTenant?: string;
  cloudApiEndpoint?: string;
}

interface Identity {
  name?: string;
  email?: string;
  preferred_username?: string;
  picture?: string;
}

async function getJSON<T>(endpoint: string): Promise<T> {
  const response = await fetch(endpoint, { credentials: "include", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`${endpoint} returned HTTP ${response.status}`);
  return (await response.json()) as T;
}

export function useConsoleConfig() {
  return useQuery({ queryKey: ["console-config"], queryFn: () => getJSON<UIConfig>("/ui/config.json"), retry: false, staleTime: Infinity });
}

type Pane = "notifications" | "settings" | "help" | null;

function NotificationItem({ notification }: { notification: PortalNotification }) {
  const { dismiss } = useNotifications();
  const status =
    notification.status === "inProgress" ? (
      <StatusBadge status="In progress" kind="warning" />
    ) : notification.status === "succeeded" ? (
      <StatusBadge status="Succeeded" kind="success" />
    ) : (
      <StatusBadge status="Failed" kind="error" />
    );
  return (
    <li className="az-notification" data-testid="notification">
      <div className="az-notification-head">
        {status}
        <Text weight="semibold" className="az-notification-title">
          {notification.link ? <RouterLink to={notification.link}>{notification.title}</RouterLink> : notification.title}
        </Text>
        <Button
          appearance="transparent"
          size="small"
          aria-label={`Dismiss ${notification.title}`}
          icon={<AzureIcon name="dismiss" />}
          onClick={() => dismiss(notification.id)}
        />
      </div>
      <Text size={200} block>
        {notification.description}
      </Text>
      <Text size={100} block className="az-notification-time">
        {formatTime(notification.time)}
      </Text>
    </li>
  );
}

function NotificationsPane() {
  const { notifications, dismissAll } = useNotifications();
  const running = notifications.filter((entry) => entry.status === "inProgress").length;
  return (
    <>
      <div className="az-pane-tools">
        <Text size={200}>{running ? `${running} in progress` : "No operations in progress"}</Text>
        <Button appearance="transparent" size="small" onClick={dismissAll} disabled={notifications.length === 0}>
          Dismiss all
        </Button>
      </div>
      {notifications.length === 0 ? (
        <Text block className="az-pane-empty">
          No new notifications from this session.
        </Text>
      ) : (
        <ul className="az-notification-list" aria-label="Notifications">
          {notifications.map((notification) => (
            <NotificationItem key={notification.id} notification={notification} />
          ))}
        </ul>
      )}
    </>
  );
}

function SettingsPane({ theme, onTheme }: { theme: "light" | "dark"; onTheme: (theme: "light" | "dark") => void }) {
  return (
    <>
      <Text as="h3" weight="semibold" block>
        Appearance
      </Text>
      <RadioGroup aria-label="Theme" value={theme} onChange={(_, data) => onTheme(data.value as "light" | "dark")}>
        <Radio value="light" label="Light" />
        <Radio value="dark" label="Dark" />
      </RadioGroup>
    </>
  );
}

const HELP_LINKS = [
  { label: "Azure documentation", href: "https://learn.microsoft.com/azure/" },
  { label: "Azure portal documentation", href: "https://learn.microsoft.com/azure/azure-portal/" },
  { label: "Azure Resource Manager REST API reference", href: "https://learn.microsoft.com/rest/api/resources/" },
  { label: "Keyboard shortcuts in the Azure portal", href: "https://learn.microsoft.com/azure/azure-portal/azure-portal-keyboard-shortcuts" },
];

function HelpPane() {
  return (
    <ul className="az-help-list">
      {HELP_LINKS.map((link) => (
        <li key={link.href}>
          <Link href={link.href} target="_blank" rel="noreferrer">
            {link.label}
            <AzureIcon name="open" size={12} />
          </Link>
        </li>
      ))}
    </ul>
  );
}

function AccountCard() {
  const config = useConsoleConfig();
  const identityEndpoint = config.data?.identityEndpoint;
  const identity = useQuery({
    queryKey: ["console-identity", identityEndpoint],
    queryFn: () => getJSON<Identity>(identityEndpoint!),
    enabled: Boolean(identityEndpoint),
    retry: false,
  });
  const tenants = useQuery({ queryKey: ["tenants"], queryFn: listTenants, enabled: Boolean(identity.data) });
  if (!identityEndpoint || !config.data?.logoutEndpoint || !identity.data) return null;
  const name = identity.data.name || identity.data.preferred_username || identity.data.email || "Signed-in user";
  const email = identity.data.email || identity.data.preferred_username || "";
  const tenant = tenants.data?.find((entry) => entry.tenantId === config.data?.federationTenant) ?? tenants.data?.[0];
  const directory = tenant?.displayName ?? "";
  return (
    <Popover positioning="below-end" trapFocus>
      <PopoverTrigger disableButtonEnhancement>
        <button type="button" className="az-account" data-shauth-user={name}>
          <span className="sr-only">Account manager for {name}: </span>
          <span className="az-account-text">
            <span className="az-account-name">{email || name}</span>
            <span className="az-account-directory">{directory.toUpperCase()}</span>
          </span>
          <Avatar name={name} size={28} aria-hidden image={identity.data.picture ? { src: identity.data.picture } : undefined} />
        </button>
      </PopoverTrigger>
      <PopoverSurface aria-label="Account" role="dialog" className="az-account-card">
        <div className="az-account-card-head">
          <Text weight="semibold">{directory}</Text>
          <form method="post" action={config.data.logoutEndpoint}>
            <Button type="submit" appearance="transparent" size="small" icon={<AzureIcon name="sign_out" />} data-shauth-sign-out>
              Sign out
            </Button>
          </form>
        </div>
        <div className="az-account-card-body">
          <Avatar name={name} size={56} />
          <div>
            <Text weight="semibold" size={400} block>
              {name}
            </Text>
            <Text block>{email}</Text>
            {tenant ? (
              <Text size={200} block className="az-account-tenant">
                {tenant.defaultDomain} · Directory ID {tenant.tenantId}
              </Text>
            ) : null}
          </div>
        </div>
        <Divider />
        <Text size={200} block className="az-account-federation">
          Signed in with Microsoft Entra ID. The console reaches Azure through Workload Identity Federation.
        </Text>
      </PopoverSurface>
    </Popover>
  );
}

function HeaderButton({ label, icon, onClick, badge }: { label: string; icon: "notifications" | "settings" | "help"; onClick: () => void; badge?: number }) {
  return (
    <Tooltip content={label} relationship="label">
      <button type="button" className="az-header-button" onClick={onClick} data-testid={`header-${icon}`}>
        <AzureIcon name={icon} size={18} />
        {badge ? <CounterBadge count={badge} size="small" className="az-header-badge" aria-hidden /> : null}
      </button>
    </Tooltip>
  );
}

function PortalMenu({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { favorites } = usePreferences();
  const navigate = useNavigate();
  const go = (to: string) => {
    onClose();
    navigate(to);
  };
  const item = (label: string, to: string, icon: ReactNode) => (
    <li key={to}>
      <button type="button" className="az-portal-menu-item" onClick={() => go(to)}>
        {icon}
        <span>{label}</span>
      </button>
    </li>
  );
  return (
    <OverlayDrawer open={open} position="start" size="small" onOpenChange={(_, data) => !data.open && onClose()} aria-label="Portal menu">
      <DrawerHeader>
        <DrawerHeaderTitle
          action={<Button appearance="subtle" aria-label="Close portal menu" icon={<AzureIcon name="dismiss" />} onClick={onClose} />}
        >
          Microsoft Azure
        </DrawerHeaderTitle>
      </DrawerHeader>
      <DrawerBody>
        <nav aria-label="Portal menu">
          <ul className="az-portal-menu">
            {item("Create a resource", "/ui/create", <AzureIcon name="add" size={18} />)}
            {item("Home", "/ui/home", <AzureIcon name="home" size={18} />)}
            {item("All services", "/ui/allservices", <AzureIcon name="apps" size={18} />)}
          </ul>
          <Text as="h2" size={200} weight="semibold" className="az-portal-menu-heading">
            FAVORITES
          </Text>
          <ul className="az-portal-menu" aria-label="Favorites">
            {favorites
              .map((id) => SERVICE_BY_ID.get(id))
              .filter((service) => service !== undefined)
              .map((service) => item(service.label, service.route, <ServiceTile icon={service.icon} color={service.color} size={20} />))}
          </ul>
        </nav>
      </DrawerBody>
    </OverlayDrawer>
  );
}

function PortalFrame({ children, theme, onTheme }: { children: ReactNode; theme: "light" | "dark"; onTheme: (theme: "light" | "dark") => void }) {
  const [pane, setPane] = useState<Pane>(null);
  const [menu, setMenu] = useState(false);
  const { notifications, markRead } = useNotifications();
  const unread = notifications.filter((entry) => entry.unread).length;
  const { pathname } = useLocation();
  useEffect(() => setMenu(false), [pathname]);
  const openPane = (next: Exclude<Pane, null>) => {
    setPane(pane === next ? null : next);
    if (next === "notifications") markRead();
  };
  const title = pane === "notifications" ? "Notifications" : pane === "settings" ? "Portal settings" : "Help + support";
  return (
    <div className="azure az-shell">
      <a href="#main-content" className="sl-skip-link">
        Skip to main content
      </a>
      <header className="az-header">
        <Tooltip content="Show portal menu" relationship="label">
          <button type="button" className="az-header-button" onClick={() => setMenu(true)} aria-expanded={menu}>
            <AzureIcon name="menu" size={18} />
          </button>
        </Tooltip>
        <RouterLink to="/ui/home" className="az-wordmark">
          Microsoft Azure
        </RouterLink>
        <GlobalSearch />
        <div className="az-header-controls">
          <HeaderButton label="Notifications" icon="notifications" onClick={() => openPane("notifications")} badge={unread} />
          <HeaderButton label="Settings" icon="settings" onClick={() => openPane("settings")} />
          <HeaderButton label="Help + support" icon="help" onClick={() => openPane("help")} />
          <AccountCard />
        </div>
      </header>
      <PortalMenu open={menu} onClose={() => setMenu(false)} />
      <OverlayDrawer
        open={pane !== null}
        position="end"
        size="small"
        modalType="non-modal"
        onOpenChange={(_, data) => !data.open && setPane(null)}
        aria-label={title}
        className="az-pane"
      >
        <DrawerHeader>
          <DrawerHeaderTitle action={<Button appearance="subtle" aria-label={`Close ${title}`} icon={<AzureIcon name="dismiss" />} onClick={() => setPane(null)} />}>
            {title}
          </DrawerHeaderTitle>
        </DrawerHeader>
        <DrawerBody>
          {pane === "notifications" ? <NotificationsPane /> : pane === "settings" ? <SettingsPane theme={theme} onTheme={onTheme} /> : pane === "help" ? <HelpPane /> : null}
        </DrawerBody>
      </OverlayDrawer>
      <main id="main-content" tabIndex={-1} className="az-main">
        {children}
      </main>
    </div>
  );
}

export function AzureApp({ children }: { children: ReactNode }) {
  const { theme, setTheme } = useTheme("light");
  return (
    <ErrorBoundary>
      <FluentProvider theme={theme === "dark" ? azureDarkTheme : azureLightTheme}>
        <BrowserRouter>
          <NotificationsProvider>
            <PortalFrame theme={theme} onTheme={setTheme}>
              <Routes>{children}</Routes>
            </PortalFrame>
          </NotificationsProvider>
        </BrowserRouter>
      </FluentProvider>
    </ErrorBoundary>
  );
}
