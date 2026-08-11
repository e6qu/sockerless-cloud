import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { shortName, formatTimestamp } from "../console/format.js";
import {
  createFirestoreDatabase,
  fetchFirestoreDatabases,
  waitArOperation,
  type FirestoreDatabase,
} from "../api.js";
import { useProject } from "../console/project.js";

// Firestore names its first database "(default)"; further databases take an
// operator-chosen ID of lowercase letters, digits and hyphens.
const DATABASE_ID_PATTERN = /^(\(default\)|[a-z](?:[a-z0-9-]{2,61}[a-z0-9]))$/;

const columns: GcpColumn<FirestoreDatabase>[] = [
  { id: "name", header: "Database ID", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
  { id: "type", header: "Database type", cell: (row) => row.type ?? "—", value: (row) => row.type ?? "" },
  { id: "location", header: "Location", cell: (row) => row.locationId ?? "—", value: (row) => row.locationId ?? "" },
  {
    id: "concurrency",
    header: "Concurrency mode",
    cell: (row) => row.concurrencyMode ?? "—",
    value: (row) => row.concurrencyMode ?? "",
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime ?? ""),
    value: (row) => row.createTime ?? "",
  },
];

// CreateDatabaseDialog runs the real projects.databases.create method, a
// long-running operation the console drives to completion through the same v1
// operations.get poll the other v1 collections use.
export function CreateFirestoreDatabaseDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [databaseId, setDatabaseId] = useState("(default)");
  const [type, setType] = useState("FIRESTORE_NATIVE");
  const [locationId, setLocationId] = useState("nam5");

  const create = useMutation({
    mutationFn: async () => waitArOperation(await createFirestoreDatabase(project, databaseId, { type, locationId })),
    onSuccess: onCreated,
  });

  const valid = DATABASE_ID_PATTERN.test(databaseId) && locationId.trim().length > 0;

  return (
    <GcpDialog title="Create a Firestore database" testId="firestore-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Database ID
        <input
          type="text"
          value={databaseId}
          data-testid="firestore-create-id"
          onChange={(event) => setDatabaseId(event.target.value)}
        />
        <p className="gc-field-hint">“(default)” for the project's first database, otherwise 4–63 lowercase characters.</p>
      </label>
      <label className="gc-field">
        Database type
        <select value={type} data-testid="firestore-create-type" onChange={(event) => setType(event.target.value)}>
          <option value="FIRESTORE_NATIVE">Native mode</option>
          <option value="DATASTORE_MODE">Datastore mode</option>
        </select>
      </label>
      <label className="gc-field">
        Location
        <input
          type="text"
          value={locationId}
          data-testid="firestore-create-location"
          onChange={(event) => setLocationId(event.target.value)}
        />
        <p className="gc-field-hint">A Firestore multi-region (nam5, eur3) or region (us-central1).</p>
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
          data-testid="firestore-create-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function FirestorePage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);

  return (
    <>
      <GcpResourceTable<FirestoreDatabase>
        title="Firestore databases"
        description="Firestore is a serverless document database that scales automatically and keeps your data in sync across clients."
        actions={[
          { label: "Create database", icon: "add", primary: true, testId: "firestore-create", onSelect: () => setCreating(true) },
        ]}
        columns={columns}
        queryKey={["firestore-databases", project]}
        queryFn={() => fetchFirestoreDatabases(project)}
        filterPlaceholder="Filter databases"
        resourceNoun="databases"
        empty={{
          headline: "Create a Firestore database to get started",
          description: "A database holds your collections and documents in the location you choose.",
          primaryLabel: "Create database",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateFirestoreDatabaseDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void queryClient.invalidateQueries({ queryKey: ["firestore-databases", project] });
          }}
        />
      ) : null}
    </>
  );
}
