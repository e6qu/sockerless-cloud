import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  deleteComputeDisk,
  deleteComputeInstance,
  fetchComputeDisks,
  fetchComputeInstances,
  startComputeInstance,
  stopComputeInstance,
  waitComputeZoneOperation,
  type ComputeDisk,
  type ComputeInstance,
} from "../api.js";

// Compute Engine names a resource's zone, machine type and network as full
// URLs; the console shows their last segment.
export function instanceZone(instance: ComputeInstance): string {
  return shortName(instance.zone ?? "");
}

const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

export function instancePath(zone: string, name: string): string {
  return `/ui/compute/instancesDetail/zones/${zone}/instances/${name}`;
}

function InstanceName({ instance }: { instance: ComputeInstance }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(instancePath(instanceZone(instance), instance.name))}>
      {instance.name}
    </Link>
  );
}

const columns: GcpColumn<ComputeInstance>[] = [
  {
    id: "status",
    header: "Status",
    cell: (row) => <GcpStatus status={row.status ?? "UNKNOWN"} label={row.status ? statusLabel(row.status) : "Unknown"} />,
    value: (row) => row.status ?? "",
  },
  { id: "name", header: "Name", cell: (row) => <InstanceName instance={row} />, value: (row) => row.name },
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
    id: "network",
    header: "Network",
    cell: (row) => lastSegment(row.networkInterfaces?.[0]?.network),
    value: (row) => lastSegment(row.networkInterfaces?.[0]?.network),
    defaultHidden: true,
  },
  {
    id: "created",
    header: "Creation time",
    cell: (row) => formatTimestamp(row.creationTimestamp ?? ""),
    value: (row) => row.creationTimestamp ?? "",
    defaultHidden: true,
  },
  {
    id: "labels",
    header: "Labels",
    cell: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}: ${value}`).join(", ") || "—",
    value: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}:${value}`).join(" "),
    defaultHidden: true,
  },
];

// The real console names instance states in sentence case.
export function statusLabel(status: string): string {
  return status.charAt(0) + status.slice(1).toLowerCase();
}

export type LifecycleAction = "start" | "stop" | "delete";

export interface InstanceRef {
  zone: string;
  name: string;
}

const COPY: Record<LifecycleAction, { title: string; body: string; verb: string; pending: string; done: string }> = {
  start: {
    title: "Start / Resume",
    body: "Starting boots the instance. It is billed while it runs.",
    verb: "Start",
    pending: "Starting",
    done: "Started",
  },
  stop: {
    title: "Stop",
    body: "Stopping shuts the guest operating system down. The boot disk and internal IP address are kept.",
    verb: "Stop",
    pending: "Stopping",
    done: "Stopped",
  },
  delete: {
    title: "Delete",
    body: "Deleting removes the instance and the boot disk it auto-deletes. This can't be undone.",
    verb: "Delete",
    pending: "Deleting",
    done: "Deleted",
  },
};

