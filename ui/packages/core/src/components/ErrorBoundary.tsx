import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: null };

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("ErrorBoundary caught:", error, info);
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) return this.props.fallback;
      return (
        <div
          className="m-6 p-6"
          style={{
            background: "var(--color-surface)",
            border: "1px solid var(--color-status-error)",
            borderLeft: "3px solid var(--color-status-error)",
            borderRadius: "var(--radius-sm)",
            color: "var(--color-fg)",
          }}
        >
          <div
            className="mb-2 text-[10px] uppercase tracking-[0.22em]"
            style={{ color: "var(--color-status-error)" }}
          >
            unhandled exception
          </div>
          <h2
            className="font-display"
            style={{
              fontStyle: "italic",
              fontWeight: 600,
              fontSize: "1.6rem",
              letterSpacing: "-0.02em",
            }}
          >
            Something broke.
          </h2>
          <pre
            className="mt-3 font-mono whitespace-pre-wrap"
            style={{
              fontSize: "0.78rem",
              color: "var(--color-fg-muted)",
              padding: "0.85rem 1rem",
              background: "var(--color-bg-subtle)",
              border: "1px solid var(--color-border)",
              borderRadius: "var(--radius-sm)",
            }}
          >
            {this.state.error?.message}
          </pre>
          <button
            onClick={() => window.location.reload()}
            className="mt-4 inline-flex items-center px-4 py-2 font-mono uppercase tracking-[0.08em]"
            style={{
              background: "var(--color-status-error)",
              color: "var(--color-bg)",
              border: "1px solid var(--color-status-error)",
              borderRadius: "var(--radius-sm)",
              fontSize: "0.78rem",
              fontWeight: 500,
            }}
          >
            Reload page
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
