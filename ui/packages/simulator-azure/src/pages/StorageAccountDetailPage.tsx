import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  makeStyles,
  tokens,
  Field,
  Select,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  Button,
  Text,
} from "@fluentui/react-components";
import {
  AzureCommandBar,
  AzureConfirmDialog,
  AzureEssentials,
  AzureStatus,
  AzureErrorMessage,
  AzureEmptyState,
  TagsEditor,
} from "../portal/index.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import { resourceGroupOf, locationLabel, tagsSummary } from "../portal/format.js";
import {
  deleteStorageAccount,
  fetchStorageAccount,
  fetchBlobContainers,
  fetchBlobs,
  storageAccessTiers,
  storageAccountSkus,
  updateResourceTags,
  updateStorageAccount,
  type StorageAccountDetail,
  API_VERSION,
} from "../api.js";

const useStyles = makeStyles({
  form: {
    backgroundColor: tokens.colorNeutralBackground1,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: "14px 16px",
    margin: "12px 0",
    display: "flex",
    flexDirection: "column",
    gap: "10px",
    maxWidth: "480px",
  },
  actions: { display: "flex", gap: "8px" },
});

// StorageAccountConfigForm is the account "Configuration" blade: the
// redundancy (sku.name) and access tier the real Microsoft.Storage PATCH edits.
export function StorageAccountConfigForm({
  account,
  busy,
  error,
  onSave,
  onDismiss,
}: {
  account: StorageAccountDetail;
  busy: boolean;
  error?: React.ReactNode;
  onSave: (skuName: string, accessTier: string) => void;
  onDismiss: () => void;
}) {
  const styles = useStyles();
  const [skuName, setSkuName] = useState(account.skuName || storageAccountSkus[0]);
  const [accessTier, setAccessTier] = useState(account.accessTier || storageAccessTiers[0]);
  return (
    <form
      className={styles.form}
      data-testid="storage-config-form"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(skuName, accessTier);
      }}
    >
      <Text as="h2" weight="semibold">
        Configuration
      </Text>
      <Field label="Redundancy">
        <Select data-testid="storage-config-sku" value={skuName} onChange={(event) => setSkuName(event.target.value)}>
          {storageAccountSkus.map((sku) => (
            <option key={sku} value={sku}>
              {sku}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Access tier">
        <Select
          data-testid="storage-config-tier"
          value={accessTier}
          onChange={(event) => setAccessTier(event.target.value)}
        >
          {storageAccessTiers.map((tier) => (
            <option key={tier} value={tier}>
              {tier}
            </option>
          ))}
        </Select>
      </Field>
      {error ? <AzureErrorMessage testid="storage-config-error">{error}</AzureErrorMessage> : null}
      <div className={styles.actions}>
        <Button type="submit" appearance="primary" data-testid="storage-config-save" disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// The Storage account blade: the real Microsoft.Storage Essentials, and its
// blob containers/blobs read from the account's own blob data plane —
// authenticated with an account SAS minted through the real
// `ListAccountSas` ARM action, the same credential path the portal's own
// Storage Browser uses.
export function StorageAccountDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [selectedContainer, setSelectedContainer] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [panel, setPanel] = useState<"config" | "tags" | null>(null);

  const account = useQuery({ queryKey: ["storage-account", name], queryFn: () => fetchStorageAccount(name) });
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["storage-account", name] });
    void queryClient.invalidateQueries({ queryKey: ["storage-accounts"] });
  };
  const remove = useMutation({
    mutationFn: () => deleteStorageAccount(account.data!.id),
    onSuccess: () => navigate("/ui/storage"),
  });
  const update = useMutation({
    mutationFn: ({ skuName, accessTier }: { skuName: string; accessTier: string }) =>
      updateStorageAccount(account.data!.id, { skuName, accessTier }),
    onSuccess: () => {
      setPanel(null);
      invalidate();
    },
  });
  const saveTags = useMutation({
    mutationFn: (tags: Record<string, string>) => updateResourceTags(account.data!.id, API_VERSION.storage, tags),
    onSuccess: () => {
      setPanel(null);
      invalidate();
    },
  });
  const containers = useQuery({
    queryKey: ["storage-containers", account.data?.id],
    queryFn: () => fetchBlobContainers(account.data!),
    enabled: Boolean(account.data),
  });
  const blobs = useQuery({
    queryKey: ["storage-blobs", account.data?.id, selectedContainer],
    queryFn: () => fetchBlobs(account.data!, selectedContainer!),
    enabled: Boolean(account.data) && Boolean(selectedContainer),
  });

  return (
    <>
      <AzureCommandBar
        commands={[
          {
            label: "Configuration",
            icon: "settings",
            testid: "storage-account-config",
            disabled: !account.data || update.isPending,
            onSelect: () => setPanel((current) => (current === "config" ? null : "config")),
          },
          {
            label: "Edit tags",
            icon: "tag",
            testid: "storage-account-tags",
            disabled: !account.data || saveTags.isPending,
            onSelect: () => setPanel((current) => (current === "tags" ? null : "tags")),
          },
          {
            label: "Delete",
            icon: "delete",
            testid: "storage-account-delete",
            disabled: !account.data || remove.isPending,
            onSelect: () => setDeleting(true),
          },
          {
            label: "Refresh",
            icon: "refresh",
            onSelect: () => {
              void account.refetch();
              void containers.refetch();
              if (selectedContainer) void blobs.refetch();
            },
            disabled: account.isFetching,
          },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      {account.data ? (
        <AzureConfirmDialog
          open={deleting}
          title={`Delete ${account.data.name}?`}
          busy={remove.isPending}
          testid="storage-account-delete-dialog"
          error={
            remove.isError ? (
              <>
                <strong>Could not delete.</strong>{" "}
                {remove.error instanceof Error ? remove.error.message : "Azure Resource Manager did not respond."}
              </>
            ) : undefined
          }
          onConfirm={() => remove.mutate()}
          onCancel={() => setDeleting(false)}
        >
          <Text as="p">
            Deleting a storage account is permanent and removes every container, blob, file share, table, and queue
            it holds. This action can&rsquo;t be undone.
          </Text>
        </AzureConfirmDialog>
      ) : null}
      <div className="az-main" data-testid="storage-account-detail">
        {account.isError ? (
          <AzureErrorMessage testid="storage-account-error">
            <strong>Could not load this storage account.</strong>{" "}
            {account.error instanceof Error ? account.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : account.isLoading || !account.data ? (
          <AzureEmptyState title="Loading the storage account…" loading />
        ) : (
          <>
            <AzureEssentials
              properties={[
                { label: "Resource group", value: resourceGroupOf(account.data.id) },
                { label: "Location", value: locationLabel(account.data.location) },
                { label: "Kind", value: account.data.kind || "—" },
                { label: "Replication", value: account.data.skuName || "—" },
                { label: "Access tier", value: account.data.accessTier || "—" },
                { label: "Provisioning state", value: <AzureStatus status={account.data.provisioningState || "Unknown"} /> },
                { label: "Blob service endpoint", value: account.data.blobEndpoint ? <code>{account.data.blobEndpoint}</code> : "—" },
                { label: "Tags", value: tagsSummary(account.data.tags) },
              ]}
            />

            {panel === "config" ? (
              <StorageAccountConfigForm
                account={account.data}
                busy={update.isPending}
                error={
                  update.isError ? (
                    <>
                      <strong>The storage account could not be updated.</strong>{" "}
                      {update.error instanceof Error ? update.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(skuName, accessTier) => update.mutate({ skuName, accessTier })}
                onDismiss={() => setPanel(null)}
              />
            ) : null}
            {panel === "tags" ? (
              <TagsEditor
                tags={account.data.tags}
                busy={saveTags.isPending}
                testidPrefix="storage-account-tags"
                error={
                  saveTags.isError ? (
                    <>
                      <strong>The tags could not be saved.</strong>{" "}
                      {saveTags.error instanceof Error ? saveTags.error.message : "Azure Resource Manager did not respond."}
                    </>
                  ) : undefined
                }
                onSave={(tags) => saveTags.mutate(tags)}
                onDismiss={() => setPanel(null)}
              />
            ) : null}

            <section className="az-blade-section" aria-label="Containers">
              <Text as="h2" weight="semibold" block>
                Containers
              </Text>
              <Table aria-label="Containers" size="small" data-testid="storage-containers">
                <TableHeader>
                  <TableRow>
                    <TableHeaderCell>Name</TableHeaderCell>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {containers.isError ? (
                    <AzureTableErrorRow colSpan={1}>
                      <strong>Could not reach the blob service.</strong>{" "}
                      {containers.error instanceof Error ? containers.error.message : "The storage account did not respond."}
                    </AzureTableErrorRow>
                  ) : containers.isLoading ? (
                    <AzureTableLoadingRow colSpan={1} label="Loading containers…" />
                  ) : (containers.data ?? []).length === 0 ? (
                    <AzureTableEmptyRow
                      colSpan={1}
                      title="No containers to display"
                      description="Blob containers created in this account appear here."
                    />
                  ) : (
                    (containers.data ?? []).map((container) => (
                      <TableRow key={container.name} appearance={selectedContainer === container.name ? "neutral" : undefined}>
                        <TableCell>
                          <Button
                            appearance="subtle"
                            data-testid="storage-container-row"
                            aria-pressed={selectedContainer === container.name}
                            onClick={() => setSelectedContainer((current) => (current === container.name ? null : container.name))}
                          >
                            {container.name}
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </section>

            {selectedContainer ? (
              <section className="az-blade-section" aria-label={`Blobs in ${selectedContainer}`}>
                <Text as="h2" weight="semibold" block>
                  Blobs — {selectedContainer}
                </Text>
                <Table aria-label={`Blobs in ${selectedContainer}`} size="small" data-testid="storage-blobs">
                  <TableHeader>
                    <TableRow>
                      <TableHeaderCell>Name</TableHeaderCell>
                      <TableHeaderCell>Size</TableHeaderCell>
                      <TableHeaderCell>Last modified</TableHeaderCell>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {blobs.isError ? (
                      <AzureTableErrorRow colSpan={3}>
                        <strong>Could not load blobs.</strong>{" "}
                        {blobs.error instanceof Error ? blobs.error.message : "The storage account did not respond."}
                      </AzureTableErrorRow>
                    ) : blobs.isLoading ? (
                      <AzureTableLoadingRow colSpan={3} label="Loading blobs…" />
                    ) : (blobs.data ?? []).length === 0 ? (
                      <AzureTableEmptyRow colSpan={3} title="No blobs to display" />
                    ) : (
                      (blobs.data ?? []).map((blob) => (
                        <TableRow key={blob.name}>
                          <TableCell>{blob.name}</TableCell>
                          <TableCell>{blob.contentLength.toLocaleString()} B</TableCell>
                          <TableCell>{blob.lastModified || "—"}</TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </section>
            ) : null}
          </>
        )}
      </div>
    </>
  );
}
