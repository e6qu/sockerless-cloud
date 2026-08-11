import { Button } from "./Button.js";

export interface RefreshButtonProps {
  onClick: () => void;
  loading?: boolean;
}

export function RefreshButton({ onClick, loading }: RefreshButtonProps) {
  return (
    <Button
      onClick={onClick}
      disabled={loading}
      variant="secondary"
      size="sm"
      aria-label="Refresh"
    >
      <svg
        className={loading ? "animate-spin" : ""}
        width="11"
        height="11"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={2.4}
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden
      >
        <path d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
      </svg>
      Refresh
    </Button>
  );
}
