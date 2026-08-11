import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Checkbox from "@cloudscape-design/components/checkbox";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import {
  createRDSInstance,
  deleteRDSInstance,
  fetchRDSClusters,
  fetchRDSInstances,
  modifyRDSInstanceAuthentication,
  type RDSCluster,
  type RDSInstance,
} from "../api.js";

// Amazon Relational Database Service (RDS) — Databases. DescribeDBInstances and
// DescribeDBClusters for the tables, DeleteDBInstance for the delete action,
// all on the real RDS Query API (Version 2014-10-31).

const instanceColumns: AwsColumn<RDSInstance>[] = [
  {
    id: "identifier",
    header: "DB identifier",
    cell: (row) => row.dbInstanceIdentifier,
    value: (row) => row.dbInstanceIdentifier,
  },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "engine", header: "Engine", cell: (row) => row.engine, value: (row) => row.engine },
  { id: "engineVersion", header: "Engine version", cell: (row) => row.engineVersion, value: (row) => row.engineVersion },
  { id: "class", header: "Size", cell: (row) => row.dbInstanceClass, value: (row) => row.dbInstanceClass },
  {
    id: "endpoint",
    header: "Endpoint",
    cell: (row) => (row.endpointAddress ? `${row.endpointAddress}:${row.endpointPort}` : "–"),
    value: (row) => row.endpointAddress,
  },
  {
    id: "storage",
    header: "Storage",
    cell: (row) => `${row.allocatedStorage} GiB`,
    value: (row) => String(row.allocatedStorage),
  },
];

function CreateInstanceModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [identifier, setIdentifier] = useState("");
  const [engine, setEngine] = useState<"postgres" | "mysql">("postgres");
  const [instanceClass, setInstanceClass] = useState("db.t3.micro");
  const [storage, setStorage] = useState("20");
  const [database, setDatabase] = useState("application");
  const [username, setUsername] = useState("dbadmin");
  const [password, setPassword] = useState("");
  const [iamAuthentication, setIamAuthentication] = useState(true);
  const valid =
    /^[a-z][a-z0-9-]{0,62}$/.test(identifier) &&
    /^[a-zA-Z][a-zA-Z0-9_]{0,62}$/.test(username) &&
    /^[a-zA-Z][a-zA-Z0-9_]{0,62}$/.test(database) &&
    password.length >= 8 &&
    Number(storage) >= 20;
  const create = useMutation({
    mutationFn: () =>
      createRDSInstance({
        dbInstanceIdentifier: identifier,
        engine,
        dbInstanceClass: instanceClass,
        allocatedStorage: Number(storage),
        masterUsername: username,
        masterUserPassword: password,
        dbName: database,
        enableIAMDatabaseAuthentication: iamAuthentication,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["rds-instances"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create database"
      size="large"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="rds-create-instance-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create database"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <FormField label="Database creation method">
          <strong>Standard create</strong>
          <p>Choose the engine, instance size, storage, and authentication settings.</p>
        </FormField>
        <FormField label="Engine">
          <Select
            selectedOption={{ label: engine === "postgres" ? "PostgreSQL" : "MySQL", value: engine }}
            options={[
              { label: "PostgreSQL", value: "postgres" },
              { label: "MySQL", value: "mysql" },
            ]}
            onChange={(event) => setEngine(event.detail.selectedOption.value as "postgres" | "mysql")}
          />
        </FormField>
        <FormField label="DB instance identifier" constraintText="Lowercase letters, numbers, and hyphens.">
          <Input value={identifier} onChange={(event) => setIdentifier(event.detail.value)} placeholder="production-db" />
        </FormField>
        <FormField label="DB instance class">
          <Input value={instanceClass} onChange={(event) => setInstanceClass(event.detail.value)} />
        </FormField>
        <FormField label="Allocated storage (GiB)">
          <Input value={storage} type="number" onChange={(event) => setStorage(event.detail.value)} />
        </FormField>
        <FormField label="Initial database name">
          <Input value={database} onChange={(event) => setDatabase(event.detail.value)} />
        </FormField>
        <FormField label="Master username">
          <Input value={username} onChange={(event) => setUsername(event.detail.value)} />
        </FormField>
        <FormField label="Master password" constraintText="At least 8 characters. The service stores it encrypted and never returns it.">
          <Input value={password} type="password" onChange={(event) => setPassword(event.detail.value)} />
        </FormField>
        <Checkbox checked={iamAuthentication} onChange={(event) => setIamAuthentication(event.detail.checked)}>
          Enable IAM database authentication
        </Checkbox>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the database.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function ModifyAuthenticationModal({ instance, onClose }: { instance: RDSInstance; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [iamAuthentication, setIamAuthentication] = useState(instance.iamDatabaseAuthenticationEnabled);
  const [password, setPassword] = useState("");
  const valid = password.length === 0 || password.length >= 8;
  const modify = useMutation({
    mutationFn: () =>
      modifyRDSInstanceAuthentication({
        dbInstanceIdentifier: instance.dbInstanceIdentifier,
        enableIAMDatabaseAuthentication: iamAuthentication,
        masterUserPassword: password || undefined,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["rds-instances"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title={`Modify authentication for ${instance.dbInstanceIdentifier}`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="rds-modify-authentication-submit"
            disabled={!valid || modify.isPending}
            onClick={() => modify.mutate()}
          >
            {modify.isPending ? "Applying…" : "Apply immediately"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <Checkbox checked={iamAuthentication} onChange={(event) => setIamAuthentication(event.detail.checked)}>
          Enable IAM database authentication
        </Checkbox>
        <FormField
          label="New master password"
          description="Optional. When supplied, Amazon RDS rotates the live database-engine credential and preserves the database volume."
          constraintText="At least 8 characters."
        >
          <Input value={password} type="password" onChange={(event) => setPassword(event.detail.value)} />
        </FormField>
        {modify.isError && (
          <AwsErrorAlert>{modify.error instanceof Error ? modify.error.message : "The modification failed."}</AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function ConnectInstanceModal({ instance, onClose }: { instance: RDSInstance; onClose: () => void }) {
  const endpoint = `${instance.endpointAddress}:${instance.endpointPort}`;
  const command =
    instance.engine === "postgres"
      ? `psql "host=${instance.endpointAddress} port=${instance.endpointPort} dbname=${instance.dbName} user=${instance.masterUsername} sslmode=require"`
      : `mysql --host=${instance.endpointAddress} --port=${instance.endpointPort} --user=${instance.masterUsername} --ssl-mode=REQUIRED ${instance.dbName}`;
  return (
    <AwsModal title={`Connect to ${instance.dbInstanceIdentifier}`} onDismiss={onClose} footer={<AwsButton onClick={onClose}>Close</AwsButton>}>
      <SpaceBetween size="l">
        <div>
          <h3>Endpoint</h3>
          <code>{endpoint}</code>
        </div>
        <div>
          <h3>Connect with the native client</h3>
          <pre>{command}</pre>
        </div>
        {instance.iamDatabaseAuthenticationEnabled && (
          <div>
            <h3>IAM database authentication</h3>
            <pre>{`aws rds generate-db-auth-token --hostname ${instance.endpointAddress} --port ${instance.endpointPort} --username ${instance.masterUsername}`}</pre>
            <p>Use the generated token as the database password. IAM tokens require TLS and expire after 15 minutes.</p>
          </div>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

const clusterColumns: AwsColumn<RDSCluster>[] = [
  {
    id: "identifier",
    header: "DB identifier",
    cell: (row) => row.dbClusterIdentifier,
    value: (row) => row.dbClusterIdentifier,
  },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "engine", header: "Engine", cell: (row) => row.engine, value: (row) => row.engine },
  { id: "engineVersion", header: "Engine version", cell: (row) => row.engineVersion, value: (row) => row.engineVersion },
  { id: "endpoint", header: "Endpoint", cell: (row) => row.endpoint || "–", value: (row) => row.endpoint },
];

function DeleteInstancesModal({
  instances,
  onClose,
  clearSelection,
}: {
  instances: RDSInstance[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const instance of instances) {
        await deleteRDSInstance(instance.dbInstanceIdentifier);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["rds-instances"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={instances.length === 1 ? `Delete ${instances[0].dbInstanceIdentifier}?` : `Delete ${instances.length} databases?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="rds-delete-instance-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>The delete request skips the final snapshot, so the database and its data are gone for good.</p>
      <ul>
        {instances.map((instance) => (
          <li key={instance.dbInstanceIdentifier}>
            <code>{instance.dbInstanceIdentifier}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function RDSPage() {
  const [deleting, setDeleting] = useState<{ instances: RDSInstance[]; clearSelection: () => void } | null>(null);
  const [creating, setCreating] = useState(false);
  const [connecting, setConnecting] = useState<RDSInstance | null>(null);
  const [modifying, setModifying] = useState<RDSInstance | null>(null);
  return (
    <>
      <AwsResourceTable<RDSInstance>
        title="DB instances"
        description="Amazon RDS database instances in this account and Region."
        columns={instanceColumns}
        queryKey={["rds-instances"]}
        queryFn={fetchRDSInstances}
        filterPlaceholder="Find databases"
        emptyTitle="No DB instances"
        emptyDescription="No Amazon RDS database instances exist in this account and Region."
        rowKey={(row) => row.dbInstanceIdentifier}
        tableTestId="rds-instances-table"
        errorTestId="rds-instances-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="rds-connect-instance"
              disabled={selected.length !== 1 || !selected[0].endpointAddress}
              onClick={() => setConnecting(selected[0])}
            >
              Connect
            </AwsButton>
            <AwsButton
              data-testid="rds-modify-authentication"
              disabled={selected.length !== 1}
              onClick={() => setModifying(selected[0])}
            >
              Modify authentication
            </AwsButton>
            <AwsButton
              data-testid="rds-delete-instance"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ instances: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="rds-create-instance" onClick={() => setCreating(true)}>
              Create database
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<RDSCluster>
        title="DB clusters"
        headingVariant="h2"
        description="Amazon Aurora and Multi-AZ DB clusters in this account and Region."
        columns={clusterColumns}
        queryKey={["rds-clusters"]}
        queryFn={fetchRDSClusters}
        filterPlaceholder="Find clusters"
        emptyTitle="No DB clusters"
        emptyDescription="No Amazon RDS database clusters exist in this account and Region."
        rowKey={(row) => row.dbClusterIdentifier}
        tableTestId="rds-clusters-table"
        errorTestId="rds-clusters-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {deleting && (
        <DeleteInstancesModal
          instances={deleting.instances}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
      {creating && <CreateInstanceModal onClose={() => setCreating(false)} />}
      {connecting && <ConnectInstanceModal instance={connecting} onClose={() => setConnecting(null)} />}
      {modifying && <ModifyAuthenticationModal instance={modifying} onClose={() => setModifying(null)} />}
    </>
  );
}
