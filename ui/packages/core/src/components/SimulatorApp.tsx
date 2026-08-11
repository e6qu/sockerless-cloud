import { type ReactNode } from "react";
import { BrowserRouter, NavLink, Routes } from "react-router";
import { AppShell, NavLinkButton, type NavItem } from "./AppShell.js";
import { ErrorBoundary } from "./ErrorBoundary.js";
import { ToastProvider } from "./Toast.js";
import { OperatorAccount } from "./OperatorAccount.js";

function renderNavLink(item: NavItem) {
  return (
    <NavLink to={item.to} end={item.to === "/ui/"}>
      {({ isActive }) => <NavLinkButton active={isActive}>{item.label}</NavLinkButton>}
    </NavLink>
  );
}

export interface SimulatorAppProps {
  title: string;
  /** Optional small kicker shown above the title (e.g. "aws · simulator"). */
  kicker?: string;
  navItems: NavItem[];
  children: ReactNode;
}

export function SimulatorApp({ title, kicker, navItems, children }: SimulatorAppProps) {
  return (
    <ErrorBoundary>
      <ToastProvider>
        <BrowserRouter>
          <AppShell
            title={title}
            kicker={kicker ?? "cloud · simulator"}
            navItems={navItems}
            renderLink={renderNavLink}
            accountControl={<OperatorAccount />}
          >
            <Routes>{children}</Routes>
          </AppShell>
        </BrowserRouter>
      </ToastProvider>
    </ErrorBoundary>
  );
}
