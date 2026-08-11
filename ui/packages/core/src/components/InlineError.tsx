import { type ReactNode } from "react";

export interface InlineErrorProps {
  /** Short context: "Failed to load containers". */
  title: string;
  /** Optional detail (typically err.message). Rendered in a muted code block. */
  detail?: string | Error | null;
  /** Action slot (Retry button, Reload, etc.). */
  action?: ReactNode;
  /** Lighter visual weight when nested inside a panel. */
  inline?: boolean;
}

/**
 * Inline error banner. Use for in-page operation failures (data load,
 * mutation rejected) where the user should still see the surrounding
 * UI. Catastrophic crashes go through ErrorBoundary instead.
 */
export function InlineError({ title, detail, action, inline }: InlineErrorProps) {
  const message = detail instanceof Error ? detail.message : detail || "";
  return (
    <div
      role="alert"
      className={inline ? "px-4 py-3" : "my-4 px-5 py-4"}
      style={{
        background: "var(--color-status-error-soft)",
        border: "1px solid color-mix(in oklch, var(--color-status-error) 30%, transparent)",
        borderLeft: "3px solid var(--color-status-error)",
        borderRadius: "var(--radius-sm)",
        color: "var(--color-fg)",
      }}
    >
      <div className="flex items-start gap-3">
        <div className="flex-1">
          <div
            className="mb-1 text-[10px] uppercase tracking-[0.18em]"
            style={{ color: "var(--color-status-error)" }}
          >
            error
          </div>
          <div
            className="font-display"
            style={{
              fontStyle: "italic",
              fontWeight: 600,
              fontSize: inline ? "0.95rem" : "1.1rem",
              lineHeight: 1.2,
            }}
          >
            {title}
          </div>
          {message && (
            <pre
              className="mt-2 font-mono whitespace-pre-wrap"
              style={{
                fontSize: "0.78rem",
                color: "var(--color-fg-muted)",
                margin: 0,
              }}
            >
              {message}
            </pre>
          )}
        </div>
        {action && <div className="flex-shrink-0">{action}</div>}
      </div>
    </div>
  );
}
