import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  deleteComputeInstance,
  fetchComputeDisks,
  fetchComputeInstances,
  startComputeInstance,
  stopComputeInstance,
  waitComputeZoneOperation,
  type ComputeDisk,
  type ComputeInstance,
} from "../api.js";
import { useProject } from "../console/project.js";

// Compute Engine reports a resource's zone as a full URL
// ("…/projects/p/zones/us-central1-a"); every zonal call the console makes
// takes the bare zone name, so the console reads it off the resource rather
// than assuming one console-wide zone.
export function instanceZone(instance: ComputeInstance): string {
  return shortName(instance.zone ?? "");
}

// The machineType, network and subnetwork members are likewise full URLs; the
// real console shows their last segment.
const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

const columns: GcpColumn<ComputeInstance>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/compute/${instanceZone(row)}/${row.name}`}>
        {row.name}
      </Link>
    ),
    value: (row) => row.name,
  },
  {
    id: "status",
    header: "Status",
    cell: (row) => <GcpStatus status={row.status ?? "Unknown"} />,
    value: (row) => row.status ?? "",
  },
  { id: "zone", header: "Zone", cell: (row) => instanceZone(row) || "—", value: instanceZone },
  {
    id: "machineType",
    header: "Machine type",
    cell: (row) => lastSegment(row.machineType),
    value: (row) => lastSegment(row.machineType),
  },
  {
    id: "internalIp",
    header: "Internal IP",
    cell: (row) => row.networkInterfaces?.[0]?.networkIP ?? "—",
    value: (row) => row.networkInterfaces?.[0]?.networkIP ?? "",
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.creationTimestamp ?? ""),
    value: (row) => row.creationTimestamp ?? "",
  },
];

// InstanceLifecycleDialog confirms one of the three real zonal lifecycle
// operations — instances.start, instances.stop, instances.delete — and drives
// the Operation it returns to DONE through compute.zoneOperations.get rather
// than assuming the API settled it synchronously. Shared by the list's per-row
// actions and the instance detail page's header actions.
export function InstanceLifecycleDialog({
  project,
  zone,
  name,
  action,
  onClose,
  onDone,
}: {
  project: string;
  zone: string;
  name: string;
  action: "start" | "stop" | "delete";
  onClose: () => void;
  onDone: () => void;
}) {
  const copy = {
    start: {
      title: "Start VM instance?",
      body: "boots the instance and starts billing for it.",
      verb: "Start",
      pending: "Starting…",
      failure: "Couldn't start the instance.",
    },
    stop: {
      title: "Stop VM instance?",
      body: "shuts the guest OS down. The boot disk and internal IP are kept.",
      verb: "Stop",
      pending: "Stopping…",
      failure: "Couldn't stop the instance.",
    },
    delete: {
      title: "Delete VM instance?",
      body: "permanently removes the instance. This can't be undone.",
      verb: "Delete",
      pending: "Deleting…",
      failure: "Couldn't delete the instance.",
    },
  }[action];

  const run = useMutation({
    mutationFn: async () => {
      const operation =
        action === "start"
          ? await startComputeInstance(project, zone, name)
          : action === "stop"
            ? await stopComputeInstance(project, zone, name)
            : await deleteComputeInstance(project, zone, name);
      return waitComputeZoneOperation(project, zone, operation);
    },
    onSuccess: onDone,
  });

  return (
    <GcpDialog title={copy.title} testId={`compute-${action}-dialog`} onClose={onClose}>
      <p>
        {copy.verb === "Delete" ? "Deleting" : `${copy.verb}ing`} <strong>{name}</strong> {copy.body}
      </p>
      {run.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>{copy.failure}</strong>{" "}
          {run.error instanceof Error ? run.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid={`compute-${action}-confirm`}
          disabled={run.isPending}
          onClick={() => run.mutate()}
        >
          {run.isPending ? copy.pending : copy.verb}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ComputeEnginePage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<{ zone: string; name: string; action: "start" | "stop" | "delete" } | null>(
    null,
  );
  const disks = useQuery({ queryKey: ["compute-disks", project], queryFn: () => fetchComputeDisks(project) });

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["compute-instances", project] });

  const columnsWithActions: GcpColumn<ComputeInstance>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const zone = instanceZone(row);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`compute-start-${row.name}`}
              aria-label={`Start ${row.name}`}
              onClick={() => setPending({ zone, name: row.name, action: "start" })}
            >
              Start
            </button>
            <button
              type="button"
              className="gc-button-text"
              data-testid={`compute-stop-${row.name}`}
              aria-label={`Stop ${row.name}`}
              onClick={() => setPending({ zone, name: row.name, action: "stop" })}
            >
              Stop
            </button>
            <button
              type="button"
              className="gc-button-text"
              data-testid={`compute-delete-${row.name}`}
              aria-label={`Delete ${row.name}`}
              onClick={() => setPending({ zone, name: row.name, action: "delete" })}
            >
              Delete
            </button>
          </span>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<ComputeInstance>
        title="VM instances"
        description="Compute Engine lets you create and run virtual machines on Google infrastructure."
        columns={columnsWithActions}
        queryKey={["compute-instances", project]}
        queryFn={() => fetchComputeInstances(project)}
        filterPlaceholder="Filter VM instances"
        resourceNoun="VM instances"
        empty={{
          headline: "Create a VM instance to get started",
          description: "Compute Engine virtual machines run on Google's infrastructure in the zone you choose.",
          primaryLabel: "Create instance",
        }}
        rowKey={(row) => `${instanceZone(row)}/${row.name}`}
      />
      <h2 className="gc-detail-heading">Disks</h2>
      <SubResourceTable<ComputeDisk>
        query={disks}
        testId="compute-disks-list-table"
        noun="disks"
        emptyHeadline="This project has no persistent disks"
        emptyDescription="Boot disks and additional persistent disks appear here."
        rowKey={(row) => `${shortName(row.zone ?? "")}/${row.name}`}
        columns={[
          { header: "Name", cell: (row) => row.name },
          { header: "Status", cell: (row) => <GcpStatus status={row.status ?? "Unknown"} /> },
          { header: "Zone", cell: (row) => shortName(row.zone ?? "") || "—" },
          { header: "Size", cell: (row) => (row.sizeGb ? `${row.sizeGb} GB` : "—") },
          { header: "Type", cell: (row) => shortName(row.type ?? "") || "—" },
          { header: "In use by", cell: (row) => (row.users ?? []).map((user) => shortName(user)).join(", ") || "—" },
        ]}
      />
      {pending ? (
        <InstanceLifecycleDialog
          project={project}
          zone={pending.zone}
          name={pending.name}
          action={pending.action}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}
