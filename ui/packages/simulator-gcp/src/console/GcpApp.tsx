import { useEffect, useRef, useState, type ReactNode } from "react";
import { BrowserRouter, Link, NavLink, Routes, useLocation } from "react-router";
import { ErrorBoundary } from "@sockerless/ui-core/components";
import { useTheme } from "@sockerless/ui-core/hooks";
import { GcpNavDrawer } from "./GcpNavDrawer.js";
import { GcpAccount } from "./GcpAccount.js";
import { ProjectPickerChip } from "./ProjectPicker.js";
import { ProjectProvider, useProject, useProjectPath } from "./project.js";
import { ConsoleSearch } from "./ConsoleSearch.js";
import { MenuButton } from "./menu.js";
import { NotificationsButton, OperationsProvider } from "./notifications.js";
import { GcpDialog } from "./GcpDialog.js";
import { Icon } from "./icons.js";
import { locate, productHome, type ConsoleProduct } from "./catalog.js";

function SettingsMenu() {
  const { theme, setTheme } = useTheme("light");
  return (
    <MenuButton
      label="Settings and utilities"
      icon="more_vert"
      header={<p className="gc-menu-heading">Appearance</p>}
      entries={[
        { label: "Light", checked: theme === "light", onSelect: () => setTheme("light") },
        { label: "Dark", checked: theme === "dark", onSelect: () => setTheme("dark") },
      ]}
    />
  );
}

const SHORTCUTS = [
  { keys: "/", action: "Search products and pages" },
  { keys: "Esc", action: "Close the open menu, panel or dialog" },
  { keys: "↑ ↓", action: "Move through a menu or the search results" },
];

function HelpMenu({ product }: { product?: ConsoleProduct }) {
  const [shortcuts, setShortcuts] = useState(false);
  return (
    <>
      <MenuButton
        label="Help"
        icon="help"
        entries={[
          ...(product ? [{ label: `${product.name} documentation`, icon: "description" as const, href: product.docs }] : []),
          { label: "Google Cloud documentation", icon: "description", href: "https://cloud.google.com/docs" },
          { label: "Keyboard shortcuts", icon: "keyboard", onSelect: () => setShortcuts(true) },
        ]}
      />
      {shortcuts ? (
        <GcpDialog title="Keyboard shortcuts" onClose={() => setShortcuts(false)}>
          <table className="gc-table">
            <tbody>
              {SHORTCUTS.map((shortcut) => (
                <tr key={shortcut.keys}>
                  <td>
                    <kbd className="gc-kbd">{shortcut.keys}</kbd>
                  </td>
                  <td>{shortcut.action}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="gc-dialog-actions">
            <button type="button" className="gc-button-text" onClick={() => setShortcuts(false)}>
              Close
            </button>
          </div>
        </GcpDialog>
      ) : null}
    </>
  );
}

function TopBar({ product }: { product?: ConsoleProduct }) {
  const [navOpen, setNavOpen] = useState(false);
  const menuRef = useRef<HTMLButtonElement>(null);
  const { pathname } = useLocation();
  const path = useProjectPath();
  return (
    <header className="gc-header">
      <div className="gc-header-left">
        <button
          ref={menuRef}
          type="button"
          className="gc-icon-button"
          aria-label="Navigation menu"
          title="Navigation menu"
          aria-haspopup="dialog"
          aria-expanded={navOpen}
          onClick={() => setNavOpen(true)}
        >
          <Icon name="menu" />
        </button>
        <GcpNavDrawer open={navOpen} onClose={() => setNavOpen(false)} triggerRef={menuRef} pathname={pathname} />
        <Link className="gc-wordmark" to={path("/ui/welcome")} aria-label="Google Cloud home">
          Google Cloud
        </Link>
        <ProjectPickerChip />
      </div>
      <ConsoleSearch />
      <div className="gc-header-right">
        <NotificationsButton />
        <HelpMenu product={product} />
        <SettingsMenu />
        <GcpAccount />
      </div>
    </header>
  );
}

function ProductNav({ product }: { product: ConsoleProduct }) {
  const path = useProjectPath();
  const { pathname } = useLocation();
  const current = locate(pathname).item;
  return (
    <nav aria-label={product.name} className="gc-product-nav">
      <Link className="gc-product-title" to={path(productHome(product))}>
        <Icon name={product.icon} size="1.5em" />
        <span>{product.name}</span>
      </Link>
      {product.nav.map((section, index) => (
        <div className="gc-nav-section" key={section.heading ?? index}>
          {section.heading ? <h2 className="gc-nav-heading">{section.heading}</h2> : null}
          <ul>
            {section.items.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={path(item.to)}
                  end
                  aria-current={current === item ? "page" : undefined}
                  className={current === item ? "gc-nav-link gc-nav-link-active" : "gc-nav-link"}
                >
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

const PAGE_TITLES: Record<string, string> = {
  "/ui/welcome": "Welcome",
  "/ui/home/dashboard": "Dashboard",
  "/ui/home/activity": "Activity",
  "/ui/products": "All products",
  "/ui/projectcreate": "New Project",
};

function ConsoleFrame({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const { project } = useProject();
  const { product, item } = locate(pathname);

  // The real console titles the tab with the page, the product and the project.
  useEffect(() => {
    const parts = product ? [item?.label, product.name] : [PAGE_TITLES[pathname]];
    document.title = [...parts, project, "Google Cloud console"].filter(Boolean).join(" – ");
  }, [pathname, product, item, project]);

  return (
    <div className="gcp">
      <a href="#main-content" className="sl-skip-link">Skip to main content</a>
      <TopBar product={product} />
      <div className={product ? "gc-body gc-body-with-nav" : "gc-body"}>
        {product ? <ProductNav product={product} /> : null}
        <main id="main-content" tabIndex={-1} className="gc-main">
          {children}
        </main>
      </div>
    </div>
  );
}

export function GcpApp({ children }: { children: ReactNode }) {
  return (
    <ErrorBoundary>
      <BrowserRouter>
        <ProjectProvider>
          <OperationsProvider>
            <ConsoleFrame>
              <Routes>{children}</Routes>
            </ConsoleFrame>
          </OperationsProvider>
        </ProjectProvider>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