// InstanceLifecycleDialog confirms instances.start, instances.stop or
// instances.delete on one or more instances, then drives each returned zonal
// Operation to DONE through zoneOperations.wait as a tracked operation.
export function InstanceLifecycleDialog({
  project,
  targets,
  action,
  onClose,
  onDone,
}: {
  project: string;
  targets: InstanceRef[];
  action: LifecycleAction;
  onClose: () => void;
  onDone: () => void;
}) {
  const copy = COPY[action];
  const { track } = useOperations();
  const names = targets.map((target) => target.name);
  const subject = names.length === 1 ? `instance "${names[0]}"` : `${names.length} instances`;
  const run = useMutation({
    mutationFn: () =>
      track(
        `${copy.verb} VM ${subject}`,
        () =>
          Promise.all(
            targets.map(async ({ zone, name }) => {
              const operation =
                action === "start"
                  ? await startComputeInstance(project, zone, name)
                  : action === "stop"
                    ? await stopComputeInstance(project, zone, name)
                    : await deleteComputeInstance(project, zone, name);
              return waitComputeZoneOperation(project, zone, operation);
            }),
          ),
        `${copy.done} VM ${subject}`,
      ),
    onSuccess: onDone,
  });

  return (
    <GcpDialog title={`${copy.title} ${names.length === 1 ? names[0] : `${names.length} instances`}?`} testId={`compute-${action}-dialog`} onClose={onClose}>
      <p>
        {copy.body}
      </p>
      <ul className="gc-dialog-list">
        {names.map((name) => (
          <li key={name}>{name}</li>
        ))}
      </ul>
      {run.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't {copy.verb.toLowerCase()} {subject}.</strong>{" "}
          {run.error instanceof Error ? run.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className={action === "delete" ? "gc-button-primary gc-button-danger" : "gc-button-primary"}
          data-testid={`compute-${action}-confirm`}
          disabled={run.isPending}
          onClick={() => run.mutate()}
        >
          {run.isPending ? `${copy.pending}…` : copy.verb}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ComputeEnginePage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<{ targets: InstanceRef[]; action: LifecycleAction } | null>(null);
  const refs = (rows: ComputeInstance[]) => rows.map((row) => ({ zone: instanceZone(row), name: row.name }));

  return (
    <>
      <GcpResourceTable<ComputeInstance>
        title="VM instances"
        actions={[{ label: "Create instance", icon: "add", to: "/ui/compute/instancesAdd", testId: "compute-create" }]}
        selectionActions={(selected) => [
          { label: "Start / Resume", icon: "play_arrow", onSelect: () => setPending({ targets: refs(selected), action: "start" }) },
          { label: "Stop", icon: "stop", onSelect: () => setPending({ targets: refs(selected), action: "stop" }) },
          { label: "Delete", icon: "delete", onSelect: () => setPending({ targets: refs(selected), action: "delete" }) },
        ]}
        rowActions={(row) => [
          { label: "Start / Resume", onSelect: () => setPending({ targets: refs([row]), action: "start" }) },
          { label: "Stop", onSelect: () => setPending({ targets: refs([row]), action: "stop" }) },
          { label: "Delete", onSelect: () => setPending({ targets: refs([row]), action: "delete" }) },
        ]}
        columns={columns}
        queryKey={["compute-instances", project]}
        queryFn={() => fetchComputeInstances(project)}
        resourceNoun="VM instances"
        testId="compute-instances-table"
        empty={{
          headline: "No VM instances",
          description: "Compute Engine lets you use virtual machines that run on Google's infrastructure.",
        }}
        rowKey={(row) => `${instanceZone(row)}/${row.name}`}
        rowLabel={(row) => row.name}
      />
      {pending ? (
        <InstanceLifecycleDialog
          project={project}
          targets={pending.targets}
          action={pending.action}
          onClose={() => setPending(null)}
          onDone={() => {
            setPending(null);
            void queryClient.invalidateQueries({ queryKey: ["compute-instances", project] });
          }}
        />
      ) : null}
    </>
  );
}

const diskColumns: GcpColumn<ComputeDisk>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  {
    id: "status",
    header: "Status",
    cell: (row) => <GcpStatus status={row.status ?? "UNKNOWN"} label={row.status ? statusLabel(row.status) : "Unknown"} />,
    value: (row) => row.status ?? "",
  },
  { id: "type", header: "Type", cell: (row) => lastSegment(row.type), value: (row) => lastSegment(row.type) },
  { id: "size", header: "Size", cell: (row) => (row.sizeGb ? `${row.sizeGb} GB` : "—"), value: (row) => row.sizeGb ?? "" },
  { id: "zone", header: "Zone", cell: (row) => lastSegment(row.zone), value: (row) => lastSegment(row.zone) },
  {
    id: "inUseBy",
    header: "In use by",
    cell: (row) => (row.users ?? []).map((user) => shortName(user)).join(", ") || "—",
    value: (row) => (row.users ?? []).map((user) => shortName(user)).join(" "),
  },
  {
    id: "created",
    header: "Creation time",
    cell: (row) => formatTimestamp(row.creationTimestamp ?? ""),
    value: (row) => row.creationTimestamp ?? "",
    defaultHidden: true,
  },
];

function DeleteDisksDialog({ disks, onClose, onDone }: { disks: ComputeDisk[]; onClose: () => void; onDone: () => void }) {
  const { project } = useProject();
  const { track } = useOperations();
  const subject = disks.length === 1 ? `disk "${disks[0].name}"` : `${disks.length} disks`;
  const remove = useMutation({
    mutationFn: () =>
      track(
        `Delete ${subject}`,
        () =>
          Promise.all(
            disks.map(async (disk) => {
              const zone = shortName(disk.zone ?? "");
              return waitComputeZoneOperation(project, zone, await deleteComputeDisk(project, zone, disk.name));
            }),
          ),
        `Deleted ${subject}`,
      ),
    onSuccess: onDone,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="compute-disk-delete-dialog" onClose={onClose}>
      <p>Deleting a disk removes its data permanently. A disk attached to an instance can't be deleted.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary gc-button-danger"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function ComputeDisksPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<ComputeDisk[] | null>(null);
  return (
    <>
      <GcpResourceTable<ComputeDisk>
        title="Disks"
        selectionActions={(selected) => [{ label: "Delete", icon: "delete", onSelect: () => setDeleting(selected) }]}
        columns={diskColumns}
        queryKey={["compute-disks", project]}
        queryFn={() => fetchComputeDisks(project)}
        resourceNoun="disks"
        testId="compute-disks-list-table"
        empty={{
          headline: "No disks",
          description: "Boot disks and additional persistent disks appear here.",
        }}
        rowKey={(row) => `${shortName(row.zone ?? "")}/${row.name}`}
        rowLabel={(row) => row.name}
      />
      {deleting ? (
        <DeleteDisksDialog
          disks={deleting}
          onClose={() => setDeleting(null)}
          onDone={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["compute-disks", project] });
          }}
        />
      ) : null}
    </>
  );
}
