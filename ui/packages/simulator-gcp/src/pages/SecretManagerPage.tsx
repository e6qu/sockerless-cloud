import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { Copyable } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpResourceDetail } from "../console/GcpResourceDetail.js";
import { ResourceTableView } from "../console/GcpTable.js";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  accessSecretVersion,
  addSecretVersion,
  changeSecretVersionState,
  deleteSecret,
  fetchSecret,
  fetchSecretVersions,
  fetchSecrets,
  secretRequest,
  sendRequest,
  type SecretResource,
  type SecretVersion,
} from "../api.js";

// Secret Manager IDs are 1–255 characters of letters, digits, hyphens and
// underscores.
const SECRET_ID_PATTERN = /^[A-Za-z0-9_-]{1,255}$/;

const REPLICA_LOCATIONS = ["us-central1", "us-east1", "us-west1", "europe-west1", "europe-west4", "asia-east1"];

// The replication policy is a oneof; the console names the arm in use.
export function replicationLabel(secret: SecretResource): string {
  if (secret.replication?.automatic) return "Automatically replicated";
  const replicas = secret.replication?.userManaged?.replicas;
  if (replicas?.length) return replicas.map((replica) => replica.location).join(", ");
  return "—";
}

function SecretName({ secret }: { secret: SecretResource }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(`/ui/security/secret-manager/secret/${shortName(secret.name)}/versions`)}>
      {shortName(secret.name)}
    </Link>
  );
}

const columns: GcpColumn<SecretResource>[] = [
  { id: "name", header: "Name", cell: (row) => <SecretName secret={row} />, value: (row) => shortName(row.name) },
  { id: "location", header: "Location", cell: replicationLabel, value: replicationLabel },
  { id: "encryption", header: "Encryption", cell: () => "Google-managed", value: () => "Google-managed" },
  {
    id: "labels",
    header: "Labels",
    cell: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}: ${value}`).join(", ") || "None",
    value: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}:${value}`).join(" "),
  },
  { id: "created", header: "Created", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
];

