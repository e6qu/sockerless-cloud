import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Icon } from "./icons.js";
import { useDismiss } from "./menu.js";

// The operations the operator started in this console session, as the real
// console's notifications panel lists them: each one runs the API call and,
// for a long-running operation, polls the service's own operations API until
// it is done. The snackbar reports the latest change; the bell keeps the list.

export type OperationStatus = "running" | "done" | "failed";

export interface ConsoleOperation {
  id: number;
  title: string;
  status: OperationStatus;
  message?: string;
  started: Date;
  finished?: Date;
}

interface OperationsValue {
  operations: ConsoleOperation[];
  /** Runs `work` as a tracked operation and returns what it resolves to; a failure rejects after it is recorded. */
  track: <T>(title: string, work: () => Promise<T>, done?: string) => Promise<T>;
  /** Shows a one-off message in the snackbar. */
  notify: (message: string) => void;
}

const OperationsContext = createContext<OperationsValue | null>(null);

interface Snack {
  key: number;
  message: string;
  status?: OperationStatus;
}

export function OperationsProvider({ children }: { children: ReactNode }) {
  const [operations, setOperations] = useState<ConsoleOperation[]>([]);
  const [snack, setSnack] = useState<Snack | null>(null);
  const counter = useRef(0);

  const update = useCallback((id: number, patch: Partial<ConsoleOperation>) => {
    setOperations((current) => current.map((operation) => (operation.id === id ? { ...operation, ...patch } : operation)));
  }, []);

  const track = useCallback(
    async <T,>(title: string, work: () => Promise<T>, done?: string): Promise<T> => {
      counter.current += 1;
      const id = counter.current;
      setOperations((current) => [{ id, title, status: "running", started: new Date() }, ...current]);
      setSnack({ key: id * 2, message: `${title}…`, status: "running" });
      try {
        const result = await work();
        update(id, { status: "done", finished: new Date() });
        setSnack({ key: id * 2 + 1, message: done ?? `${title}: done`, status: "done" });
        return result;
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        update(id, { status: "failed", message, finished: new Date() });
        setSnack({ key: id * 2 + 1, message: `${title} failed: ${message}`, status: "failed" });
        throw error;
      }
    },
    [update],
  );

  const notify = useCallback((message: string) => {
    counter.current += 1;
    setSnack({ key: counter.current * 2, message });
  }, []);

  const dismiss = useCallback(() => setSnack(null), []);
  const value = useMemo(() => ({ operations, track, notify }), [operations, track, notify]);
  return (
    <OperationsContext.Provider value={value}>
      {children}
      {snack ? <Snackbar key={snack.key} snack={snack} onClose={dismiss} /> : null}
    </OperationsContext.Provider>
  );
}

export function useOperations(): OperationsValue {
  const value = useContext(OperationsContext);
  if (!value) throw new Error("useOperations requires an OperationsProvider ancestor");
  return value;
}

const SNACKBAR_SECONDS = 8;

function Snackbar({ snack, onClose }: { snack: Snack; onClose: () => void }) {
  // The real console's snackbar leaves on its own after a few seconds unless
  // it still reports work in progress.
  useEffect(() => {
    if (snack.status === "running") return;
    const timer = window.setTimeout(onClose, SNACKBAR_SECONDS * 1000);
    return () => window.clearTimeout(timer);
  }, [snack, onClose]);
  return (
    <div className="gc-snackbar" role="status" aria-live="polite" data-testid="snackbar">
      {snack.status === "running" ? <span className="gc-spinner gc-spinner-inverse" aria-hidden /> : null}
      <span className="gc-snackbar-text">{snack.message}</span>
      <button type="button" className="gc-snackbar-close" aria-label="Dismiss" onClick={onClose}>
        <Icon name="close" size="1.25em" />
      </button>
    </div>
  );
}

function OperationIcon({ status }: { status: OperationStatus }) {
  if (status === "running") return <span className="gc-spinner" role="img" aria-label="In progress" />;
  if (status === "done") {
    return (
      <span role="img" aria-label="Done" className="gc-status-success">
        <Icon name="check_circle_fill" />
      </span>
    );
  }
  return (
    <span role="img" aria-label="Failed" className="gc-status-error">
      <Icon name="error_fill" />
    </span>
  );
}

/** The top bar's bell: the session's operations, newest first. */
export function NotificationsButton() {
  const { operations } = useOperations();
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useDismiss(wrapper, open, () => setOpen(false), trigger);
  const running = operations.filter((operation) => operation.status === "running").length;
  const label = running > 0 ? `Notifications, ${running} in progress` : "Notifications";
  return (
    <div className="gc-menu-anchor" ref={wrapper}>
      <button
        ref={trigger}
        type="button"
        className="gc-icon-button"
        aria-label={label}
        title="Notifications"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <Icon name="notifications" />
        {running > 0 ? <span className="gc-badge" aria-hidden>{running}</span> : null}
      </button>
      {open ? (
        <div className="gc-menu gc-menu-end gc-notifications" role="dialog" aria-label="Notifications">
          <h2 className="gc-notifications-title">Notifications</h2>
          {operations.length === 0 ? (
            <p className="gc-notifications-empty">No operations in this session yet.</p>
          ) : (
            <ul className="gc-notifications-list">
              {operations.map((operation) => (
                <li key={operation.id} className="gc-notification">
                  <OperationIcon status={operation.status} />
                  <div>
                    <p className="gc-notification-title">{operation.title}</p>
                    <p className="gc-notification-meta">
                      {operation.status === "failed" && operation.message
                        ? operation.message
                        : (operation.finished ?? operation.started).toLocaleTimeString()}
                    </p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
