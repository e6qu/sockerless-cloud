import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

export type ToastTone = "info" | "success" | "warn" | "error";

export interface Toast {
  id: string;
  tone: ToastTone;
  title: string;
  body?: string;
  /** ms before auto-dismiss; 0 disables. Default 5000. */
  duration?: number;
}

interface ToastContext {
  toasts: Toast[];
  push: (t: Omit<Toast, "id">) => string;
  dismiss: (id: string) => void;
}

const Ctx = createContext<ToastContext | null>(null);

/**
 * useToast pushes operator-facing notifications. The Provider renders
 * a stack at the top-right of the viewport; the hook returns push +
 * dismiss + the current list (mostly for tests).
 */
export function useToast(): ToastContext {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useToast must be used inside <ToastProvider>");
  return ctx;
}

export interface ToastProviderProps {
  children: ReactNode;
  /** Default auto-dismiss ms when a toast doesn't supply its own. Default 5000. */
  defaultDuration?: number;
}

export function ToastProvider({ children, defaultDuration = 5000 }: ToastProviderProps) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const idRef = useRef(0);

  const dismiss = useCallback((id: string) => {
    setToasts((cur) => cur.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (t: Omit<Toast, "id">) => {
      idRef.current += 1;
      const id = `toast-${idRef.current}`;
      const duration = t.duration ?? defaultDuration;
      setToasts((cur) => [...cur, { ...t, id, duration }]);
      if (duration > 0) {
        // Schedule dismissal — caller can also dismiss early.
        window.setTimeout(() => dismiss(id), duration);
      }
      return id;
    },
    [defaultDuration, dismiss],
  );

  const value = useMemo(() => ({ toasts, push, dismiss }), [toasts, push, dismiss]);

  return (
    <Ctx.Provider value={value}>
      {children}
      <ToastStack toasts={toasts} onDismiss={dismiss} />
    </Ctx.Provider>
  );
}

interface ToastStackProps {
  toasts: Toast[];
  onDismiss: (id: string) => void;
}

function ToastStack({ toasts, onDismiss }: ToastStackProps) {
  if (toasts.length === 0) return null;
  return (
    <div
      role="region"
      aria-label="Notifications"
      className="pointer-events-none fixed right-4 top-4 z-50 flex max-w-sm flex-col gap-2"
    >
      {toasts.map((t) => (
        <ToastCard key={t.id} toast={t} onDismiss={() => onDismiss(t.id)} />
      ))}
    </div>
  );
}

interface ToastCardProps {
  toast: Toast;
  onDismiss: () => void;
}

const toneColor: Record<ToastTone, string> = {
  info: "var(--color-status-info)",
  success: "var(--color-status-ok)",
  warn: "var(--color-status-warn)",
  error: "var(--color-status-error)",
};

function ToastCard({ toast, onDismiss }: ToastCardProps) {
  const accent = toneColor[toast.tone];
  return (
    <div
      role="alert"
      aria-live={toast.tone === "error" ? "assertive" : "polite"}
      className="pointer-events-auto reveal flex items-start gap-3 px-3 py-2.5"
      style={{
        background: "var(--color-surface-raised)",
        border: "1px solid var(--color-border)",
        borderLeft: `3px solid ${accent}`,
        borderRadius: "var(--radius-sm)",
        color: "var(--color-fg)",
        boxShadow: "0 8px 24px -8px color-mix(in oklch, var(--color-fg) 22%, transparent)",
        minWidth: 280,
      }}
    >
      <div className="flex-1">
        <div
          className="mb-0.5 text-[10px] uppercase tracking-[0.18em]"
          style={{ color: accent }}
        >
          {toast.tone}
        </div>
        <div
          className="font-display"
          style={{ fontStyle: "italic", fontWeight: 600, fontSize: "0.95rem", lineHeight: 1.2 }}
        >
          {toast.title}
        </div>
        {toast.body && (
          <div
            className="mt-1 font-mono"
            style={{ fontSize: "0.75rem", color: "var(--color-fg-muted)" }}
          >
            {toast.body}
          </div>
        )}
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss notification"
        className="inline-flex h-6 w-6 items-center justify-center"
        style={{
          background: "transparent",
          color: "var(--color-fg-subtle)",
          border: "none",
          fontSize: 14,
          lineHeight: 1,
        }}
      >
        ×
      </button>
    </div>
  );
}

/**
 * useReportError returns a callback that pushes an error toast given
 * an unknown error value (Error, string, anything stringable). Common
 * pattern: pass straight to TanStack Query's `onError` option.
 */
export function useReportError(): (err: unknown, title?: string) => void {
  const { push } = useToast();
  return useCallback(
    (err: unknown, title = "Operation failed") => {
      const body = err instanceof Error ? err.message : typeof err === "string" ? err : JSON.stringify(err);
      push({ tone: "error", title, body });
    },
    [push],
  );
}

/**
 * useToastQueryErrors observes a TanStack-Query-shaped error value
 * (anything truthy with a .message field) and toasts the first time it
 * appears. Use this when wrapping individual hook results that already
 * surface errors inline but you also want a notification.
 */
export function useToastQueryErrors(error: unknown, title?: string) {
  const report = useReportError();
  useEffect(() => {
    if (error) report(error, title);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [error]);
}
