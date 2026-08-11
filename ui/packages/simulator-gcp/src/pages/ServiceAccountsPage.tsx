import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import {
  createServiceAccount,
  deleteServiceAccount,
  fetchServiceAccounts,
  type ServiceAccount,
} from "../api.js";
import { useProject } from "../console/project.js";

export const serviceAccountsQueryKey = (project: string) => ["iam-service-accounts", project];

function accountStatus(account: ServiceAccount): string {
  return account.disabled ? "Disabled" : "Enabled";
}

// The IAM API's service-account id contract: 6–30 characters, lowercase
// letters, digits and hyphens, starting with a letter and not ending with a
// hyphen. Checked before submitting so the dialog can explain the rule.
const ACCOUNT_ID_PATTERN = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;

// The console derives the account id from the display name as it is typed,
// the way the real Create service account page fills the ID field.
function deriveAccountId(displayName: string): string {
  return displayName
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 30);
}

export function CreateServiceAccountDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [displayName, setDisplayName] = useState("");
  const [accountId, setAccountId] = useState("");
  const [accountIdEdited, setAccountIdEdited] = useState(false);
  const [description, setDescription] = useState("");

  const create = useMutation({
    mutationFn: () => createServiceAccount(project, accountId, displayName, description),
    onSuccess: onCreated,
  });

  const idValid = ACCOUNT_ID_PATTERN.test(accountId);
  const email = `${accountId || "<id>"}@${project}.iam.gserviceaccount.com`;

  return (
    <GcpDialog title="Create service account" testId="sa-create-dialog" onClose={onClose}>
      <label className="gc-field">
        Service account name
        <input
          type="text"
          value={displayName}
          data-testid="sa-create-name"
          onChange={(event) => {
            setDisplayName(event.target.value);
            if (!accountIdEdited) setAccountId(deriveAccountId(event.target.value));
          }}
        />
        <p className="gc-field-hint">Display name for this service account</p>
      </label>
      <label className="gc-field">
        Service account ID
        <input
          type="text"
          value={accountId}
          data-testid="sa-create-id"
          onChange={(event) => {
            setAccountIdEdited(true);
            setAccountId(event.target.value);
          }}
        />
        <p className="gc-field-hint">
          6–30 lowercase letters, digits or hyphens; must start with a letter
        </p>
      </label>
      <p className="gc-derived-email" data-testid="sa-create-email">Email address: {email}</p>
      <label className="gc-field">
        Service account description
        <textarea
          rows={2}
          value={description}
          data-testid="sa-create-description"
          onChange={(event) => setDescription(event.target.value)}
        />
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the service account.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="sa-create-submit"
          disabled={!idValid || create.isPending}
          onClick={() => create.mutate()}
        >
          Create
        </button>
      </div>
    </GcpDialog>
  );
}

function DeleteServiceAccountDialog({
  email,
  onClose,
  onDeleted,
}: {
  email: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({
    mutationFn: () => deleteServiceAccount(project, email),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete service account?" testId="sa-delete-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{email}</strong> immediately revokes its keys. Applications using this
        service account lose access to your cloud resources.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the service account.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="sa-delete-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          Delete
        </button>
      </div>
    </GcpDialog>
  );
}

export function ServiceAccountsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: serviceAccountsQueryKey(project) });

  const columns: GcpColumn<ServiceAccount>[] = [
    {
      id: "email",
      header: "Email",
      cell: (row) => (
        <Link className="gc-cell-link" data-testid={`sa-row-${row.email}`} to={`/ui/serviceaccounts/${row.email}`}>
          {row.email}
        </Link>
      ),
      value: (row) => row.email,
    },
    {
      id: "status",
      header: "Status",
      cell: (row) => <GcpStatus status={accountStatus(row)} />,
      value: accountStatus,
    },
    { id: "name", header: "Name", cell: (row) => row.displayName || "—", value: (row) => row.displayName ?? "" },
    {
      id: "description",
      header: "Description",
      cell: (row) => row.description || "—",
      value: (row) => row.description ?? "",
    },
    {
      id: "actions",
      header: "Actions",
      cell: (row) => (
        <button
          type="button"
          className="gc-button-text"
          data-testid={`sa-delete-${row.email}`}
          aria-label={`Delete ${row.email}`}
          onClick={() => setDeleting(row.email)}
        >
          Delete
        </button>
      ),
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<ServiceAccount>
        title="Service accounts"
        description={`Service accounts for project "${project}". A service account represents a non-human identity; keys created for it authenticate the Google Cloud CLI and SDKs.`}
        actions={[{ label: "Create service account", icon: "add", primary: true, onSelect: () => setCreating(true) }]}
        columns={columns}
        queryKey={serviceAccountsQueryKey(project)}
        queryFn={() => fetchServiceAccounts(project)}
        filterPlaceholder="Filter service accounts"
        resourceNoun="service accounts"
        empty={{
          headline: "Grant applications access to your project",
          description:
            "A service account is an identity your workloads and tools authenticate as. Create one, then mint a key to use with the Google Cloud CLI.",
          primaryLabel: "Create service account",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.email}
      />
      {creating ? (
        <CreateServiceAccountDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteServiceAccountDialog
          email={deleting}
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
