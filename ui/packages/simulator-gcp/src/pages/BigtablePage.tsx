import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  createBigtableInstance,
  deleteBigtableInstance,
  fetchBigtableClusters,
  fetchBigtableInstance,
  fetchBigtableInstances,
  fetchBigtableTables,
  waitV2Operation,
  type BigtableCluster,
  type BigtableInstance,
  type BigtableTable,
} from "../api.js";
import { useProject } from "../console/project.js";

const INSTANCE_ID_PATTERN = /^[a-z](?:[a-z0-9-]{4,28}[a-z0-9])$/;

// Bigtable clusters are zonal; the console offers the zones of the region it
// is configured for, the same coordinate every other regional read uses.
const ZONES = ["a", "b", "c", "f"].map((suffix) => `${CONSOLE_REGION}-${suffix}`);

const columns: GcpColumn<BigtableInstance>[] = [
  {
    id: "name",
    header: "Instance ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/bigtable/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  {
    id: "displayName",
    header: "Instance name",
    cell: (row) => row.displayName ?? "—",
    value: (row) => row.displayName ?? "",
  },
  { id: "type", header: "Instance type", cell: (row) => row.type ?? "—", value: (row) => row.type ?? "" },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
];

// CreateBigtableInstanceDialog runs the real projects.instances.create method:
// instanceId, the Instance and the initial clusters map all ride in the body,
// and the reply is a long-running Operation the console polls to done.
export function CreateBigtableInstanceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [instanceId, setInstanceId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [zone, setZone] = useState(ZONES[0]);
  const [serveNodes, setServeNodes] = useState("1");

  const create = useMutation({
    mutationFn: async () =>
      waitV2Operation(
        await createBigtableInstance(project, instanceId, { displayName, zone, serveNodes: Number(serveNodes) }),
      ),
    onSuccess: onCreated,
  });

  const valid = INSTANCE_ID_PATTERN.test(instanceId) && displayName.trim().length > 0 && Number(serveNodes) >= 1;

  return (
    <GcpDialog title="Create a Bigtable instance" testId="bigtable-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Instance ID
        <input
          type="text"
          value={instanceId}
          data-testid="bigtable-create-id"
          onChange={(event) => setInstanceId(event.target.value)}
        />
        <p className="gc-field-hint">6–30 lowercase letters, numbers or hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        Instance name
        <input
          type="text"
          value={displayName}
          data-testid="bigtable-create-name"
          onChange={(event) => setDisplayName(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Cluster zone
        <select value={zone} data-testid="bigtable-create-zone" onChange={(event) => setZone(event.target.value)}>
          {ZONES.map((candidate) => (
            <option key={candidate} value={candidate}>{candidate}</option>
          ))}
        </select>
      </label>
      <label className="gc-field">
        Nodes
        <input
          type="number"
          min={1}
          value={serveNodes}
          data-testid="bigtable-create-nodes"
          onChange={(event) => setServeNodes(event.target.value)}
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
          data-testid="bigtable-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteBigtableInstanceDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deleteBigtableInstance(project, name), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete instance?" testId="bigtable-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the instance, its clusters and every table on it.
        This can't be undone.
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
          data-testid="bigtable-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function BigtablePage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["bigtable-instances", project] });

  const columnsWithActions: GcpColumn<BigtableInstance>[] = [
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
              data-testid={`bigtable-delete-${id}`}
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
      <GcpResourceTable<BigtableInstance>
        title="Bigtable instances"
        description="Bigtable is a fully managed, wide-column NoSQL database for large analytical and operational workloads."
        actions={[
          { label: "Create instance", icon: "add", primary: true, testId: "bigtable-create-instance", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["bigtable-instances", project]}
        queryFn={() => fetchBigtableInstances(project)}
        filterPlaceholder="Filter instances"
        resourceNoun="instances"
        empty={{
          headline: "Create a Bigtable instance to get started",
          description: "An instance holds one or more clusters, each serving your tables from a zone.",
          primaryLabel: "Create instance",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateBigtableInstanceDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteBigtableInstanceDialog
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

export function BigtableInstanceDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const instance = useQuery({
    queryKey: ["bigtable-instance", project, name],
    queryFn: () => fetchBigtableInstance(project, name),
  });
  const clusters = useQuery({
    queryKey: ["bigtable-clusters", project, name],
    queryFn: () => fetchBigtableClusters(project, name),
  });
  const tables = useQuery({
    queryKey: ["bigtable-tables", project, name],
    queryFn: () => fetchBigtableTables(project, name),
  });

  const data = instance.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/bigtable">‹ Bigtable instances</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Bigtable instance"
        actions={[{ label: "Delete", testId: "bigtable-instance-delete", onSelect: () => setDeleting(true) }]}
        onRefresh={() => {
          void instance.refetch();
          void clusters.refetch();
          void tables.refetch();
        }}
        refreshing={instance.isFetching || clusters.isFetching || tables.isFetching}
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
              id: "tables",
              label: "Tables",
              content: (
                <SubResourceTable<BigtableTable>
                  query={tables}
                  testId="bigtable-tables-table"
                  noun="tables"
                  emptyHeadline="This instance has no tables"
                  emptyDescription="Tables created on this instance appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Table ID", cell: (row) => shortName(row.name) },
                    { header: "Granularity", cell: (row) => row.granularity ?? "—" },
                    {
                      header: "Replication",
                      cell: (row) =>
                        Object.entries(row.clusterStates ?? {})
                          .map(([cluster, state]) => `${shortName(cluster)}: ${state.replicationState ?? "—"}`)
                          .join(", ") || "—",
                    },
                  ]}
                />
              ),
            },
            {
              id: "clusters",
              label: "Clusters",
              content: (
                <SubResourceTable<BigtableCluster>
                  query={clusters}
                  testId="bigtable-clusters-table"
                  noun="clusters"
                  emptyHeadline="This instance has no clusters"
                  emptyDescription="Clusters serving this instance appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Cluster ID", cell: (row) => shortName(row.name) },
                    { header: "Zone", cell: (row) => shortName(row.location ?? "") || "—" },
                    { header: "Nodes", cell: (row) => row.serveNodes ?? "—" },
                    { header: "Storage type", cell: (row) => row.defaultStorageType ?? "—" },
                    { header: "State", cell: (row) => <GcpStatus status={row.state ?? "Unknown"} /> },
                  ]}
                />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Instance name", value: data.displayName ?? "—" },
                    { label: "Instance type", value: data.type ?? "—" },
                    { label: "State", value: <GcpStatus status={data.state ?? "Unknown"} /> },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteBigtableInstanceDialog
          name={name}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate("/ui/bigtable")}
        />
      ) : null}
    </>
  );
}
