import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import { createKMSKey, fetchKMSKeys, scheduleKMSKeyDeletion, setKMSKeyEnabled, type KMSKey } from "../api.js";

// AWS Key Management Service (KMS) — Customer managed keys. ListKeys,
// DescribeKey, and ListAliases for the table; CreateKey, EnableKey/DisableKey,
// and ScheduleKeyDeletion for the actions — all on the real KMS API
// (X-Amz-Target TrentService.<Op>).

const columns: AwsColumn<KMSKey>[] = [
  {
    id: "aliases",
    header: "Aliases",
    cell: (row) => row.aliases.join(", ") || "–",
    value: (row) => row.aliases.join(", "),
  },
  { id: "keyId", header: "Key ID", cell: (row) => row.keyId, value: (row) => row.keyId },
  { id: "keyState", header: "Status", cell: (row) => <AwsStatus status={row.keyState} />, value: (row) => row.keyState },
  { id: "keyUsage", header: "Key usage", cell: (row) => row.keyUsage || "–", value: (row) => row.keyUsage },
  { id: "keySpec", header: "Key spec", cell: (row) => row.keySpec || "–", value: (row) => row.keySpec },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  {
    id: "creationDate",
    header: "Created",
    cell: (row) => formatEpoch(row.creationDate),
    value: (row) => String(row.creationDate),
  },
];

// The waiting periods real KMS accepts on ScheduleKeyDeletion: 7 to 30 days.
const MIN_PENDING_WINDOW_DAYS = 7;
const MAX_PENDING_WINDOW_DAYS = 30;

function CreateKeyModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => createKMSKey(description.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["kms-keys"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create key"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="kms-create-key-submit"
            disabled={description.trim().length === 0 || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create key"}
          </AwsButton>
        </>
      }
    >
      <p>A symmetric encryption key for encrypt and decrypt, the default key type KMS creates.</p>
      <FormField label="Description">
        <Input
          value={description}
          onChange={(event) => setDescription(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "kms-key-description-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the key.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function ScheduleDeletionModal({
  keys,
  onClose,
  clearSelection,
}: {
  keys: KMSKey[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const [days, setDays] = useState("30");
  const schedule = useMutation({
    mutationFn: async () => {
      for (const key of keys) {
        await scheduleKMSKeyDeletion(key.keyId, Number(days));
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["kms-keys"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  const parsed = Number(days);
  const valid =
    Number.isInteger(parsed) && parsed >= MIN_PENDING_WINDOW_DAYS && parsed <= MAX_PENDING_WINDOW_DAYS;
  return (
    <AwsModal
      title={keys.length === 1 ? `Schedule deletion of ${keys[0].keyId}?` : `Schedule deletion of ${keys.length} keys?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="kms-schedule-deletion-confirm"
            disabled={!valid || schedule.isPending}
            onClick={() => schedule.mutate()}
          >
            {schedule.isPending ? "Scheduling…" : "Schedule deletion"}
          </AwsButton>
        </>
      }
    >
      <p>
        KMS never deletes a key immediately: it enters the Pending deletion state for the waiting period, and any data
        encrypted under it becomes unrecoverable once the period elapses.
      </p>
      <FormField
        label="Waiting period (days)"
        constraintText={`${MIN_PENDING_WINDOW_DAYS} to ${MAX_PENDING_WINDOW_DAYS} days.`}
      >
        <Input
          type="number"
          value={days}
          onChange={(event) => setDays(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "kms-pending-window-input" }}
        />
      </FormField>
      {schedule.isError && (
        <AwsErrorAlert>
          <strong>Could not schedule the deletion.</strong>{" "}
          {schedule.error instanceof Error ? schedule.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function KMSKeysPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ keys: KMSKey[]; clearSelection: () => void } | null>(null);
  const setEnabled = useMutation({
    mutationFn: async ({ keys, enabled }: { keys: KMSKey[]; enabled: boolean }) => {
      for (const key of keys) {
        await setKMSKeyEnabled(key.keyId, enabled);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["kms-keys"] }),
  });
  return (
    <>
      <AwsResourceTable<KMSKey>
        title="Customer managed keys"
        description="AWS KMS keys in this account and Region."
        columns={columns}
        queryKey={["kms-keys"]}
        queryFn={fetchKMSKeys}
        filterPlaceholder="Find keys"
        emptyTitle="No keys"
        emptyDescription="No AWS KMS keys exist in this account and Region."
        rowKey={(row) => row.keyId}
        tableTestId="kms-table"
        errorTestId="kms-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="kms-enable-key"
              disabled={selected.length === 0 || setEnabled.isPending}
              onClick={() => setEnabled.mutate({ keys: selected, enabled: true })}
            >
              Enable
            </AwsButton>
            <AwsButton
              data-testid="kms-disable-key"
              disabled={selected.length === 0 || setEnabled.isPending}
              onClick={() => setEnabled.mutate({ keys: selected, enabled: false })}
            >
              Disable
            </AwsButton>
            <AwsButton
              data-testid="kms-schedule-deletion"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ keys: selected, clearSelection })}
            >
              Schedule key deletion
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="kms-create-key" onClick={() => setCreating(true)}>
              Create key
            </AwsButton>
          </>
        )}
      />
      {setEnabled.isError && (
        <AwsErrorAlert testId="kms-key-state-error">
          <strong>Could not change the key state.</strong>{" "}
          {setEnabled.error instanceof Error ? setEnabled.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
      {creating && <CreateKeyModal onClose={() => setCreating(false)} />}
      {deleting && (
        <ScheduleDeletionModal keys={deleting.keys} clearSelection={deleting.clearSelection} onClose={() => setDeleting(null)} />
      )}
    </>
  );
}
