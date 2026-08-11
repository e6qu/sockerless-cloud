import { BrowserRouter, NavLink, Route, Routes } from "react-router";
import { AppShell, NavLinkButton, type NavItem } from "./AppShell.js";
import { ErrorBoundary } from "./ErrorBoundary.js";
import { ToastProvider } from "./Toast.js";
import { OverviewPage } from "../pages/OverviewPage.js";
import { ContainersPage } from "../pages/ContainersPage.js";
import { ResourcesPage } from "../pages/ResourcesPage.js";
import { MetricsPage } from "../pages/MetricsPage.js";

const navItems: NavItem[] = [
  { label: "Overview", to: "/ui/" },
  { label: "Containers", to: "/ui/containers" },
  { label: "Resources", to: "/ui/resources" },
  { label: "Metrics", to: "/ui/metrics" },
];

function renderNavLink(item: NavItem) {
  return (
    <NavLink to={item.to} end={item.to === "/ui/"}>
      {({ isActive }) => <NavLinkButton active={isActive}>{item.label}</NavLinkButton>}
    </NavLink>
  );
}

export interface BackendAppProps {
  title: string;
  /** Optional kicker — defaults to "sockerless · backend". */
  kicker?: string;
}

export function BackendApp({ title, kicker = "sockerless · backend" }: BackendAppProps) {
  return (
    <ErrorBoundary>
      <ToastProvider>
        <BrowserRouter>
          <AppShell
            title={title}
            kicker={kicker}
            navItems={navItems}
            renderLink={renderNavLink}
          >
            <Routes>
              <Route path="/ui/" element={<OverviewPage />} />
              <Route path="/ui/containers" element={<ContainersPage />} />
              <Route path="/ui/resources" element={<ResourcesPage />} />
              <Route path="/ui/metrics" element={<MetricsPage />} />
            </Routes>
          </AppShell>
        </BrowserRouter>
      </ToastProvider>
    </ErrorBoundary>
  );
}
