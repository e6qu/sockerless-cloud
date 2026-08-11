import { useEffect, useRef, type ReactNode } from "react";

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  /** Optional kicker above the title. */
  kicker?: string;
  children: ReactNode;
  /** Footer slot — typically action buttons. */
  footer?: ReactNode;
  /** Width preset; defaults to md (640). */
  size?: "sm" | "md" | "lg" | "xl";
}

const widths: Record<NonNullable<ModalProps["size"]>, number> = {
  sm: 420,
  md: 640,
  lg: 880,
  xl: 1120,
};

/**
 * Editorial-brutalist modal. Uses native <dialog> for focus trapping
 * + ESC dismissal + the platform's accessible-by-default modal
 * semantics. Backdrop click closes; X button closes; ESC closes.
 *
 * Usage:
 *   const [open, setOpen] = useState(false);
 *   ...
 *   <Modal open={open} onClose={() => setOpen(false)} title="Container detail">
 *     <ContainerDetailBody id={...} />
 *   </Modal>
 */
export function Modal({ open, onClose, title, kicker, children, footer, size = "md" }: ModalProps) {
  const ref = useRef<HTMLDialogElement | null>(null);

  // Open / close in sync with the prop.
  useEffect(() => {
    const dlg = ref.current;
    if (!dlg) return;
    if (open && !dlg.open) {
      try {
        dlg.showModal();
      } catch {
        // Fallback for very old jsdom that doesn't ship showModal().
        dlg.setAttribute("open", "");
      }
    } else if (!open && dlg.open) {
      dlg.close();
    }
  }, [open]);

  // Wire native close events (ESC, programmatic close()) back to onClose
  // so the parent state stays in sync.
  useEffect(() => {
    const dlg = ref.current;
    if (!dlg) return;
    const handler = () => onClose();
    dlg.addEventListener("close", handler);
    return () => dlg.removeEventListener("close", handler);
  }, [onClose]);

  // Backdrop click: native <dialog> reports clicks on the backdrop as
  // events whose target equals the dialog element itself (the content
  // is inside a child wrapper).
  const onClick = (e: React.MouseEvent<HTMLDialogElement>) => {
    if (e.target === ref.current) onClose();
  };

  return (
    <dialog
      ref={ref}
      onClick={onClick}
      className="reveal"
      style={{
        background: "var(--color-surface-raised)",
        color: "var(--color-fg)",
        border: "1px solid var(--color-border)",
        borderRadius: "var(--radius-sm)",
        padding: 0,
        width: "min(95vw, " + widths[size] + "px)",
        maxHeight: "90vh",
        boxShadow: "0 24px 60px -10px color-mix(in oklch, var(--color-fg) 45%, transparent)",
      }}
    >
      <div className="flex max-h-[90vh] flex-col">
        <header
          className="flex items-start justify-between gap-4 border-b px-6 py-4"
          style={{ borderColor: "var(--color-border)" }}
        >
          <div className="flex-1">
            {kicker && (
              <div
                className="mb-1 text-[10px] uppercase tracking-[0.22em]"
                style={{ color: "var(--color-fg-subtle)" }}
              >
                {kicker}
              </div>
            )}
            <h2
              className="font-display"
              style={{
                fontStyle: "italic",
                fontWeight: 600,
                fontSize: "1.4rem",
                lineHeight: 1.15,
                letterSpacing: "-0.02em",
                margin: 0,
              }}
            >
              {title}
            </h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="inline-flex h-8 w-8 items-center justify-center"
            style={{
              background: "transparent",
              color: "var(--color-fg-muted)",
              border: "1px solid var(--color-border)",
              borderRadius: "var(--radius-sm)",
              fontSize: 16,
              lineHeight: 1,
            }}
          >
            ×
          </button>
        </header>

        <div className="flex-1 overflow-auto px-6 py-5">{children}</div>

        {footer && (
          <footer
            className="flex items-center justify-end gap-2 border-t px-6 py-3"
            style={{
              borderColor: "var(--color-border)",
              background: "var(--color-bg-subtle)",
            }}
          >
            {footer}
          </footer>
        )}
      </div>
    </dialog>
  );
}
