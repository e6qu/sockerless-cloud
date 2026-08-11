import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  createSqlDatabase,
  createSqlInstance,
  deleteSqlInstance,
  fetchSqlDatabases,
  fetchSqlInstance,
  fetchSqlInstances,
  fetchSqlUsers,
  waitSqlOperation,
  type SqlDatabase,
  type SqlInstance,
  type SqlUser,
} from "../api.js";
import { useProject } from "../console/project.js";

// Cloud SQL's instance-name contract: lowercase letters, digits and hyphens,
// starting with a letter, up to 98 characters.
const INSTANCE_ID_PATTERN = /^[a-z](?:[a-z0-9-]{0,96}[a-z0-9])?$/;

// The database versions the real Create instance form offers first, in the
// enum spelling the API takes.
const DATABASE_VERSIONS = [
  "POSTGRES_16",
  "POSTGRES_15",
  "MYSQL_8_0",
  "MYSQL_5_7",
  "SQLSERVER_2022_STANDARD",
] as const;

const columns: GcpColumn<SqlInstance>[] = [
  {
    id: "name",
    header: "Instance ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/sql/${row.name}`}>
        {row.name}
      </Link>
    ),
    value: (row) => row.name,
  },
  {
    id: "state",
    header: "Status",
    cell: (row) => <GcpStatus status={row.state ?? "Unknown"} />,
    value: (row) => row.state ?? "",
  },
  {
    id: "databaseVersion",
    header: "Database version",
    cell: (row) => row.databaseVersion ?? "—",
    value: (row) => row.databaseVersion ?? "",
  },
  { id: "region", header: "Location", cell: (row) => row.region ?? "—", value: (row) => row.region ?? "" },
  {
    id: "publicIp",
    header: "IP address",
    cell: (row) => row.ipAddresses?.[0]?.ipAddress ?? "—",
    value: (row) => row.ipAddresses?.[0]?.ipAddress ?? "",
  },
];

