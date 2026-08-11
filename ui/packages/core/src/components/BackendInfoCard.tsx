import { StatusBadge } from "./StatusBadge.js";
import type { StatusResponse } from "../api/types.js";

export interface BackendInfoCardProps {
  status: StatusResponse;
}

export function BackendInfoCard({ status }: BackendInfoCardProps) {
  return (
    <div
      className="px-4 py-4"
      style={{
        background: "var(--color-surface)",
        border: "1px solid var(--color-border)",
        borderLeft: "3px solid var(--color-accent)",
        borderRadius: "var(--radius-sm)",
      }}
    >
      <div
        className="mb-3 text-[10px] uppercase tracking-[0.22em]"
        style={{ color: "var(--color-fg-subtle)" }}
      >
        Backend
      </div>
      <dl className="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-[13px] font-mono">
        <dt style={{ color: "var(--color-fg-subtle)" }}>type</dt>
        <dd>
          <StatusBadge status={status.backend_type} />
        </dd>
        <dt style={{ color: "var(--color-fg-subtle)" }}>instance</dt>
        <dd
          className="truncate"
          style={{ color: "var(--color-fg)" }}
          title={status.instance_id}
        >
          {status.instance_id}
        </dd>
        {status.context && (
          <>
            <dt style={{ color: "var(--color-fg-subtle)" }}>context</dt>
            <dd style={{ color: "var(--color-fg)" }}>{status.context}</dd>
          </>
        )}
      </dl>
    </div>
  );
}
