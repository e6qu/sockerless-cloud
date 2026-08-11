import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import {
  CONSOLE_REGION,
  createRedisInstance,
  deleteRedisInstance,
  fetchRedisInstance,
  fetchRedisInstances,
  waitArOperation,
  type RedisInstance,
} from "../api.js";
import { useProject } from "../console/project.js";

const INSTANCE_ID_PATTERN = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/;

const columns: GcpColumn<RedisInstance>[] = [
  {
    id: "name",
    header: "Instance ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/memorystore/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
  { id: "tier", header: "Tier", cell: (row) => row.tier ?? "—", value: (row) => row.tier ?? "" },
  {
    id: "capacity",
    header: "Capacity",
    cell: (row) => (row.memorySizeGb ? `${row.memorySizeGb} GB` : "—"),
    value: (row) => String(row.memorySizeGb ?? ""),
  },
  {
    id: "version",
    header: "Version",
    cell: (row) => row.redisVersion ?? "—",
    value: (row) => row.redisVersion ?? "",
  },
  {
    id: "endpoint",
    header: "Primary endpoint",
    cell: (row) => (row.host ? `${row.host}:${row.port ?? 6379}` : "—"),
    value: (row) => row.host ?? "",
  },
];

// CreateRedisInstanceDialog runs the real
// projects.locations.instances.create method (POST ?instanceId=), a
// long-running operation driven to done through the v1 operations.get poll.
export function CreateRedisInstanceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [instanceId, setInstanceId] = useState("");
  const [tier, setTier] = useState("BASIC");
  const [memorySizeGb, setMemorySizeGb] = useState("1");

  const create = useMutation({
    mutationFn: async () =>
      waitArOperation(await createRedisInstance(project, instanceId, { tier, memorySizeGb: Number(memorySizeGb) })),
    onSuccess: onCreated,
  });

  const valid = INSTANCE_ID_PATTERN.test(instanceId) && Number(memorySizeGb) >= 1;

  return (
    <GcpDialog title="Create a Redis instance" testId="memorystore-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Instance ID
        <input
          type="text"
          value={instanceId}
          data-testid="memorystore-create-id"
          onChange={(event) => setInstanceId(event.target.value)}
        />
        <p className="gc-field-hint">Up to 40 lowercase letters, numbers or hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        Tier
        <select value={tier} data-testid="memorystore-create-tier" onChange={(event) => setTier(event.target.value)}>
          <option value="BASIC">Basic</option>
          <option value="STANDARD_HA">Standard (highly available)</option>
        </select>
      </label>
      <label className="gc-field">
        Region
        <input type="text" value={CONSOLE_REGION} data-testid="memorystore-create-region" readOnly />
      </label>
      <label className="gc-field">
        Capacity (GB)
        <input
          type="number"
          min={1}
          value={memorySizeGb}
          data-testid="memorystore-create-capacity"
          onChange={(event) => setMemorySizeGb(event.target.value)}
        />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the instance.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="memorystore-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteRedisInstanceDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({
    mutationFn: async () => waitArOperation(await deleteRedisInstance(project, name)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete instance?" testId="memorystore-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the instance and the data it holds. This can't be undone.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the instance.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="memorystore-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function MemorystorePage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["redis-instances", project] });

  const columnsWithActions: GcpColumn<RedisInstance>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = shortName(row.name);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`memorystore-delete-${id}`}
              aria-label={`Delete ${id}`}
              onClick={() => setDeleting(id)}
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
      <GcpResourceTable<RedisInstance>
        title="Memorystore for Redis"
        description={`Memorystore for Redis is a fully managed in-memory data store. Showing instances in ${CONSOLE_REGION}.`}
        actions={[
          { label: "Create instance", icon: "add", primary: true, testId: "memorystore-create-instance", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["redis-instances", project]}
        queryFn={() => fetchRedisInstances(project)}
        filterPlaceholder="Filter instances"
        resourceNoun="instances"
        empty={{
          headline: "Create a Redis instance to get started",
          description: "An instance gives your workloads a managed, in-memory Redis endpoint.",
          primaryLabel: "Create instance",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateRedisInstanceDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteRedisInstanceDialog
          name={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}

export function MemorystoreInstanceDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const instance = useQuery({
    queryKey: ["redis-instance", project, name],
    queryFn: () => fetchRedisInstance(project, name),
  });
  const data = instance.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/memorystore">‹ Memorystore for Redis</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Memorystore for Redis instance"
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
        <dl className="gc-detail-grid">
          {[
            { label: "Status", value: <GcpStatus status={data.state ?? "Unknown"} /> },
            { label: "Tier", value: data.tier ?? "—" },
            { label: "Capacity", value: data.memorySizeGb ? `${data.memorySizeGb} GB` : "—" },
            { label: "Version", value: data.redisVersion ?? "—" },
            { label: "Primary endpoint", value: data.host ? `${data.host}:${data.port ?? 6379}` : "—" },
            { label: "Created", value: formatTimestamp(data.createTime ?? "") },
          ].map((property) => (
            <div className="gc-detail-pair" key={property.label}>
              <dt>{property.label}</dt>
              <dd>{property.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </>
  );
}
