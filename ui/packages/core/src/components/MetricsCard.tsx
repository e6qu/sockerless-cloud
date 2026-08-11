export interface MetricsCardProps {
  title: string;
  value: string | number;
  subtitle?: string;
  /** Optional emphasis — pulls the number into the accent colour. */
  emphasized?: boolean;
}

export function MetricsCard({ title, value, subtitle, emphasized }: MetricsCardProps) {
  return (
    <div
      className="relative overflow-hidden px-5 py-5"
      style={{
        background: "var(--color-surface)",
        border: "1px solid var(--color-border)",
        borderRadius: "var(--radius-md)",
        boxShadow: "var(--shadow-card)",
      }}
    >
      <div
        className="text-xs font-semibold"
        style={{ color: "var(--color-fg-muted)" }}
      >
        {title}
      </div>
      <div
        className="mt-3 font-display tabular-nums"
        style={{
          fontSize: "2rem",
          fontWeight: 800,
          letterSpacing: "-0.02em",
          lineHeight: 1.05,
          color: emphasized ? "var(--color-accent)" : "var(--color-fg)",
        }}
      >
        {value}
      </div>
      <span aria-hidden style={{ position: "absolute", right: -18, bottom: -24, width: 74, height: 74, borderRadius: 999, background: "var(--color-accent-soft)", opacity: emphasized ? 0.9 : 0.5 }} />
      {subtitle && (
        <div
          className="mt-2 text-[11px] font-mono"
          style={{ color: "var(--color-fg-muted)" }}
        >
          {subtitle}
        </div>
      )}
    </div>
  );
}
