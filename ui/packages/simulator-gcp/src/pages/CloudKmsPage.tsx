import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  CONSOLE_REGION,
  createKmsCryptoKey,
  createKmsKeyRing,
  fetchKmsCryptoKeyVersions,
  fetchKmsCryptoKeys,
  fetchKmsKeyRing,
  fetchKmsKeyRings,
  type KmsCryptoKey,
  type KmsCryptoKeyVersion,
  type KmsKeyRing,
} from "../api.js";
import { useProject } from "../console/project.js";

// Cloud KMS resource IDs are 1–63 characters of letters, digits, hyphens and
// underscores.
const KMS_ID_PATTERN = /^[A-Za-z0-9_-]{1,63}$/;

// The CryptoKey purposes the real Create key form offers, in the enum spelling
// the API takes.
const PURPOSES = [
  { value: "ENCRYPT_DECRYPT", label: "Symmetric encrypt/decrypt" },
  { value: "ASYMMETRIC_SIGN", label: "Asymmetric sign" },
  { value: "ASYMMETRIC_DECRYPT", label: "Asymmetric decrypt" },
  { value: "MAC", label: "MAC signing/verification" },
] as const;

const columns: GcpColumn<KmsKeyRing>[] = [
  {
    id: "name",
    header: "Key ring",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/kms/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  { id: "location", header: "Location", cell: () => CONSOLE_REGION, value: () => CONSOLE_REGION },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatTimestamp(row.createTime ?? ""),
    value: (row) => row.createTime ?? "",
  },
];

