/**
 * Status pip + label. Sharp-edged. Dot prefix carries the colour;
 * label sits beside it in monospace small-caps. Operator-tool
 * vocabulary, not SaaS-pill aesthetic.
 */

const statusToken: Record<string, { fg: string; soft: string; dot: string }> = {
  // Container / job lifecycle
  running:             { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
  in_progress:         { fg: "var(--color-status-info)",  soft: "var(--color-status-info-soft)",    dot: "var(--color-status-info)" },
  starting:            { fg: "var(--color-status-info)",  soft: "var(--color-status-info-soft)",    dot: "var(--color-status-info)" },
  created:             { fg: "var(--color-status-info)",  soft: "var(--color-status-info-soft)",    dot: "var(--color-status-info)" },
  queued:              { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  pending:             { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  pending_concurrency: { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  waiting:             { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  // Outcomes
  completed:           { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
  success:             { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
  ok:                  { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
  warning:             { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  failure:             { fg: "var(--color-status-error)", soft: "var(--color-status-error-soft)",   dot: "var(--color-status-error)" },
  failed:              { fg: "var(--color-status-error)", soft: "var(--color-status-error-soft)",   dot: "var(--color-status-error)" },
  error:               { fg: "var(--color-status-error)", soft: "var(--color-status-error-soft)",   dot: "var(--color-status-error)" },
  cancelled:           { fg: "var(--color-fg-muted)",     soft: "var(--color-status-neutral-soft)", dot: "var(--color-fg-subtle)" },
  skipped:             { fg: "var(--color-fg-muted)",     soft: "var(--color-status-neutral-soft)", dot: "var(--color-fg-subtle)" },
  // Idle / lifecycle terminals
  exited:              { fg: "var(--color-fg-muted)",     soft: "var(--color-status-neutral-soft)", dot: "var(--color-fg-subtle)" },
  stopping:            { fg: "var(--color-status-warn)",  soft: "var(--color-status-warn-soft)",    dot: "var(--color-status-warn)" },
  stopped:             { fg: "var(--color-fg-muted)",     soft: "var(--color-status-neutral-soft)", dot: "var(--color-fg-subtle)" },
  offline:             { fg: "var(--color-fg-muted)",     soft: "var(--color-status-neutral-soft)", dot: "var(--color-fg-subtle)" },
  online:              { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
  active:              { fg: "var(--color-status-ok)",    soft: "var(--color-status-ok-soft)",      dot: "var(--color-status-ok)" },
};

const fallback = {
  fg: "var(--color-fg-muted)",
  soft: "var(--color-status-neutral-soft)",
  dot: "var(--color-fg-subtle)",
};

export interface StatusBadgeProps {
  status: string;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const t = statusToken[status] ?? fallback;
  return (
    <span
      className="inline-flex items-center gap-1.5 px-1.5 py-0.5 font-mono"
      style={{
        background: t.soft,
        color: t.fg,
        borderRadius: "var(--radius-sm)",
        fontSize: "0.68rem",
        fontWeight: 500,
        letterSpacing: "0.04em",
        textTransform: "uppercase",
        lineHeight: 1.4,
      }}
    >
      <span
        aria-hidden
        style={{
          display: "inline-block",
          width: 6,
          height: 6,
          borderRadius: "999px",
          background: t.dot,
          boxShadow: `0 0 0 2px color-mix(in oklch, ${t.dot} 25%, transparent)`,
        }}
      />
      {status}
    </span>
  );
}
