import { type ReactNode } from "react";

export interface PageHeadingProps {
  /** Small label printed above the title — typically a section/category. */
  kicker?: string;
  /** The headline. Rendered in the serif display voice. */
  title: ReactNode;
  /** Sub-line under the title (monospace). */
  meta?: ReactNode;
  /** Right-aligned actions / button row. */
  actions?: ReactNode;
}

export function PageHeading({ kicker, title, meta, actions }: PageHeadingProps) {
  return (
    <header className="mb-7 flex flex-wrap items-center justify-between gap-x-8 gap-y-3"
    >
      <div className="min-w-0 flex-1">
        {kicker && (
          <div
            className="mb-2 text-[11px] font-bold uppercase tracking-[0.16em]"
            style={{ color: "var(--color-accent)" }}
          >
            {kicker}
          </div>
        )}
        <h2
          className="font-display"
          style={{
            fontWeight: 800,
            fontSize: "clamp(1.75rem, 2.4vw, 2.55rem)",
            lineHeight: 1.1,
            letterSpacing: "-0.025em",
            color: "var(--color-fg)",
          }}
        >
          {title}
        </h2>
        {meta && (
          <div
            className="mt-2 text-sm"
            style={{ color: "var(--color-fg-muted)" }}
          >
            {meta}
          </div>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </header>
  );
}
