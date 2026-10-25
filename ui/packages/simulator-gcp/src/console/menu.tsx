import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { Icon, type IconName } from "./icons.js";

/** Closes a popup on Escape or on a pointer press outside `container`. */
export function useDismiss(
  container: RefObject<HTMLElement | null>,
  open: boolean,
  onClose: () => void,
  trigger?: RefObject<HTMLElement | null>,
) {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    if (!open) return;
    const onPointer = (event: MouseEvent) => {
      if (container.current && !container.current.contains(event.target as Node)) close.current();
    };
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      close.current();
      trigger?.current?.focus();
    };
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, container, trigger]);
}

/** Arrow-key movement between the menu items of a role="menu" element. */
export function onMenuKeyDown(event: KeyboardEvent<HTMLElement>) {
  const items = Array.from(
    event.currentTarget.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([aria-disabled="true"])'),
  );
  if (items.length === 0) return;
  const index = items.indexOf(document.activeElement as HTMLElement);
  const move = (next: number) => {
    event.preventDefault();
    items[(next + items.length) % items.length].focus();
  };
  if (event.key === "ArrowDown") move(index + 1);
  else if (event.key === "ArrowUp") move(index - 1);
  else if (event.key === "Home") move(0);
  else if (event.key === "End") move(items.length - 1);
}

export interface MenuEntry {
  label: string;
  icon?: IconName;
  onSelect?: () => void;
  href?: string;
  checked?: boolean;
  disabled?: boolean;
  testId?: string;
}

/** An icon or text button opening a menu of actions or links. */
export function MenuButton({
  label,
  icon,
  text,
  entries,
  header,
  align = "end",
  className = "gc-icon-button",
  testId,
}: {
  label: string;
  icon?: IconName;
  text?: ReactNode;
  entries: MenuEntry[];
  header?: ReactNode;
  align?: "start" | "end";
  className?: string;
  testId?: string;
}) {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  useDismiss(wrapper, open, () => setOpen(false), trigger);

  useEffect(() => {
    if (open) menu.current?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus();
  }, [open]);

  const radio = entries.some((entry) => entry.checked !== undefined);
  return (
    <div className="gc-menu-anchor" ref={wrapper}>
      <button
        ref={trigger}
        type="button"
        className={className}
        aria-label={label}
        title={text ? undefined : label}
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid={testId}
        onClick={() => setOpen((value) => !value)}
      >
        {icon ? <Icon name={icon} /> : null}
        {text}
      </button>
      {open ? (
        <div
          ref={menu}
          role="menu"
          aria-label={label}
          className={`gc-menu gc-menu-${align}`}
          onKeyDown={onMenuKeyDown}
        >
          {header}
          {entries.map((entry) => {
            const content = (
              <>
                {radio ? (
                  <Icon name="check" size="1.25em" style={{ visibility: entry.checked ? "visible" : "hidden" }} />
                ) : entry.icon ? (
                  <Icon name={entry.icon} size="1.25em" />
                ) : null}
                <span>{entry.label}</span>
                {entry.href ? <Icon name="open_in_new" size="1em" className="gc-menu-trailing" /> : null}
              </>
            );
            return entry.href ? (
              <a
                key={entry.label}
                role="menuitem"
                tabIndex={-1}
                className="gc-menu-item"
                href={entry.href}
                target="_blank"
                rel="noreferrer"
                onClick={() => setOpen(false)}
              >
                {content}
              </a>
            ) : (
              <button
                key={entry.label}
                type="button"
                role={radio ? "menuitemradio" : "menuitem"}
                data-testid={entry.testId}
                aria-checked={radio ? Boolean(entry.checked) : undefined}
                aria-disabled={entry.disabled || undefined}
                tabIndex={-1}
                className="gc-menu-item"
                onClick={() => {
                  if (entry.disabled) return;
                  setOpen(false);
                  trigger.current?.focus();
                  entry.onSelect?.();
                }}
              >
                {content}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
