/**
 * Loading indicator. Operator UI doesn't get bouncy spinners — instead
 * a sliding monospace bar with a small accent block on top. The bar
 * also surfaces a label so loading states feel intentional, not vague.
 */

export interface SpinnerProps {
  label?: string;
}

export function Spinner({ label = "Loading" }: SpinnerProps) {
  return (
    <div role="status" aria-live="polite" aria-busy="true" className="flex items-center justify-center px-4 py-10">
      <div
        className="flex items-center gap-3 font-mono uppercase tracking-[0.2em]"
        style={{ color: "var(--color-fg-muted)", fontSize: "0.7rem" }}
      >
        <span
          className="block"
          style={{
            width: 24,
            height: 6,
            position: "relative",
            background: "var(--color-bg-subtle)",
            border: "1px solid var(--color-border)",
            overflow: "hidden",
          }}
        >
          <span
            className="absolute inset-y-0 left-0"
            style={{
              width: "33%",
              background: "var(--color-accent)",
              animation: "spinner-slide 1.1s var(--ease-in-out-strong) infinite",
            }}
          />
        </span>
        <span>{label}</span>
      </div>
      <style>{`
        @keyframes spinner-slide {
          0%   { transform: translateX(-100%); }
          50%  { transform: translateX(180%); }
          100% { transform: translateX(-100%); }
        }
      `}</style>
    </div>
  );
}