// CreateKeyRingDialog runs the real
// projects.locations.keyRings.create method, which answers synchronously with
// the created KeyRing.
export function CreateKeyRingDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [keyRingId, setKeyRingId] = useState("");
  const create = useMutation({ mutationFn: () => createKmsKeyRing(project, keyRingId), onSuccess: onCreated });
  return (
    <GcpDialog title="Create key ring" testId="kms-create-ring-dialog" onClose={onClose}>
      <label className="gc-field">
        Key ring name
        <input
          type="text"
          value={keyRingId}
          data-testid="kms-create-ring-id"
          onChange={(event) => setKeyRingId(event.target.value)}
        />
        <p className="gc-field-hint">Up to 63 letters, numbers, hyphens or underscores.</p>
      </label>
      <label className="gc-field">
        Location
        <input type="text" value={CONSOLE_REGION} data-testid="kms-create-ring-location" readOnly />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the key ring.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="kms-create-ring-submit"
          disabled={!KMS_ID_PATTERN.test(keyRingId) || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

// CreateCryptoKeyDialog runs the real
// projects.locations.keyRings.cryptoKeys.create method; the reply carries the
// key's first (primary) version, already generated.
export function CreateCryptoKeyDialog({
  keyRing,
  onClose,
  onCreated,
}: {
  keyRing: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [cryptoKeyId, setCryptoKeyId] = useState("");
  const [purpose, setPurpose] = useState<string>(PURPOSES[0].value);
  const create = useMutation({
    mutationFn: () => createKmsCryptoKey(project, keyRing, cryptoKeyId, purpose),
    onSuccess: onCreated,
  });
  return (
    <GcpDialog title="Create key" testId="kms-create-key-dialog" onClose={onClose}>
      <label className="gc-field">
        Key name
        <input
          type="text"
          value={cryptoKeyId}
          data-testid="kms-create-key-id"
          onChange={(event) => setCryptoKeyId(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Purpose
        <select value={purpose} data-testid="kms-create-key-purpose" onChange={(event) => setPurpose(event.target.value)}>
          {PURPOSES.map((candidate) => (
            <option key={candidate.value} value={candidate.value}>{candidate.label}</option>
          ))}
        </select>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the key.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="kms-create-key-submit"
          disabled={!KMS_ID_PATTERN.test(cryptoKeyId) || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudKmsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);

  return (
    <>
      <GcpResourceTable<KmsKeyRing>
        title="Cloud KMS key rings"
        description={`Cloud Key Management Service lets you create and manage cryptographic keys. A key ring groups keys in one location. Showing key rings in ${CONSOLE_REGION}.`}
        actions={[
          { label: "Create key ring", icon: "add", primary: true, testId: "kms-create-ring", onSelect: () => setCreating(true) },
        ]}
        columns={columns}
        queryKey={["kms-keyrings", project]}
        queryFn={() => fetchKmsKeyRings(project)}
        filterPlaceholder="Filter key rings"
        resourceNoun="key rings"
        empty={{
          headline: "Create a key ring to hold your keys",
          description: "A key ring groups keys that share a location, and cannot be deleted once created.",
          primaryLabel: "Create key ring",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateKeyRingDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void queryClient.invalidateQueries({ queryKey: ["kms-keyrings", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function CloudKmsKeyRingDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const [creating, setCreating] = useState(false);
  const keyRing = useQuery({
    queryKey: ["kms-keyring", project, name],
    queryFn: () => fetchKmsKeyRing(project, name),
  });
  const keys = useQuery({
    queryKey: ["kms-cryptokeys", project, name],
    queryFn: () => fetchKmsCryptoKeys(project, name),
  });

  const data = keyRing.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/kms">‹ Cloud KMS key rings</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Cloud KMS key ring"
        actions={[{ label: "Create key", icon: "add", testId: "kms-keyring-create-key", onSelect: () => setCreating(true) }]}
        onRefresh={() => {
          void keyRing.refetch();
          void keys.refetch();
        }}
        refreshing={keyRing.isFetching || keys.isFetching}
      />
      {keyRing.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this key ring.</strong>{" "}
          {keyRing.error instanceof Error ? keyRing.error.message : "The simulator did not respond."}
        </div>
      ) : keyRing.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading key ring…</div>
      ) : (
        <GcpTabs
          label="Key ring detail"
          tabs={[
            {
              id: "keys",
              label: "Keys",
              content: (
                <SubResourceTable<KmsCryptoKey>
                  query={keys}
                  testId="kms-keys-table"
                  noun="keys"
                  emptyHeadline="This key ring has no keys"
                  emptyDescription="Keys created on this key ring appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    {
                      header: "Key name",
                      cell: (row) => (
                        <Link className="gc-cell-link" to={`/ui/kms/${name}/${shortName(row.name)}`}>
                          {shortName(row.name)}
                        </Link>
                      ),
                    },
                    { header: "Purpose", cell: (row) => row.purpose ?? "—" },
                    { header: "Protection level", cell: (row) => row.versionTemplate?.protectionLevel ?? "—" },
                    { header: "Algorithm", cell: (row) => row.versionTemplate?.algorithm ?? "—" },
                    {
                      header: "Primary version",
                      cell: (row) => (row.primary ? shortName(row.primary.name) : "—"),
                    },
                    {
                      header: "Version state",
                      cell: (row) => <GcpStatus status={row.primary?.state ?? "Unknown"} />,
                    },
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
                    { label: "Key ring name", value: data.name },
                    { label: "Location", value: CONSOLE_REGION },
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
      {creating ? (
        <CreateCryptoKeyDialog
          keyRing={name}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void keys.refetch();
          }}
        />
      ) : null}
    </>
  );
}

export function CloudKmsCryptoKeyDetailPage() {
  const { name = "", key = "" } = useParams();
  const { project } = useProject();
  const keys = useQuery({
    queryKey: ["kms-cryptokeys", project, name],
    queryFn: () => fetchKmsCryptoKeys(project, name),
    select: (all: KmsCryptoKey[]) => all.find((candidate) => shortName(candidate.name) === key),
  });
  const versions = useQuery({
    queryKey: ["kms-key-versions", project, name, key],
    queryFn: () => fetchKmsCryptoKeyVersions(project, name, key),
  });

  const data = keys.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to={`/ui/kms/${name}`}>‹ {name}</Link>
      </div>
      <GcpPageHeader
        title={key}
        description="Cloud KMS key"
        onRefresh={() => {
          void keys.refetch();
          void versions.refetch();
        }}
        refreshing={keys.isFetching || versions.isFetching}
      />
      {keys.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this key.</strong>{" "}
          {keys.error instanceof Error ? keys.error.message : "The simulator did not respond."}
        </div>
      ) : keys.isLoading ? (
        <div className="gc-loading" role="status">Loading key…</div>
      ) : !data ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Key {key} was not found on key ring {name}.</strong>
        </div>
      ) : (
        <GcpTabs
          label="Key detail"
          tabs={[
            {
              id: "versions",
              label: "Versions",
              content: (
                <SubResourceTable<KmsCryptoKeyVersion>
                  query={versions}
                  testId="kms-key-versions-table"
                  noun="key versions"
                  emptyHeadline="This key has no versions"
                  emptyDescription="Every rotation of the key adds a version here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Version", cell: (row) => shortName(row.name) },
                    { header: "State", cell: (row) => <GcpStatus status={row.state ?? "Unknown"} /> },
                    { header: "Protection level", cell: (row) => row.protectionLevel ?? "—" },
                    { header: "Algorithm", cell: (row) => row.algorithm ?? "—" },
                    { header: "Generated", cell: (row) => formatTimestamp(row.generateTime ?? "") },
                    { header: "Scheduled for destruction", cell: (row) => formatTimestamp(row.destroyTime ?? "") },
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
                    { label: "Key name", value: data.name },
                    { label: "Purpose", value: data.purpose ?? "—" },
                    { label: "Protection level", value: data.versionTemplate?.protectionLevel ?? "—" },
                    { label: "Algorithm", value: data.versionTemplate?.algorithm ?? "—" },
                    { label: "Primary version", value: data.primary ? shortName(data.primary.name) : "—" },
                    { label: "Rotation period", value: data.rotationPeriod ?? "—" },
                    { label: "Next rotation", value: formatTimestamp(data.nextRotationTime ?? "") },
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
    </>
  );
}
