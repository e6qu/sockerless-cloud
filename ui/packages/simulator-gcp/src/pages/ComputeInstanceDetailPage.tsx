import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { fetchComputeInstance, type ComputeInstance } from "../api.js";
import { useProject } from "../console/project.js";
import { InstanceLifecycleDialog } from "./ComputeEnginePage.js";

const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

// NetworkInterfacesTable and DisksTable are the detail page's two sub-resource
// tables, presentational so their rendering is testable apart from the query
// that loads the instance.
export function NetworkInterfacesTable({ instance }: { instance: ComputeInstance }) {
  const rows = instance.networkInterfaces ?? [];
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="compute-nics-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Network</th>
            <th>Subnetwork</th>
            <th>Internal IP</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">This instance has no network interfaces</p>
                  <p className="gc-empty-description">Interfaces attached to the instance appear here.</p>
                </div>
              </td>
            </tr>
          ) : (
            rows.map((nic, index) => (
              <tr key={nic.name ?? `nic${index}`}>
                <td>{nic.name ?? `nic${index}`}</td>
                <td>{lastSegment(nic.network)}</td>
                <td>{lastSegment(nic.subnetwork)}</td>
                <td>{nic.networkIP ?? "—"}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

export function DisksTable({ instance }: { instance: ComputeInstance }) {
  const rows = instance.disks ?? [];
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="compute-disks-table">
        <thead>
          <tr>
            <th>Device name</th>
            <th>Source</th>
            <th>Boot</th>
            <th>Mode</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">This instance has no attached disks</p>
                  <p className="gc-empty-description">Disks attached to the instance appear here.</p>
                </div>
              </td>
            </tr>
          ) : (
            rows.map((disk, index) => (
              <tr key={disk.deviceName ?? `disk${index}`}>
                <td>{disk.deviceName ?? "—"}</td>
                <td>{lastSegment(disk.source)}</td>
                <td>{disk.boot ? "Yes" : "No"}</td>
                <td>{disk.mode ?? "—"}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

export function ComputeInstanceDetailPage() {
  const { zone = "", name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [pending, setPending] = useState<"start" | "stop" | "delete" | null>(null);
  const instance = useQuery({
    queryKey: ["compute-instance", project, zone, name],
    queryFn: () => fetchComputeInstance(project, zone, name),
  });

  // The lifecycle actions address the instance by the zone and name in the
  // route, not the loaded resource, so they render (and are operable) before
  // the read settles — the same way the list pages' "Create …" header action
  // doesn't wait on a successful list read.
  const actions = [
    { label: "Start", icon: "play_arrow" as const, testId: "compute-instance-start", onSelect: () => setPending("start") },
    { label: "Stop", icon: "block" as const, testId: "compute-instance-stop", onSelect: () => setPending("stop") },
    { label: "Delete", testId: "compute-instance-delete", onSelect: () => setPending("delete") },
  ];

  const data = instance.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/compute">‹ VM instances</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Compute Engine VM instance"
        actions={actions}
        onRefresh={() => void instance.refetch()}
        refreshing={instance.isFetching}
      />

      {instance.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this instance.</strong>{" "}
          {instance.error instanceof Error ? instance.error.message : "The simulator did not respond."}
        </div>
      ) : instance.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading instance…</div>
      ) : (
        <GcpTabs
          label="Instance detail"
          tabs={[
            {
              id: "details",
              label: "Details",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Status", value: <GcpStatus status={data.status ?? "Unknown"} /> },
                    { label: "Zone", value: lastSegment(data.zone) },
                    { label: "Machine type", value: lastSegment(data.machineType) },
                    { label: "Instance ID", value: data.id ?? "—" },
                    { label: "Created", value: formatTimestamp(data.creationTimestamp ?? "") },
                    { label: "Network tags", value: (data.tags?.items ?? []).join(", ") || "—" },
                    { label: "Description", value: data.description || "—" },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
            { id: "network", label: "Network interfaces", content: <NetworkInterfacesTable instance={data} /> },
            { id: "disks", label: "Disks", content: <DisksTable instance={data} /> },
          ]}
        />
      )}
      {pending ? (
        <InstanceLifecycleDialog
          project={project}
          zone={zone}
          name={name}
          action={pending}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            if (pending === "delete") navigate("/ui/compute");
            else void instance.refetch();
          }}
        />
      ) : null}
    </>
  );
}
