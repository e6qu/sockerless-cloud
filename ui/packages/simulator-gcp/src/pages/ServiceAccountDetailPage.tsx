import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpPageHeader, GcpStatus } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { formatTimestamp } from "../console/format.js";
import { serviceAccountKeyId } from "../console/keyfile.js";
import { cloudEndpoint } from "../console/federation.js";
import {
  createServiceAccountKey,
  deleteServiceAccountKey,
  fetchServiceAccount,
  fetchServiceAccountKeys,
  type ServiceAccountKey,
} from "../api.js";
import { ServiceAccountKeyMintedDialog } from "./ServiceAccountKeyMintedDialog.js";
import { useProject } from "../console/project.js";

// ServiceAccountKeysTable is the Keys tab's table, presentational so the
// key-list rendering is testable apart from the queries that feed it.
export function ServiceAccountKeysTable({
  keys,
  onDelete,
}: {
  keys: ServiceAccountKey[];
  onDelete: (keyId: string) => void;
}) {
  return (
    <div className="gc-table-wrap">
      <table className="gc-table" data-testid="sa-keys-table">
        <thead>
          <tr>
            <th>Key ID</th>
            <th>Key creation date</th>
            <th>Key expiration date</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {keys.length === 0 ? (
            <tr>
              <td className="gc-table-state" colSpan={4}>
                <div className="gc-empty">
                  <p className="gc-empty-headline">No keys</p>
                  <p className="gc-empty-description">
                    Keys created for this service account appear here. The private key is
                    downloadable only at creation.
                  </p>
                </div>
              </td>
            </tr>
          ) : (
            keys.map((key) => {
              const keyId = serviceAccountKeyId(key);
              return (
                <tr key={key.name}>
                  <td>{keyId}</td>
                  <td>{key.validAfterTime ? formatTimestamp(key.validAfterTime) : "—"}</td>
                  <td>{key.validBeforeTime ? formatTimestamp(key.validBeforeTime) : "—"}</td>
                  <td>
                    <button
                      type="button"
                      className="gc-button-text"
                      data-testid={`sa-key-delete-${keyId}`}
                      aria-label={`Delete key ${keyId}`}
                      onClick={() => onDelete(keyId)}
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

// CreateKeyDialog mirrors the real console's "Create private key" step: JSON
// is the recommended (and only sim-backed) type; P12 is the legacy option the
// real console lists but discourages.
function CreateKeyDialog({
  email,
  pending,
  error,
  onCreate,
  onClose,
}: {
  email: string;
  pending: boolean;
  error: Error | null;
  onCreate: () => void;
  onClose: () => void;
}) {
  return (
    <GcpDialog title={`Create private key for "${email}"`} testId="sa-key-create-dialog" onClose={onClose}>
      <p>
        Downloads a file that contains the private key. Store the file securely because this key
        can't be recovered if lost.
      </p>
      <label className="gc-radio">
        <input type="radio" name="key-type" checked readOnly />
        <span>
          JSON
          <br />
          <span className="gc-radio-note">Recommended</span>
        </span>
      </label>
      <label className="gc-radio">
        <input type="radio" name="key-type" disabled />
        <span>
          P12
          <br />
          <span className="gc-radio-note">For backward compatibility with code using the P12 format</span>
        </span>
      </label>
      {error ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the key.</strong> {error.message}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="sa-key-create-submit"
          disabled={pending}
          onClick={onCreate}
        >
          Create
        </button>
      </div>
    </GcpDialog>
  );
}

function DeleteKeyDialog({
  keyId,
  pending,
  error,
  onDelete,
  onClose,
}: {
  keyId: string;
  pending: boolean;
  error: Error | null;
  onDelete: () => void;
  onClose: () => void;
}) {
  return (
    <GcpDialog title="Delete service account key?" testId="sa-key-delete-dialog" onClose={onClose}>
      <p>
        Deleting key <strong>{keyId}</strong> immediately revokes it: credentials signed with it
        stop being exchanged for tokens. This cannot be undone.
      </p>
      {error ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the key.</strong> {error.message}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="sa-key-delete-confirm"
          disabled={pending}
          onClick={onDelete}
        >
          Delete
        </button>
      </div>
    </GcpDialog>
  );
}

export function ServiceAccountDetailPage() {
  const { email = "" } = useParams();
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creatingKey, setCreatingKey] = useState(false);
  const [deletingKey, setDeletingKey] = useState<string | null>(null);
  const [mintedKey, setMintedKey] = useState<ServiceAccountKey | null>(null);

  const account = useQuery({ queryKey: ["iam-service-account", project, email], queryFn: () => fetchServiceAccount(project, email) });
  const keysQueryKey = ["iam-service-account-keys", project, email];
  const keys = useQuery({ queryKey: keysQueryKey, queryFn: () => fetchServiceAccountKeys(project, email) });
  // The coordinate the CLI panel names — resolved from the console's own
  // config, the same coordinate every API call on this page already used.
  const endpoint = useQuery({ queryKey: ["cloud-endpoint"], queryFn: cloudEndpoint, staleTime: Number.POSITIVE_INFINITY });

  const refreshKeys = () => void queryClient.invalidateQueries({ queryKey: keysQueryKey });

  const createKey = useMutation({
    mutationFn: () => createServiceAccountKey(project, email),
    onSuccess: (key) => {
      setCreatingKey(false);
      setMintedKey(key);
      refreshKeys();
    },
  });
  const deleteKey = useMutation({
    mutationFn: (keyId: string) => deleteServiceAccountKey(project, email, keyId),
    onSuccess: () => {
      setDeletingKey(null);
      refreshKeys();
    },
  });

  if (account.isError) {
    return (
      <>
        <GcpPageHeader title={email} description="Service account" />
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this service account.</strong>{" "}
          {account.error instanceof Error ? account.error.message : "The simulator did not respond."}
        </div>
      </>
    );
  }

  const data = account.data;
  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/serviceaccounts">‹ Service accounts</Link>
      </div>
      <GcpPageHeader
        title={email}
        description="Service account"
        onRefresh={() => {
          void account.refetch();
          void keys.refetch();
        }}
        refreshing={account.isFetching || keys.isFetching}
      />

      {account.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading service account…</div>
      ) : (
        <>
          <dl className="gc-detail-grid">
            <div className="gc-detail-pair">
              <dt>Email</dt>
              <dd>{data.email}</dd>
            </div>
            <div className="gc-detail-pair">
              <dt>Status</dt>
              <dd><GcpStatus status={data.disabled ? "Disabled" : "Enabled"} /></dd>
            </div>
            <div className="gc-detail-pair">
              <dt>Unique ID</dt>
              <dd>{data.uniqueId ?? "—"}</dd>
            </div>
            <div className="gc-detail-pair">
              <dt>Name</dt>
              <dd>{data.displayName || "—"}</dd>
            </div>
            <div className="gc-detail-pair">
              <dt>Description</dt>
              <dd>{data.description || "—"}</dd>
            </div>
          </dl>

          <div className="gc-page-header-row" style={{ marginTop: 24 }}>
            <h2 className="gc-detail-heading" style={{ margin: 0 }}>Keys</h2>
            <div className="gc-page-actions">
              <button
                type="button"
                className="gc-text-action gc-text-action-primary"
                data-testid="sa-keys-add"
                onClick={() => setCreatingKey(true)}
              >
                Add key
              </button>
            </div>
          </div>
          <p className="gc-page-description">
            A key lets code and tools authenticate as this service account. The private key is
            handed out once, at creation.
          </p>
          {keys.isError ? (
            <div className="gc-message gc-message-error" role="alert">
              <strong>Couldn't load keys.</strong>{" "}
              {keys.error instanceof Error ? keys.error.message : "The simulator did not respond."}
            </div>
          ) : keys.isLoading ? (
            <div className="gc-loading" role="status">Loading keys…</div>
          ) : (
            <ServiceAccountKeysTable keys={keys.data ?? []} onDelete={(keyId) => setDeletingKey(keyId)} />
          )}
        </>
      )}

      {creatingKey ? (
        <CreateKeyDialog
          email={email}
          pending={createKey.isPending}
          error={createKey.error instanceof Error ? createKey.error : null}
          onCreate={() => createKey.mutate()}
          onClose={() => setCreatingKey(false)}
        />
      ) : null}
      {deletingKey ? (
        <DeleteKeyDialog
          keyId={deletingKey}
          pending={deleteKey.isPending}
          error={deleteKey.error instanceof Error ? deleteKey.error : null}
          onDelete={() => deleteKey.mutate(deletingKey)}
          onClose={() => setDeletingKey(null)}
        />
      ) : null}
      {mintedKey ? (
        <ServiceAccountKeyMintedDialog
          project={project}
          saKey={mintedKey}
          endpoint={endpoint.data ?? window.location.origin}
          onClose={() => setMintedKey(null)}
        />
      ) : null}
    </>
  );
}