export function DeleteSecretDialog({
  secrets,
  onClose,
  onDeleted,
}: {
  secrets: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const { track } = useOperations();
  const [confirmation, setConfirmation] = useState("");
  const subject = secrets.length === 1 ? `secret "${secrets[0]}"` : `${secrets.length} secrets`;
  const remove = useMutation({
    mutationFn: () =>
      track(`Delete ${subject}`, () => Promise.all(secrets.map((secret) => deleteSecret(project, secret))), `Deleted ${subject}`),
    onSuccess: onDeleted,
  });
  const expected = secrets.length === 1 ? secrets[0] : "DELETE";
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="secrets-delete-dialog" onClose={onClose}>
      <p>Deleting removes the secret and every version of it permanently. Workloads that read it stop working.</p>
      <label className="gc-field">
        <span className="gc-field-label">To confirm, type {secrets.length === 1 ? "the secret name" : "DELETE"}</span>
        <input type="text" value={confirmation} data-testid="secrets-delete-confirmation" onChange={(event) => setConfirmation(event.target.value)} />
      </label>
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
          data-testid="secrets-delete-confirm"
          disabled={confirmation !== expected || remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete secret"}
        </button>
      </div>
    </GcpDialog>
  );
}

// AddSecretVersionDialog runs projects.secrets.addVersion, which takes the
// payload base64-encoded in payload.data.
export function AddSecretVersionDialog({ secret, onClose, onAdded }: { secret: string; onClose: () => void; onAdded: () => void }) {
  const { project } = useProject();
  const { track } = useOperations();
  const [value, setValue] = useState("");
  const add = useMutation({
    mutationFn: () => track(`Add a version to "${secret}"`, () => addSecretVersion(project, secret, value), `Added a version to "${secret}"`),
    onSuccess: onAdded,
  });
  return (
    <GcpDialog title="Add new version" testId="secrets-add-version-dialog" onClose={onClose}>
      <label className="gc-field">
        <span className="gc-field-label">Secret value</span>
        <textarea value={value} rows={4} data-testid="secrets-add-version-value" onChange={(event) => setValue(event.target.value)} />
      </label>
      {add.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't add the version.</strong> {add.error instanceof Error ? add.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
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
  const [deleting, setDeleting] = useState<string[] | null>(null);
  return (
    <>
      <GcpResourceTable<SecretResource>
        title="Secret Manager"
        actions={[{ label: "Create secret", icon: "add", to: "/ui/security/secret-manager/create", testId: "secrets-create" }]}
        selectionActions={(selected) => [
          { label: "Delete", icon: "delete", onSelect: () => setDeleting(selected.map((row) => shortName(row.name))) },
        ]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting([shortName(row.name)]) }]}
        columns={columns}
        queryKey={["secrets", project]}
        queryFn={() => fetchSecrets(project)}
        resourceNoun="secrets"
        testId="secrets-table"
        empty={{ headline: "No secrets", description: "A secret holds versioned payloads your workloads read at runtime." }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteSecretDialog
          secrets={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["secrets", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function SecretCreatePage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [secretId, setSecretId] = useState("");
  const [value, setValue] = useState("");
  const [manual, setManual] = useState(false);
  const [locations, setLocations] = useState<string[]>([]);
  const [labels, setLabels] = useState<LabelPair[]>([]);
  const request = secretRequest(project, { secretId, locations: manual ? locations : [], labels: pairsToLabels(labels) });
  const idError = secretId && !SECRET_ID_PATTERN.test(secretId) ? "Use letters, numbers, hyphens and underscores only." : null;
  const create = useMutation({
    mutationFn: () =>
      track(
        `Create secret "${secretId}"`,
        async () => {
          await sendRequest<SecretResource>(request);
          if (value) await addSecretVersion(project, secretId, value);
        },
        `Created secret "${secretId}"`,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["secrets", project] });
      navigate(path(`/ui/security/secret-manager/secret/${secretId}/versions`));
    },
  });
  return (
    <CreatePage
      title="Create secret"
      backTo="/ui/security/secret-manager"
      submitLabel="Create secret"
      testId="secrets-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(secretId && !idError && (!manual || locations.length > 0))}
      error={create.error}
      errorTitle="Couldn't create the secret."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Secret details">
        <Field label="Name" error={idError}>
          <input type="text" value={secretId} data-testid="secrets-create-id" onChange={(event) => setSecretId(event.target.value)} />
        </Field>
        <Field label="Secret value" hint="The first version's payload. Leave empty to create the secret without a version.">
          <textarea rows={4} value={value} data-testid="secrets-create-value" onChange={(event) => setValue(event.target.value)} />
        </Field>
      </FormSection>
      <FormSection title="Replication policy">
        <label className="gc-checkbox">
          <input type="checkbox" checked={manual} onChange={(event) => setManual(event.target.checked)} />
          Manually manage locations for this secret
        </label>
        {manual ? (
          <fieldset className="gc-checklist">
            <legend className="gc-field-label">Locations</legend>
            {REPLICA_LOCATIONS.map((location) => (
              <label key={location} className="gc-checkbox">
                <input
                  type="checkbox"
                  checked={locations.includes(location)}
                  onChange={(event) =>
                    setLocations((current) =>
                      event.target.checked ? [...current, location] : current.filter((entry) => entry !== location),
                    )
                  }
                />
                {location}
              </label>
            ))}
          </fieldset>
        ) : (
          <p className="gc-field-hint">Google chooses the locations the secret is replicated to.</p>
        )}
      </FormSection>
      <FormSection title="Labels">
        <LabelsEditor pairs={labels} onChange={setLabels} idPrefix="secrets-create" />
      </FormSection>
    </CreatePage>
  );
}

function SecretValueDialog({ secret, version, onClose }: { secret: string; version: string; onClose: () => void }) {
  const { project } = useProject();
  const value = useQuery({
    queryKey: ["secret-value", project, secret, version],
    queryFn: () => accessSecretVersion(project, secret, version),
    gcTime: 0,
  });
  return (
    <GcpDialog title={`Secret value of version ${version}`} testId="secrets-value-dialog" onClose={onClose}>
      {value.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't access the version.</strong> {value.error instanceof Error ? value.error.message : String(value.error)}
        </div>
      ) : value.isLoading ? (
        <p>Loading…</p>
      ) : (
        <pre className="gc-code" data-testid="secrets-value">{value.data}</pre>
      )}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Done
        </button>
      </div>
    </GcpDialog>
  );
}

function VersionsTab({ secret }: { secret: string }) {
  const { project } = useProject();
  const { track } = useOperations();
  const queryClient = useQueryClient();
  const [viewing, setViewing] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const versions = useQuery({ queryKey: ["secret-versions", project, secret], queryFn: () => fetchSecretVersions(project, secret) });
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["secret-versions", project, secret] });
  const change = (version: string, action: "enable" | "disable" | "destroy") =>
    void track(
      `${action.charAt(0).toUpperCase()}${action.slice(1)} version ${version} of "${secret}"`,
      () => changeSecretVersionState(project, secret, version, action),
    ).then(refresh, () => undefined);
  return (
    <>
      <div className="gc-toolbar">
        <button type="button" className="gc-action" data-testid="secrets-detail-add-version" onClick={() => setAdding(true)}>
          New version
        </button>
      </div>
      <ResourceTableView<SecretVersion>
        rows={versions.data}
        isLoading={versions.isLoading}
        error={versions.error}
        tableId="secrets-versions-table"
        resourceNoun="versions"
        rowKey={(row) => row.name}
        rowLabel={(row) => `version ${shortName(row.name)}`}
        columns={[
          { id: "version", header: "Version", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
          {
            id: "status",
            header: "Status",
            cell: (row) => <GcpStatus status={row.state ?? "UNKNOWN"} label={(row.state ?? "Unknown").charAt(0) + (row.state ?? "unknown").slice(1).toLowerCase()} />,
            value: (row) => row.state ?? "",
          },
          { id: "created", header: "Created on", cell: (row) => formatTimestamp(row.createTime ?? ""), value: (row) => row.createTime ?? "" },
          { id: "destroyed", header: "Destroyed on", cell: (row) => (row.destroyTime ? formatTimestamp(row.destroyTime) : "—"), value: (row) => row.destroyTime ?? "" },
        ]}
        rowActions={(row) => {
          const version = shortName(row.name);
          return [
            { label: "View secret value", disabled: row.state !== "ENABLED", onSelect: () => setViewing(version) },
            row.state === "ENABLED"
              ? { label: "Disable", onSelect: () => change(version, "disable") }
              : { label: "Enable", disabled: row.state === "DESTROYED", onSelect: () => change(version, "enable") },
            { label: "Destroy", disabled: row.state === "DESTROYED", onSelect: () => change(version, "destroy") },
          ];
        }}
        empty={{ headline: "No versions", description: "Add a version to store a payload workloads can read." }}
      />
      {viewing ? <SecretValueDialog secret={secret} version={viewing} onClose={() => setViewing(null)} /> : null}
      {adding ? (
        <AddSecretVersionDialog
          secret={secret}
          onClose={() => setAdding(false)}
          onAdded={() => {
            setAdding(false);
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
  const path = useProjectPath();
  const [deleting, setDeleting] = useState(false);
  return (
    <>
      <GcpResourceDetail<SecretResource>
        title={name}
        backTo="/ui/security/secret-manager"
        queryKey={["secret", project, name]}
        queryFn={() => fetchSecret(project, name)}
        actions={[{ label: "Delete", icon: "delete", testId: "secrets-detail-delete", onSelect: () => setDeleting(true) }]}
        properties={(secret) => [
          { label: "Resource ID", value: <Copyable value={secret.name} /> },
          { label: "Location", value: replicationLabel(secret) },
          { label: "Encryption", value: "Google-managed" },
          { label: "Created", value: formatTimestamp(secret.createTime ?? "") },
          {
            label: "Labels",
            value: Object.entries(secret.labels ?? {}).length
              ? Object.entries(secret.labels ?? {}).map(([key, value]) => (
                  <span key={key} className="gc-label-chip">
                    {key}: {value}
                  </span>
                ))
              : "None",
          },
        ]}
        tabs={() => [{ id: "versions", label: "Versions", content: <VersionsTab secret={name} /> }]}
      />
      {deleting ? (
        <DeleteSecretDialog secrets={[name]} onClose={() => setDeleting(false)} onDeleted={() => navigate(path("/ui/security/secret-manager"))} />
      ) : null}
    </>
  );
}
