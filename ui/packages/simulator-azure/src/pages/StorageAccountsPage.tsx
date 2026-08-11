import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { makeStyles, tokens, Field, Input, Select, Button, Text } from "@fluentui/react-components";
import { AzureResourceTable, AzureErrorMessage, type AzureColumn } from "../portal/index.js";
import { resourceGroupOf, locationLabel } from "../portal/format.js";
import {
  createStorageAccount,
  deleteStorageAccount,
  fetchStorageAccounts,
  fetchSubscriptions,
  type CreateStorageAccountInput,
  type StorageAccount,
  type Subscription,
} from "../api.js";

const columns: AzureColumn<StorageAccount>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link to={`/ui/storage/${encodeURIComponent(row.name)}`}>{row.name}</Link>,
    value: (row) => row.name,
  },
  { id: "resourceGroup", header: "Resource group", cell: (row) => resourceGroupOf(row.id), value: (row) => resourceGroupOf(row.id) },
  { id: "location", header: "Location", cell: (row) => locationLabel(row.location), value: (row) => row.location },
  { id: "kind", header: "Kind", cell: (row) => row.kind, value: (row) => row.kind },
];

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
  formActions: { display: "flex", gap: "8px" },
});

// Real Azure Storage enforces this on CreateStorageAccount: 3–24 characters,
// lowercase letters and numbers only, and the name must be unique across all
// of Azure — validating it inline is what the real "Create a storage
// account" blade does before it lets the request go out.
const STORAGE_ACCOUNT_NAME_PATTERN = /^[a-z0-9]{3,24}$/;
export function isValidStorageAccountName(name: string): boolean {
  return STORAGE_ACCOUNT_NAME_PATTERN.test(name);
}

// The replication (sku.name) and account-kind choices the real "Create a
// storage account" blade's Basics tab offers.
const STORAGE_SKUS = ["Standard_LRS", "Standard_GRS", "Standard_RAGRS", "Standard_ZRS", "Premium_LRS"] as const;
const STORAGE_KINDS = ["StorageV2", "BlobStorage", "BlockBlobStorage", "FileStorage", "Storage"] as const;

export interface StorageAccountCreateFormProps {
  subscriptions: Subscription[];
  busy: boolean;
  onCreate: (input: CreateStorageAccountInput) => void;
  onDismiss: () => void;
}

