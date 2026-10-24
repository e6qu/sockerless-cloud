import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import Flashbar, { type FlashbarProps } from "@cloudscape-design/components/flashbar";

// The flash notifications the real console stacks above a page's content to
// report the outcome of an operation ("Successfully created role …").

type NotifyKind = "success" | "error" | "info";

interface Notification {
  id: number;
  type: NotifyKind;
  header: string;
  content?: ReactNode;
}

interface Notifier {
  notify: (type: NotifyKind, header: string, content?: ReactNode) => void;
}

const NotifierContext = createContext<Notifier>({ notify: () => undefined });
const ItemsContext = createContext<{ items: Notification[]; dismiss: (id: number) => void }>({
  items: [],
  dismiss: () => undefined,
});

let nextId = 1;

export function NotificationsProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Notification[]>([]);
  const notify = useCallback((type: NotifyKind, header: string, content?: ReactNode) => {
    const id = nextId++;
    setItems((current) => [{ id, type, header, content }, ...current].slice(0, 5));
  }, []);
  const dismiss = useCallback((id: number) => setItems((current) => current.filter((item) => item.id !== id)), []);
  const notifier = useMemo(() => ({ notify }), [notify]);
  const state = useMemo(() => ({ items, dismiss }), [items, dismiss]);
  return (
    <NotifierContext.Provider value={notifier}>
      <ItemsContext.Provider value={state}>{children}</ItemsContext.Provider>
    </NotifierContext.Provider>
  );
}

export function useNotify(): Notifier["notify"] {
  return useContext(NotifierContext).notify;
}

/** The message of a failed operation, as the service reported it. */
export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "The request failed.";
}

export function ConsoleFlashbar() {
  const { items, dismiss } = useContext(ItemsContext);
  const flashItems: FlashbarProps.MessageDefinition[] = items.map((item) => ({
    id: String(item.id),
    type: item.type,
    header: item.header,
    content: item.content,
    dismissible: true,
    dismissLabel: "Dismiss message",
    onDismiss: () => dismiss(item.id),
  }));
  return (
    <Flashbar
      items={flashItems}
      stackItems={flashItems.length > 2}
      i18nStrings={{
        ariaLabel: "Notifications",
        notificationBarText: "Notifications",
        notificationBarAriaLabel: "View all notifications",
        errorIconAriaLabel: "Error",
        successIconAriaLabel: "Success",
        infoIconAriaLabel: "Info",
        warningIconAriaLabel: "Warning",
        inProgressIconAriaLabel: "In progress",
      }}
    />
  );
}
