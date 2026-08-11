import { type ButtonHTMLAttributes, forwardRef } from "react";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

/**
 * Sharp, monospace button. Three voices:
 *
 * - primary  — accent fill, bold. Reserved for the one most-important
 *              action on a screen.
 * - secondary — outlined accent. Confident, secondary actions.
 * - ghost    — text-only with hover ground. For low-stakes / nav.
 * - danger   — error-tinted, framed. For destructive ops.
 *
 * Sizing is restrained on purpose — operator UI shouldn't have giant
 * pill buttons.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", className = "", style, children, ...rest },
  ref,
) {
  const sizeStyle =
    size === "sm"
      ? { padding: "0.25rem 0.7rem", fontSize: "0.72rem" }
      : { padding: "0.45rem 0.95rem", fontSize: "0.78rem" };

  const variantStyle: React.CSSProperties = (() => {
    switch (variant) {
      case "primary":
        return {
          background: "var(--color-accent)",
          color: "var(--color-accent-fg)",
          border: "1px solid var(--color-accent)",
        };
      case "secondary":
        return {
          background: "transparent",
          color: "var(--color-fg)",
          border: "1px solid var(--color-border-strong)",
        };
      case "ghost":
        return {
          background: "transparent",
          color: "var(--color-fg-muted)",
          border: "1px solid transparent",
        };
      case "danger":
        return {
          background: "transparent",
          color: "var(--color-status-error)",
          border: "1px solid var(--color-status-error)",
        };
    }
  })();

  return (
    <button
      ref={ref}
      className={`group inline-flex items-center justify-center gap-1.5 font-mono uppercase tracking-[0.08em] ${className}`}
      style={{
        ...variantStyle,
        ...sizeStyle,
        borderRadius: "var(--radius-sm)",
        fontWeight: 500,
        transition:
          "background-color 0.12s var(--ease-out-quint), color 0.12s var(--ease-out-quint), border-color 0.12s var(--ease-out-quint), transform 0.08s var(--ease-out-quint)",
        ...style,
      }}
      {...rest}
    >
      {children}
    </button>
  );
});
