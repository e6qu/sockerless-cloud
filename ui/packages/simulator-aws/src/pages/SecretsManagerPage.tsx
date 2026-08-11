import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createSecret,
  deleteSecret,
  fetchSecrets,
  removeSecretReplica,
  replicateSecret,
  type Secret,
} from "../api.js";

// AWS Secrets Manager — Secrets. ListSecrets, CreateSecret, and DeleteSecret on
// the real Secrets Manager API (X-Amz-Target secretsmanager.<Op>).

const columns: AwsColumn<Secret>[] = [
  { id: "name", header: "Secret name", cell: (row) => row.name, value: (row) => row.name },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  {
    id: "replication",
    header: "Replication",
    cell: (row) =>
      row.primaryRegion
        ? `Replica of ${row.primaryRegion}`
        : row.replicationStatus.length > 0
          ? row.replicationStatus.map((replica) => `${replica.region} (${replica.status})`).join(", ")
          : "Not replicated",
    value: (row) =>
      row.primaryRegion || row.replicationStatus.map((replica) => `${replica.region} ${replica.status}`).join(" "),
  },
  {
    id: "rotationEnabled",
    header: "Rotation",
    cell: (row) => (row.rotationEnabled ? "Enabled" : "Disabled"),
    value: (row) => (row.rotationEnabled ? "Enabled" : "Disabled"),
  },
  {
    id: "lastChangedDate",
    header: "Last changed",
    cell: (row) => formatEpoch(row.lastChangedDate),
    value: (row) => String(row.lastChangedDate),
  },
  {
    id: "createdDate",
    header: "Created",
    cell: (row) => formatEpoch(row.createdDate),
    value: (row) => String(row.createdDate),
  },
];

function ReplicateSecretModal({ secret, onClose }: { secret: Secret; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [region, setRegion] = useState("");
  const existing = secret.replicationStatus.find((replica) => replica.region === region.trim());
  const mutate = useMutation({
    mutationFn: () =>
      existing
        ? removeSecretReplica(secret.arn || secret.name, region.trim())
        : replicateSecret(secret.arn || secret.name, region.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["secrets"] });
      onClose();
    },
  });
  const valid = /^[a-z]{2}(?:-gov)?-[a-z]+-\d$/.test(region.trim()) && !secret.primaryRegion;
  return (
    <AwsModal
      title={`Manage replication for ${secret.name}`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="secrets-replication-submit"
            disabled={!valid || mutate.isPending}
            onClick={() => mutate.mutate()}
          >
            {mutate.isPending ? "Updating…" : existing ? "Remove replica" : "Replicate"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        {secret.primaryRegion ? (
          <AwsErrorAlert>This secret is a replica of the primary secret in {secret.primaryRegion}.</AwsErrorAlert>
        ) : (
          <>
            <p>
              Secrets Manager copies every version, tag, rotation setting, and resource policy to the destination
              Region and keeps subsequent changes in sync.
            </p>
            <FormField
              label="Destination Region"
              description={
                existing
                  ? `${region.trim()} is currently ${existing.status}. Submitting removes that replica.`
                  : "Enter an AWS Region such as us-west-2."
              }
            >
              <Input
                value={region}
                onChange={(event) => setRegion(event.detail.value)}
                placeholder="us-west-2"
                nativeInputAttributes={{ "data-testid": "secrets-replication-region" }}
              />
            </FormField>
          </>
        )}
        {mutate.isError && (
          <AwsErrorAlert>
            <strong>Could not update replication.</strong>{" "}
            {mutate.error instanceof Error ? mutate.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function CreateSecretModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [secretValue, setSecretValue] = useState("");
  const create = useMutation({
    mutationFn: () => createSecret(name.trim(), secretValue, description.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["secrets"] });
      onClose();
    },
  });
  const valid = name.trim().length > 0 && secretValue.length > 0;
  return (
    <AwsModal
      title="Store a new secret"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="secrets-create-secret-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Storing…" : "Store"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>The secret value is encrypted with the service-managed key unless a customer managed key is chosen later.</p>
        <FormField label="Secret name">
          <Input
            value={name}
            onChange={(event) => setName(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "secrets-secret-name-input" }}
          />
        </FormField>
        <FormField label="Secret value">
          <Input
            type="password"
            value={secretValue}
            onChange={(event) => setSecretValue(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "secrets-secret-value-input" }}
          />
        </FormField>
        <FormField label="Description - optional">
          <Input
            value={description}
            onChange={(event) => setDescription(event.detail.value)}
            nativeInputAttributes={{ "data-testid": "secrets-secret-description-input" }}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not store the secret.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function DeleteSecretsModal({
  secrets,
  onClose,
  clearSelection,
}: {
  secrets: Secret[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const secret of secrets) {
        await deleteSecret(secret.arn || secret.name);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["secrets"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={secrets.length === 1 ? `Delete ${secrets[0].name}?` : `Delete ${secrets.length} secrets?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="secrets-delete-secret-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Scheduling…" : "Schedule deletion"}
          </AwsButton>
        </>
      }
    >
      <p>Secrets Manager schedules the deletion after its recovery window rather than deleting immediately, so the secret can be restored until then.</p>
      <ul>
        {secrets.map((secret) => (
          <li key={secret.arn || secret.name}>
            <code>{secret.name}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not schedule the deletion.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function SecretsManagerPage() {
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ secrets: Secret[]; clearSelection: () => void } | null>(null);
  const [replicating, setReplicating] = useState<Secret | null>(null);
  return (
    <>
      <AwsResourceTable<Secret>
        title="Secrets"
        description="Secrets Manager secrets in this account and Region."
        columns={columns}
        queryKey={["secrets"]}
        queryFn={fetchSecrets}
        filterPlaceholder="Find secrets"
        emptyTitle="No secrets"
        emptyDescription="No Secrets Manager secrets exist in this account and Region."
        rowKey={(row) => row.arn || row.name}
        tableTestId="secrets-table"
        errorTestId="secrets-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="secrets-manage-replication"
              disabled={selected.length !== 1}
              onClick={() => setReplicating(selected[0])}
            >
              Manage replication
            </AwsButton>
            <AwsButton
              data-testid="secrets-delete-secret"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ secrets: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="secrets-create-secret" onClick={() => setCreating(true)}>
              Store a new secret
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateSecretModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteSecretsModal
          secrets={deleting.secrets}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
      {replicating && <ReplicateSecretModal secret={replicating} onClose={() => setReplicating(null)} />}
    </>
  );
}
