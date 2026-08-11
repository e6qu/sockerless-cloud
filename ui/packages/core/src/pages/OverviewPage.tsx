import { useStatus, useHealth, useInfo, useCheck } from "../hooks/index.js";
import { MetricsCard } from "../components/MetricsCard.js";
import { PageHeading } from "../components/PageHeading.js";
import { StatusBadge } from "../components/StatusBadge.js";
import { Spinner } from "../components/Spinner.js";
import { BackendInfoCard } from "../components/BackendInfoCard.js";
import { InlineError } from "../components/InlineError.js";
import { Button } from "../components/Button.js";

export function OverviewPage() {
  const { data: status, isLoading: statusLoading, isError, error, refetch } = useStatus();
  const { data: health } = useHealth();
  const { data: info } = useInfo();
  const { data: checks } = useCheck();

  if (statusLoading) return <Spinner label="loading overview" />;

  if (isError) {
    return (
      <div>
        <PageHeading kicker="backend · overview" title={<>System status</>} />
        <InlineError
          title="Failed to load backend status"
          detail={error}
          action={<Button variant="ghost" onClick={() => refetch()}>Retry</Button>}
        />
      </div>
    );
  }

  return (
    <div>
      <PageHeading
        kicker="backend · overview"
        title={<>System status</>}
        meta={
          status
            ? `${status.backend_type} · uptime ${formatUptime(status.uptime_seconds)}`
            : undefined
        }
      />

      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <MetricsCard
          title="Containers"
          value={status?.containers ?? 0}
          emphasized={(status?.containers ?? 0) > 0}
        />
        <MetricsCard title="Active resources" value={status?.active_resources ?? 0} />
        <MetricsCard title="Uptime" value={formatUptime(status?.uptime_seconds ?? 0)} />
        <MetricsCard title="Backend" value={status?.backend_type ?? "—"} />
      </div>

      <div className="grid gap-3 md:grid-cols-2 mb-6">
        {status && <BackendInfoCard status={status} />}

        {info && (
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
              System info
            </div>
            <dl className="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-[13px] font-mono">
              <DLPair label="name" value={info.Name} />
              <DLPair label="version" value={info.ServerVersion} />
              <DLPair label="driver" value={info.Driver} />
              <DLPair
                label="os / arch"
                value={`${info.OperatingSystem} (${info.Architecture})`}
              />
              <DLPair label="images" value={String(info.Images)} />
            </dl>
          </div>
        )}
      </div>

      {checks && checks.checks.length > 0 && (
        <div
          className="px-4 py-4 mb-4"
          style={{
            background: "var(--color-surface)",
            border: "1px solid var(--color-border)",
            borderRadius: "var(--radius-sm)",
          }}
        >
          <div
            className="mb-3 text-[10px] uppercase tracking-[0.22em]"
            style={{ color: "var(--color-fg-subtle)" }}
          >
            Health checks
          </div>
          <ul className="space-y-2 font-mono" style={{ fontSize: "0.78rem" }}>
            {checks.checks.map((c) => (
              <li key={c.name} className="flex items-center gap-2">
                <StatusBadge status={c.status} />
                <span style={{ color: "var(--color-fg)", fontWeight: 500 }}>{c.name}</span>
                {c.detail && (
                  <span style={{ color: "var(--color-fg-subtle)" }}>— {c.detail}</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {health && (
        <p
          className="font-mono text-[11px]"
          style={{ color: "var(--color-fg-subtle)" }}
        >
          health: <StatusBadge status={health.status} /> · component: {health.component}
        </p>
      )}
    </div>
  );
}

function DLPair({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt style={{ color: "var(--color-fg-subtle)" }}>{label}</dt>
      <dd className="truncate" style={{ color: "var(--color-fg)" }} title={value}>
        {value}
      </dd>
    </>
  );
}

function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}
