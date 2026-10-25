import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

// The operations this browser session ran, as the portal's Notifications pane
// lists them: each starts "in progress" and settles to succeeded or failed.
// They live in sessionStorage, so a reload keeps them and a new tab starts
// empty, as the portal's own notifications do for a signed-in session.

export type NotificationStatus = "inProgress" | "succeeded" | "failed";

export interface PortalNotification {
  id: string;
  title: string;
  description: string;
  status: NotificationStatus;
  /** Milliseconds since the epoch. */
  time: number;
  /** A console route the notification opens (a deployment, a resource). */
  link?: string;
  unread: boolean;
}

interface NotificationsApi {
  notifications: PortalNotification[];
  start: (title: string, description: string, link?: string) => string;
  settle: (id: string, status: Exclude<NotificationStatus, "inProgress">, title: string, description: string, link?: string) => void;
  dismiss: (id: string) => void;
  dismissAll: () => void;
  markRead: () => void;
  /** Runs an operation under a notification that settles with its outcome. */
  track: <T>(labels: TrackLabels, operation: () => Promise<T>) => Promise<T>;
}

export interface TrackLabels {
  progress: string;
  success: string;
  failure: string;
  description: string;
  link?: string;
}

const STORAGE_KEY = "azure-portal:notifications";

function load(): PortalNotification[] {
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as PortalNotification[]) : [];
  } catch {
    return [];
  }
}

function save(notifications: PortalNotification[]) {
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(notifications));
  } catch {
    // Storage blocked: the list still lives for this page.
  }
}

const NotificationsContext = createContext<NotificationsApi | null>(null);

export function NotificationsProvider({ children }: { children: ReactNode }) {
  const [notifications, setNotifications] = useState<PortalNotification[]>(load);

  const update = useCallback((change: (current: PortalNotification[]) => PortalNotification[]) => {
    setNotifications((current) => {
      const next = change(current);
      save(next);
      return next;
    });
  }, []);

  const start = useCallback(
    (title: string, description: string, link?: string) => {
      const id = crypto.randomUUID();
      update((current) => [{ id, title, description, status: "inProgress", time: Date.now(), link, unread: true }, ...current]);
      return id;
    },
    [update],
  );

  const settle = useCallback<NotificationsApi["settle"]>(
    (id, status, title, description, link) => {
      update((current) =>
        current.map((entry) =>
          entry.id === id ? { ...entry, status, title, description, link: link ?? entry.link, time: Date.now(), unread: true } : entry,
        ),
      );
    },
    [update],
  );

  const dismiss = useCallback((id: string) => update((current) => current.filter((entry) => entry.id !== id)), [update]);
  const dismissAll = useCallback(() => update(() => []), [update]);
  const markRead = useCallback(
    () => update((current) => (current.some((entry) => entry.unread) ? current.map((entry) => ({ ...entry, unread: false })) : current)),
    [update],
  );

  const track = useCallback(
    async <T,>(labels: TrackLabels, operation: () => Promise<T>): Promise<T> => {
      const id = start(labels.progress, labels.description, labels.link);
      try {
        const result = await operation();
        settle(id, "succeeded", labels.success, labels.description, labels.link);
        return result;
      } catch (error) {
        settle(id, "failed", labels.failure, error instanceof Error ? error.message : String(error), labels.link);
        throw error;
      }
    },
    [start, settle],
  );

  const api = useMemo(
    () => ({ notifications, start, settle, dismiss, dismissAll, markRead, track }),
    [notifications, start, settle, dismiss, dismissAll, markRead, track],
  );
  return <NotificationsContext.Provider value={api}>{children}</NotificationsContext.Provider>;
}

export function useNotifications(): NotificationsApi {
  const api = useContext(NotificationsContext);
  if (!api) throw new Error("useNotifications needs a NotificationsProvider");
  return api;
}
