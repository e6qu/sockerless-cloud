import { Modal } from "./Modal.js";
import { StatusBadge } from "./StatusBadge.js";
import type { ContainerSummary } from "../api/index.js";

export interface ContainerDetailModalProps {
  container: ContainerSummary | null;
  onClose: () => void;
}

/**
 * Operator-facing detail panel for a single container row. Renders
 * inside the shared Modal. Body is a compact two-column grid of all
 * the fields ContainerSummary carries — no calls beyond what was
 * already loaded for the list.
 */
export function ContainerDetailModal({ container, onClose }: ContainerDetailModalProps) {
  return (
    <Modal
      open={container !== null}
      onClose={onClose}
      kicker="container · detail"
      title={container?.name || container?.id?.slice(0, 12) || "Container"}
      size="lg"
    >
      {container && (
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <StatusBadge status={container.state || "unknown"} />
            <span
              className="font-mono"
              style={{ fontSize: "0.78rem", color: "var(--color-fg-muted)" }}
            >
              {container.image}
            </span>
          </div>
          <DLGrid
            rows={[
              ["id", container.id || "—"],
              ["short id", container.id?.slice(0, 12) || "—"],
              ["name", container.name || "—"],
              ["image", container.image || "—"],
              ["state", container.state || "—"],
              ["created", container.created ? new Date(container.created).toLocaleString() : "—"],
              ["pod", container.pod_name || "—"],
            ]}
          />
        </div>
      )}
    </Modal>
  );
}

interface DLGridProps {
  rows: Array<[label: string, value: string]>;
}

function DLGrid({ rows }: DLGridProps) {
  return (
    <dl
      className="grid gap-x-6 gap-y-2 font-mono"
      style={{
        gridTemplateColumns: "8rem 1fr",
        fontSize: "0.82rem",
      }}
    >
      {rows.map(([label, value]) => (
        <div key={label} className="contents">
          <dt
            className="uppercase tracking-[0.12em]"
            style={{ color: "var(--color-fg-subtle)", fontSize: "0.7rem", paddingTop: 2 }}
          >
            {label}
          </dt>
          <dd
            className="break-all"
            style={{ color: "var(--color-fg)", margin: 0 }}
            title={value}
          >
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}
