import { type ReactNode } from "react";
import { ThemeToggle } from "./ThemeToggle.js";

export interface NavItem {
  label: string;
  to: string;
}

export interface AppShellProps {
  title: string;
  /** Optional small label printed above the title. Acts as a kicker. */
  kicker?: string;
  navItems: NavItem[];
  renderLink: (item: NavItem, isActive?: boolean) => ReactNode;
  /** Optional signed-in user control rendered above the global shell controls. */
  accountControl?: ReactNode;
  children: ReactNode;
}

export function AppShell({ title, kicker, navItems, renderLink, accountControl, children }: AppShellProps) {
  return (
    <div className="sl-shell">
      <a
        href="#main-content"
        className="sl-skip-link"
      >
        Skip to main content
      </a>
      <aside aria-label="Sidebar" className="sl-sidebar">
        <div className="sl-brand">
          <span className="sl-brand-mark" aria-hidden>{title.slice(0, 1).toUpperCase()}</span>
          <div className="min-w-0">
            <h1 className="truncate text-lg font-display" style={{ color: "var(--color-fg)", lineHeight: 1.15 }}>{title}</h1>
            {kicker && <div className="mt-1 truncate text-[10px] font-semibold uppercase tracking-[0.14em]" style={{ color: "var(--color-fg-subtle)" }}>{kicker}</div>}
          </div>
        </div>

        <nav aria-label="Primary" className="sl-nav">
          <ul>
            {navItems.map((item, i) => (
              <li
                key={item.to}
                className="reveal"
                style={{ "--reveal-delay": `${i * 30}ms` } as React.CSSProperties}
              >
                {renderLink(item)}
              </li>
            ))}
          </ul>
        </nav>

        {accountControl && <div className="sl-account">{accountControl}</div>}
        <div className="sl-sidebar-footer">
          <span>sockerless · operator</span>
          <ThemeToggle />
        </div>
      </aside>
      <main id="main-content" tabIndex={-1} className="sl-main">
        <div className="sl-main-inner">{children}</div>
      </main>
    </div>
  );
}

export interface NavLinkButtonProps {
  active: boolean;
  children: ReactNode;
}

export function NavLinkButton({ active, children }: NavLinkButtonProps) {
  return (
    <span className="sl-nav-link" data-active={active ? "true" : "false"}>
      {children}
    </span>
  );
}
