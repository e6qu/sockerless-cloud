import { useEffect, useRef, type ReactNode } from "react";

// The console's modal dialog: a scrim over the working area with a white card,
// title at the top, content beneath, and the caller's actions (text buttons,
// primary at the right) at the bottom.
//
// Keyboard operability matches the real console: Escape and a click on the
// scrim dismiss the dialog exactly like its own Cancel button, focus lands on
// the dialog on open so a keyboard or screen-reader user isn't left on the
// page behind it, and closing returns focus to whatever opened it rather than
// dropping it back at the top of the document.
export function GcpDialog({
  title,
  children,
  testId,
  onClose,
}: {
  title: string;
  children: ReactNode;
  testId?: string;
  onClose?: () => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    dialogRef.current?.focus();
    return () => opener?.focus?.();
  }, []);

  useEffect(() => {
    if (!onClose) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  return (
    <div
      className="gc-dialog-overlay"
      role="presentation"
      onMouseDown={(event) => {
        if (onClose && event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className="gc-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        data-testid={testId}
      >
        <h2 className="gc-dialog-title">{title}</h2>
        {children}
      </div>
    </div>
  );
}
