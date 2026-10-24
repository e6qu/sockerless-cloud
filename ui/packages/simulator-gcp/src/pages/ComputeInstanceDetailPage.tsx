import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Copyable, GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { DetailList, LoadError, Loading } from "../console/GcpResourceDetail.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { fetchComputeInstance, type ComputeInstance } from "../api.js";
import { useProject, useProjectPath } from "../console/project.js";
import { InstanceLifecycleDialog, statusLabel, type LifecycleAction } from "./ComputeEnginePage.js";
import { LogsTab } from "./LogsTab.js";

const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

export function NetworkInterfacesTable({ instance }: { instance: ComputeInstance }) {
  const rows = instance.networkInterfaces ?? [];
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="compute-nics-table">
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Network</th>
            <th scope="col">Subnetwork</th>
            <th scope="col">Primary internal IP address</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">No network interfaces</p>
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
            <th scope="col">Device name</th>
            <th scope="col">Name</th>
            <th scope="col">Boot</th>
            <th scope="col">Mode</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">No attached disks</p>
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
  const path = useProjectPath();
  const [pending, setPending] = useState<LifecycleAction | null>(null);
  const instance = useQuery({
    queryKey: ["compute-instance", project, zone, name],
    queryFn: () => fetchComputeInstance(project, zone, name),
  });
  const data = instance.data;

  return (
    <>
      <GcpPageHeader
        backTo="/ui/compute/instances"
        title={name}
        actions={[
          { label: "Start / Resume", icon: "play_arrow", testId: "compute-instance-start", onSelect: () => setPending("start") },
          { label: "Stop", icon: "stop", testId: "compute-instance-stop", onSelect: () => setPending("stop") },
          { label: "Delete", icon: "delete", testId: "compute-instance-delete", onSelect: () => setPending("delete") },
        ]}
        onRefresh={() => void instance.refetch()}
        refreshing={instance.isFetching}
      />
      {instance.isError ? (
        <LoadError error={instance.error} noun="this instance" />
      ) : instance.isLoading || !data ? (
        <Loading noun="instance" />
      ) : (
        <GcpTabs
          label="Instance details"
          tabs={[
            {
              id: "details",
              label: "Details",
              content: (
                <>
                  <h2 className="gc-detail-heading">Basic information</h2>
                  <DetailList
                    properties={[
                      { label: "Name", value: <Copyable value={data.name} /> },
                      { label: "Instance ID", value: data.id ? <Copyable value={data.id} /> : "—" },
                      { label: "Status", value: <GcpStatus status={data.status ?? "UNKNOWN"} label={statusLabel(data.status ?? "UNKNOWN")} /> },
                      { label: "Zone", value: lastSegment(data.zone) },
                      { label: "Creation time", value: formatTimestamp(data.creationTimestamp ?? "") },
                      { label: "Description", value: data.description || "None" },
                      {
                        label: "Labels",
                        value: Object.entries(data.labels ?? {}).length
                          ? Object.entries(data.labels ?? {}).map(([key, value]) => (
                              <span key={key} className="gc-label-chip">
                                {key}: {value}
                              </span>
                            ))
                          : "None",
                      },
                    ]}
                  />
                  <h2 className="gc-detail-heading">Machine configuration</h2>
                  <DetailList
                    properties={[
                      { label: "Machine type", value: lastSegment(data.machineType) },
                      { label: "Network tags", value: (data.tags?.items ?? []).join(", ") || "None" },
                    ]}
                  />
                  <h2 className="gc-detail-heading">Network interfaces</h2>
                  <NetworkInterfacesTable instance={data} />
                  <h2 className="gc-detail-heading">Storage</h2>
                  <DisksTable instance={data} />
                </>
              ),
            },
            {
              id: "logs",
              label: "Logs",
              content: (
                <LogsTab
                  testId="compute-instance-logs"
                  filter={`resource.type="gce_instance" AND resource.labels.instance_id="${data.id ?? ""}"`}
                />
              ),
            },
          ]}
        />
      )}
      {pending ? (
        <InstanceLifecycleDialog
          project={project}
          targets={[{ zone, name }]}
          action={pending}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            if (pending === "delete") navigate(path("/ui/compute/instances"));
            else void instance.refetch();
          }}
        />
      ) : null}
    </>
  );
}
