import { type ReactNode } from "react";
import { BrowserRouter, Routes } from "react-router";
import { ErrorBoundary } from "@sockerless/ui-core/components";
import { GcpHeader, GcpProductNav, type GcpNavItem } from "./GcpConsole.js";
import { GcpAccount } from "./GcpAccount.js";
import { ProjectPickerChip } from "./ProjectPicker.js";
import { ProjectProvider } from "./project.js";

/** The console navigates a single product flat, the way Cloud Run lists
 *  Overview, Services, Jobs. The simulator spans several products, so the
 *  navigation is one flat list under a single product identity. */
const NAV_ITEMS: GcpNavItem[] = [
  { label: "Overview", to: "/ui/", icon: "home" },
  { label: "Cloud Run jobs", to: "/ui/cloudrun", icon: "deployed_code" },
  { label: "Cloud Run functions", to: "/ui/functions", icon: "function" },
  { label: "Artifact Registry", to: "/ui/ar", icon: "inventory_2" },
  { label: "Cloud Storage", to: "/ui/gcs", icon: "database" },
  { label: "Service accounts", to: "/ui/serviceaccounts", icon: "manage_accounts" },
  { label: "Manage resources", to: "/ui/projects", icon: "folder_data" },
  { label: "Logs Explorer", to: "/ui/logging", icon: "monitoring" },
];

function ConsoleFrame({ children }: { children: ReactNode }) {
  return (
    <div className="gcp">
      <a href="#main-content" className="sl-skip-link">Skip to main content</a>
      <GcpHeader picker={<ProjectPickerChip />} account={<GcpAccount />} />
      <div className="gc-body">
        <GcpProductNav product="Simulator" items={NAV_ITEMS} />
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
          <ConsoleFrame>
            <Routes>{children}</Routes>
          </ConsoleFrame>
        </ProjectProvider>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
