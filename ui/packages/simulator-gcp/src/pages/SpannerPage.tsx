import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  createSpannerInstance,
  deleteSpannerInstance,
  fetchSpannerDatabases,
  fetchSpannerInstance,
  fetchSpannerInstances,
  waitSpannerOperation,
  type SpannerDatabase,
  type SpannerInstance,
} from "../api.js";
import { useProject } from "../console/project.js";

const INSTANCE_ID_PATTERN = /^[a-z](?:[a-z0-9-]{1,28}[a-z0-9])$/;

const columns: GcpColumn<SpannerInstance>[] = [
  {
    id: "name",
    header: "Instance ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/spanner/${shortName(row.name)}`}>
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
  {
    id: "config",
    header: "Configuration",
    cell: (row) => shortName(row.config ?? "") || "—",
    value: (row) => row.config ?? "",
  },
  {
    id: "nodes",
    header: "Nodes",
    cell: (row) => row.nodeCount ?? "—",
    value: (row) => String(row.nodeCount ?? ""),
  },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
];

// CreateSpannerInstanceDialog runs the real projects.instances.create method,
// whose reply is a google.longrunning.Operation named under the instance; the
// console drives it to done through the Spanner operations.get poll.
export function CreateSpannerInstanceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [instanceId, setInstanceId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [nodeCount, setNodeCount] = useState("1");

  const create = useMutation({
    mutationFn: async () =>
      waitSpannerOperation(
        await createSpannerInstance(project, instanceId, { displayName, nodeCount: Number(nodeCount) }),
      ),
    onSuccess: onCreated,
  });

  const valid = INSTANCE_ID_PATTERN.test(instanceId) && displayName.trim().length > 0 && Number(nodeCount) >= 1;

  return (
    <GcpDialog title="Create a Spanner instance" testId="spanner-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Instance ID
        <input
          type="text"
          value={instanceId}
          data-testid="spanner-create-id"
          onChange={(event) => setInstanceId(event.target.value)}
        />
        <p className="gc-field-hint">3–30 lowercase letters, numbers or hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        Instance name
        <input
          type="text"
          value={displayName}
          data-testid="spanner-create-name"
          onChange={(event) => setDisplayName(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Instance configuration
        <input type="text" value={`regional-${CONSOLE_REGION}`} data-testid="spanner-create-config" readOnly />
      </label>
      <label className="gc-field">
        Nodes
        <input
          type="number"
          min={1}
          value={nodeCount}
          data-testid="spanner-create-nodes"
          onChange={(event) => setNodeCount(event.target.value)}
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
          data-testid="spanner-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteSpannerInstanceDialog({
  name,
  onClose,
  onDeleted,
}: {
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deleteSpannerInstance(project, name), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete instance?" testId="spanner-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the instance and every database on it.
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
          data-testid="spanner-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function SpannerPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["spanner-instances", project] });

  const columnsWithActions: GcpColumn<SpannerInstance>[] = [
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
              data-testid={`spanner-delete-${id}`}
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
      <GcpResourceTable<SpannerInstance>
        title="Spanner instances"
        description="Spanner is a fully managed relational database with unlimited scale, strong consistency and up to 99.999% availability."
        actions={[
          { label: "Create instance", icon: "add", primary: true, testId: "spanner-create-instance", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["spanner-instances", project]}
        queryFn={() => fetchSpannerInstances(project)}
        filterPlaceholder="Filter instances"
        resourceNoun="instances"
        empty={{
          headline: "Create a Spanner instance to get started",
          description: "An instance allocates the compute and storage your databases run on.",
          primaryLabel: "Create instance",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateSpannerInstanceDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteSpannerInstanceDialog
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

export function SpannerInstanceDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const instance = useQuery({
    queryKey: ["spanner-instance", project, name],
    queryFn: () => fetchSpannerInstance(project, name),
  });
  const databases = useQuery({
    queryKey: ["spanner-databases", project, name],
    queryFn: () => fetchSpannerDatabases(project, name),
  });

  const data = instance.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/spanner">‹ Spanner instances</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Spanner instance"
        actions={[{ label: "Delete", testId: "spanner-instance-delete", onSelect: () => setDeleting(true) }]}
        onRefresh={() => {
          void instance.refetch();
          void databases.refetch();
        }}
        refreshing={instance.isFetching || databases.isFetching}
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
              id: "databases",
              label: "Databases",
              content: (
                <SubResourceTable<SpannerDatabase>
                  query={databases}
                  testId="spanner-databases-table"
                  noun="databases"
                  emptyHeadline="This instance has no databases"
                  emptyDescription="Databases created on this instance appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Database ID", cell: (row) => shortName(row.name) },
                    { header: "State", cell: (row) => <GcpStatus status={row.state ?? "Unknown"} /> },
                    { header: "Dialect", cell: (row) => row.databaseDialect ?? "—" },
                    { header: "Created", cell: (row) => formatTimestamp(row.createTime ?? "") },
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
                    { label: "Configuration", value: shortName(data.config ?? "") || "—" },
                    { label: "Nodes", value: data.nodeCount ?? "—" },
                    { label: "Processing units", value: data.processingUnits ?? "—" },
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
        <DeleteSpannerInstanceDialog
          name={name}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate("/ui/spanner")}
        />
      ) : null}
    </>
  );
}
