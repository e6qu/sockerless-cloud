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
  addSecretVersion,
  createSecret,
  deleteSecret,
  fetchSecret,
  fetchSecretVersions,
  fetchSecrets,
  type SecretResource,
  type SecretVersion,
} from "../api.js";
import { useProject } from "../console/project.js";

// Secret Manager IDs are 1–255 characters of letters, digits, hyphens and
// underscores.
const SECRET_ID_PATTERN = /^[A-Za-z0-9_-]{1,255}$/;

// The replication policy is a oneof; the real console names the arm rather
// than the wrapper.
export function replicationLabel(secret: SecretResource): string {
  if (secret.replication?.automatic) return "Automatic";
  const replicas = secret.replication?.userManaged?.replicas;
  if (replicas?.length) return `User managed (${replicas.map((replica) => replica.location).join(", ")})`;
  return "—";
}

const columns: GcpColumn<SecretResource>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/secrets/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  { id: "replication", header: "Replication", cell: replicationLabel, value: replicationLabel },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime ?? ""),
    value: (row) => row.createTime ?? "",
  },
];

// CreateSecretDialog runs the real projects.secrets.create method
// (POST ?secretId=), sending the automatic replication policy the API
// requires.
export function CreateSecretDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [secretId, setSecretId] = useState("");
  const create = useMutation({ mutationFn: () => createSecret(project, secretId), onSuccess: onCreated });
  return (
    <GcpDialog title="Create secret" testId="secrets-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Name
        <input
          type="text"
          value={secretId}
          data-testid="secrets-create-id"
          onChange={(event) => setSecretId(event.target.value)}
        />
        <p className="gc-field-hint">Up to 255 letters, numbers, hyphens or underscores.</p>
      </label>
      <label className="gc-field">
        Replication policy
        <input type="text" value="Automatic" data-testid="secrets-create-replication" readOnly />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the secret.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="secrets-create-submit"
          disabled={!SECRET_ID_PATTERN.test(secretId) || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create secret"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteSecretDialog({
  secret,
  onClose,
  onDeleted,
}: {
  secret: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deleteSecret(project, secret), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete secret?" testId="secrets-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{secret}</strong> permanently removes the secret and every version of it.
        Workloads reading it stop working.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the secret.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="secrets-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

// AddVersionDialog runs the real projects.secrets.addVersion method, which
// takes the payload base64-encoded in `payload.data`.
export function AddSecretVersionDialog({
  secret,
  onClose,
  onAdded,
}: {
  secret: string;
  onClose: () => void;
  onAdded: () => void;
}) {
  const { project } = useProject();
  const [value, setValue] = useState("");
  const add = useMutation({ mutationFn: () => addSecretVersion(project, secret, value), onSuccess: onAdded });
  return (
    <GcpDialog title="Add new version" testId="secrets-add-version-dialog" onClose={onClose}>
      <label className="gc-field">
        Secret value
        <textarea
          value={value}
          rows={4}
          data-testid="secrets-add-version-value"
          onChange={(event) => setValue(event.target.value)}
        />
        <p className="gc-field-hint">The value is base64-encoded and sent as the version's payload.</p>
      </label>
      {add.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't add the version.</strong>{" "}
          {add.error instanceof Error ? add.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="secrets-add-version-submit"
          disabled={value.length === 0 || add.isPending}
          onClick={() => add.mutate()}
        >
          {add.isPending ? "Adding…" : "Add new version"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function SecretManagerPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["secrets", project] });

  const columnsWithActions: GcpColumn<SecretResource>[] = [
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
              data-testid={`secrets-delete-${id}`}
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
      <GcpResourceTable<SecretResource>
        title="Secret Manager"
        description="Secret Manager stores API keys, passwords and certificates, and versions every change to them."
        actions={[
          { label: "Create secret", icon: "add", primary: true, testId: "secrets-create", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["secrets", project]}
        queryFn={() => fetchSecrets(project)}
        filterPlaceholder="Filter secrets"
        resourceNoun="secrets"
        empty={{
          headline: "Create a secret to store a credential",
          description: "A secret holds versioned payloads your workloads read at runtime.",
          primaryLabel: "Create secret",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateSecretDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteSecretDialog
          secret={deleting}
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

export function SecretDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [adding, setAdding] = useState(false);
  const secret = useQuery({ queryKey: ["secret", project, name], queryFn: () => fetchSecret(project, name) });
  const versions = useQuery({
    queryKey: ["secret-versions", project, name],
    queryFn: () => fetchSecretVersions(project, name),
  });

  const data = secret.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/secrets">‹ Secret Manager</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Secret Manager secret"
        actions={[
          { label: "Add new version", icon: "add", testId: "secrets-detail-add-version", onSelect: () => setAdding(true) },
          { label: "Delete", testId: "secrets-detail-delete", onSelect: () => setDeleting(true) },
        ]}
        onRefresh={() => {
          void secret.refetch();
          void versions.refetch();
        }}
        refreshing={secret.isFetching || versions.isFetching}
      />
      {secret.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this secret.</strong>{" "}
          {secret.error instanceof Error ? secret.error.message : "The simulator did not respond."}
        </div>
      ) : secret.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading secret…</div>
      ) : (
        <GcpTabs
          label="Secret detail"
          tabs={[
            {
              id: "versions",
              label: "Versions",
              content: (
                <SubResourceTable<SecretVersion>
                  query={versions}
                  testId="secrets-versions-table"
                  noun="versions"
                  emptyHeadline="This secret has no versions"
                  emptyDescription="Add a version to store a payload workloads can read."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Version", cell: (row) => shortName(row.name) },
                    { header: "State", cell: (row) => <GcpStatus status={row.state ?? "Unknown"} /> },
                    { header: "Created", cell: (row) => formatTimestamp(row.createTime ?? "") },
                    { header: "Destroyed", cell: (row) => formatTimestamp(row.destroyTime ?? "") },
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
                    { label: "Secret name", value: data.name },
                    { label: "Replication", value: replicationLabel(data) },
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
        <DeleteSecretDialog secret={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/secrets")} />
      ) : null}
      {adding ? (
        <AddSecretVersionDialog
          secret={name}
          onClose={() => setAdding(false)}
          onAdded={() => {
            setAdding(false);
            void versions.refetch();
          }}
        />
      ) : null}
    </>
  );
}