// StorageAccountCreateForm is the "Create a storage account" blade form: the
// scoping subscription and resource group, the account's name and region,
// and its replication/kind — the fields the real Microsoft.Storage PUT
// needs, with the same inline name validation the real blade runs before it
// lets the request go out.
export function StorageAccountCreateForm({ subscriptions, busy, onCreate, onDismiss }: StorageAccountCreateFormProps) {
  const styles = useStyles();
  // The subscription list arrives asynchronously, so the chosen value cannot be
  // frozen at mount: a form opened before the query resolves would hold an
  // empty subscription forever and leave its submit permanently disabled.
  // An explicit choice wins; otherwise the first loaded subscription is used.
  const [chosenSubscriptionId, setSubscriptionId] = useState("");
  const subscriptionId = chosenSubscriptionId || subscriptions[0]?.subscriptionId || "";
  const [resourceGroup, setResourceGroup] = useState("sockerless-console");
  const [name, setName] = useState("");
  const [location, setLocation] = useState("eastus");
  const [skuName, setSkuName] = useState<string>(STORAGE_SKUS[0]);
  const [kind, setKind] = useState<string>(STORAGE_KINDS[0]);

  const trimmedName = name.trim();
  const trimmedRG = resourceGroup.trim();
  const trimmedLocation = location.trim();
  const valid = isValidStorageAccountName(trimmedName) && subscriptionId.trim() !== "" && trimmedRG !== "" && trimmedLocation !== "";

  return (
    <form
      className={styles.form}
      data-testid="storage-create-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!valid) return;
        onCreate({
          subscriptionId: subscriptionId.trim(),
          resourceGroup: trimmedRG,
          name: trimmedName,
          location: trimmedLocation,
          skuName,
          kind,
        });
      }}
    >
      <Text as="h2" weight="semibold">
        Create a storage account
      </Text>
      <Field label="Subscription">
        <Select
          data-testid="storage-create-subscription"
          value={subscriptionId}
          onChange={(event) => setSubscriptionId(event.target.value)}
        >
          <option value="" disabled>
            Select a subscription
          </option>
          {subscriptions.map((subscription) => (
            <option key={subscription.subscriptionId} value={subscription.subscriptionId}>
              {subscription.displayName || subscription.subscriptionId}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Resource group" hint="Created automatically if it doesn't already exist in this subscription.">
        <Input
          data-testid="storage-create-rg"
          value={resourceGroup}
          onChange={(_, data) => setResourceGroup(data.value)}
        />
      </Field>
      <Field
        label="Storage account name"
        hint="3–24 characters. Lowercase letters and numbers only. Must be unique across Azure."
      >
        <Input data-testid="storage-create-name" value={name} onChange={(_, data) => setName(data.value)} />
      </Field>
      <Field label="Region">
        <Input data-testid="storage-create-location" value={location} onChange={(_, data) => setLocation(data.value)} />
      </Field>
      <Field label="Redundancy">
        <Select data-testid="storage-create-sku" value={skuName} onChange={(event) => setSkuName(event.target.value)}>
          {STORAGE_SKUS.map((sku) => (
            <option key={sku} value={sku}>
              {sku}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Account kind">
        <Select data-testid="storage-create-kind" value={kind} onChange={(event) => setKind(event.target.value)}>
          {STORAGE_KINDS.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </Select>
      </Field>
      <div className={styles.formActions}>
        <Button type="submit" appearance="primary" data-testid="storage-create-submit" disabled={!valid || busy}>
          {busy ? "Creating…" : "Review + create"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// The Storage accounts blade: the real Microsoft.Storage list across every
// subscription this directory can reach, and a Create flow that provisions a
// new account through the real storage-account PUT (ensuring the scoping
// resource group exists first, the same as `az storage account create`).
export function StorageAccountsPage() {
  const queryClient = useQueryClient();
  const { data: subscriptions } = useQuery({ queryKey: ["subscriptions"], queryFn: fetchSubscriptions });
  const [creating, setCreating] = useState(false);

  const create = useMutation({
    mutationFn: createStorageAccount,
    onSuccess: () => {
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ["storage-accounts"] });
    },
  });

  return (
    <AzureResourceTable<StorageAccount>
      columns={columns}
      queryKey={["storage-accounts"]}
      queryFn={fetchStorageAccounts}
      filterPlaceholder="Filter by name"
      resourceNoun="storage accounts"
      emptyTitle="No storage accounts to display"
      emptyDescription="Storage accounts created in this subscription appear here."
      rowKey={(row) => row.id}
      essentials={(rows) => [
        { label: "Subscription", value: "Simulator" },
        { label: "Storage accounts", value: String(rows.length) },
        { label: "Locations", value: new Set(rows.map((row) => row.location)).size || "—" },
      ]}
      extraCommands={[{ label: "Create", icon: "add", testid: "storage-create", onSelect: () => setCreating(true) }]}
      onDelete={async (accounts) => {
        for (const account of accounts) {
          await deleteStorageAccount(account.id);
        }
      }}
      deleteWarning="Deleting a storage account is permanent and removes every container, blob, file share, table, and queue it holds. This action can't be undone."
      deleteTestId="storage-delete"
      banner={
        <>
          {create.isError ? (
            <AzureErrorMessage testid="storage-create-error">
              <strong>The storage account could not be created.</strong>{" "}
              {create.error instanceof Error ? create.error.message : "Azure Resource Manager did not respond."}
            </AzureErrorMessage>
          ) : null}
          {creating ? (
            <StorageAccountCreateForm
              subscriptions={subscriptions ?? []}
              busy={create.isPending}
              onCreate={(input) => create.mutate(input)}
              onDismiss={() => setCreating(false)}
            />
          ) : null}
        </>
      }
    />
  );
}