// CreateInstanceDialog runs the real sql.instances.insert method, which
// answers with a sql#operation the console drives to DONE through
// sql.operations.get rather than assuming the instance is ready.
export function CreateInstanceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [name, setName] = useState("");
  const [databaseVersion, setDatabaseVersion] = useState<string>(DATABASE_VERSIONS[0]);
  const [tier, setTier] = useState("db-f1-micro");

  const create = useMutation({
    mutationFn: async () =>
      waitSqlOperation(project, await createSqlInstance(project, { name, databaseVersion, tier })),
    onSuccess: onCreated,
  });

  const valid = INSTANCE_ID_PATTERN.test(name) && tier.trim().length > 0;

  return (
    <GcpDialog title="Create a Cloud SQL instance" testId="sql-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Instance ID
        <input type="text" value={name} data-testid="sql-create-id" onChange={(event) => setName(event.target.value)} />
        <p className="gc-field-hint">Lowercase letters, numbers and hyphens; must start with a letter.</p>
      </label>
      <label className="gc-field">
        Database version
        <select
          value={databaseVersion}
          data-testid="sql-create-version"
          onChange={(event) => setDatabaseVersion(event.target.value)}
        >
          {DATABASE_VERSIONS.map((version) => (
            <option key={version} value={version}>{version}</option>
          ))}
        </select>
      </label>
      <label className="gc-field">
        Region
        <input type="text" value={CONSOLE_REGION} data-testid="sql-create-region" readOnly />
      </label>
      <label className="gc-field">
        Machine tier
        <input type="text" value={tier} data-testid="sql-create-tier" onChange={(event) => setTier(event.target.value)} />
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
          data-testid="sql-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteInstanceDialog({
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
    mutationFn: async () => waitSqlOperation(project, await deleteSqlInstance(project, name)),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete instance?" testId="sql-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{name}</strong> permanently removes the instance, its databases and its backups.
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
          data-testid="sql-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

// CreateDatabaseDialog runs sql.databases.insert on the loaded instance — the
// write the real console's Databases tab offers.
export function CreateDatabaseDialog({
  instance,
  onClose,
  onCreated,
}: {
  instance: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: async () => waitSqlOperation(project, await createSqlDatabase(project, instance, name)),
    onSuccess: onCreated,
  });
  return (
    <GcpDialog title="Create a database" testId="sql-create-db-dialog" onClose={onClose}>
      <label className="gc-field">
        Database name
        <input
          type="text"
          value={name}
          data-testid="sql-create-db-name"
          onChange={(event) => setName(event.target.value)}
        />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the database.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="sql-create-db-submit"
          disabled={name.trim().length === 0 || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudSqlPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["sql-instances", project] });

  const columnsWithActions: GcpColumn<SqlInstance>[] = [
    ...columns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => (
        <span className="gc-row-actions">
          <button
            type="button"
            className="gc-button-text"
            data-testid={`sql-delete-${row.name}`}
            aria-label={`Delete ${row.name}`}
            onClick={() => setDeleting(row.name)}
          >
            Delete
          </button>
        </span>
      ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<SqlInstance>
        title="Cloud SQL instances"
        description="Cloud SQL is a fully managed relational database service for MySQL, PostgreSQL and SQL Server."
        actions={[
          { label: "Create instance", icon: "add", primary: true, testId: "sql-create-instance", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["sql-instances", project]}
        queryFn={() => fetchSqlInstances(project)}
        filterPlaceholder="Filter instances"
        resourceNoun="instances"
        empty={{
          headline: "Create a Cloud SQL instance to get started",
          description: "An instance hosts your databases and the users that connect to them.",
          primaryLabel: "Create instance",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateInstanceDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteInstanceDialog
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

export function CloudSqlInstanceDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [creatingDatabase, setCreatingDatabase] = useState(false);
  const instance = useQuery({
    queryKey: ["sql-instance", project, name],
    queryFn: () => fetchSqlInstance(project, name),
  });
  const databases = useQuery({
    queryKey: ["sql-databases", project, name],
    queryFn: () => fetchSqlDatabases(project, name),
  });
  const users = useQuery({ queryKey: ["sql-users", project, name], queryFn: () => fetchSqlUsers(project, name) });

  const data = instance.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/sql">‹ Cloud SQL instances</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Cloud SQL instance"
        actions={[
          { label: "Create database", icon: "add", testId: "sql-instance-create-db", onSelect: () => setCreatingDatabase(true) },
          { label: "Delete", testId: "sql-instance-delete", onSelect: () => setDeleting(true) },
        ]}
        onRefresh={() => {
          void instance.refetch();
          void databases.refetch();
          void users.refetch();
        }}
        refreshing={instance.isFetching || databases.isFetching || users.isFetching}
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
                <SubResourceTable<SqlDatabase>
                  query={databases}
                  testId="sql-databases-table"
                  noun="databases"
                  emptyHeadline="This instance has no databases"
                  emptyDescription="Databases created on this instance appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Name", cell: (row) => row.name },
                    { header: "Character set", cell: (row) => row.charset ?? "—" },
                    { header: "Collation", cell: (row) => row.collation ?? "—" },
                  ]}
                />
              ),
            },
            {
              id: "users",
              label: "Users",
              content: (
                <SubResourceTable<SqlUser>
                  query={users}
                  testId="sql-users-table"
                  noun="users"
                  emptyHeadline="This instance has no users"
                  emptyDescription="Database users created on this instance appear here."
                  rowKey={(row) => `${row.name}@${row.host ?? ""}`}
                  columns={[
                    { header: "User name", cell: (row) => row.name },
                    { header: "Host", cell: (row) => row.host || "—" },
                    { header: "Type", cell: (row) => row.type ?? "—" },
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
                    { label: "Status", value: <GcpStatus status={data.state ?? "Unknown"} /> },
                    { label: "Database version", value: data.databaseVersion ?? "—" },
                    { label: "Location", value: data.region ?? "—" },
                    { label: "Machine tier", value: data.settings?.tier ?? "—" },
                    { label: "Connection name", value: data.connectionName ?? "—" },
                    { label: "IP address", value: data.ipAddresses?.[0]?.ipAddress ?? "—" },
                    { label: "Created", value: formatTimestamp(data.createTime ?? "") },
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
        <DeleteInstanceDialog name={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/sql")} />
      ) : null}
      {creatingDatabase ? (
        <CreateDatabaseDialog
          instance={name}
          onClose={() => setCreatingDatabase(false)}
          onCreated={() => {
            setCreatingDatabase(false);
            void databases.refetch();
          }}
        />
      ) : null}
    </>
  );
}
