import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { makeStyles, tokens, Field, Input, Select, Button, Text } from "@fluentui/react-components";
import { AzureResourceTable, AzureErrorMessage, type AzureColumn } from "../portal/index.js";
import { resourceGroupOf, locationLabel } from "../portal/format.js";
import {
  createACRRegistry,
  deleteACRRegistry,
  fetchACRRegistries,
  fetchSubscriptions,
  type ACRRegistry,
  type CreateACRRegistryInput,
  type Subscription,
} from "../api.js";

const columns: AzureColumn<ACRRegistry>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link to={`/ui/acr/${encodeURIComponent(row.name)}`}>{row.name}</Link>,
    value: (row) => row.name,
  },
  { id: "resourceGroup", header: "Resource group", cell: (row) => resourceGroupOf(row.id), value: (row) => resourceGroupOf(row.id) },
  { id: "location", header: "Location", cell: (row) => locationLabel(row.location), value: (row) => row.location },
  { id: "loginServer", header: "Login server", cell: (row) => `${row.name}.azurecr.io`, value: (row) => `${row.name}.azurecr.io` },
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

// Real Azure Container Registry enforces this on CreateRegistry: 5–50
// characters, letters and numbers only (no separators), and the name must be
// unique across all of Azure (it becomes the registry's login server,
// `<name>.azurecr.io`) — validating it inline is what the real "Create
// container registry" blade does before it lets the request go out.
const ACR_NAME_PATTERN = /^[a-zA-Z0-9]{5,50}$/;
export function isValidACRRegistryName(name: string): boolean {
  return ACR_NAME_PATTERN.test(name);
}

// The sku.name choices the real "Create container registry" blade's Basics
// tab offers.
const ACR_SKUS = ["Basic", "Standard", "Premium"] as const;

export interface ACRRegistryCreateFormProps {
  subscriptions: Subscription[];
  busy: boolean;
  onCreate: (input: CreateACRRegistryInput) => void;
  onDismiss: () => void;
}

// ACRRegistryCreateForm is the "Create container registry" blade form: the
// scoping subscription and resource group, the registry's name, region, and
// SKU — the fields the real Microsoft.ContainerRegistry PUT needs, with the
// same inline name validation the real blade runs before it lets the
// request go out.
export function ACRRegistryCreateForm({ subscriptions, busy, onCreate, onDismiss }: ACRRegistryCreateFormProps) {
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
  const [skuName, setSkuName] = useState<string>(ACR_SKUS[0]);

  const trimmedName = name.trim();
  const trimmedRG = resourceGroup.trim();
  const trimmedLocation = location.trim();
  const valid = isValidACRRegistryName(trimmedName) && subscriptionId.trim() !== "" && trimmedRG !== "" && trimmedLocation !== "";

  return (
    <form
      className={styles.form}
      data-testid="acr-create-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!valid) return;
        onCreate({
          subscriptionId: subscriptionId.trim(),
          resourceGroup: trimmedRG,
          name: trimmedName,
          location: trimmedLocation,
          skuName,
        });
      }}
    >
      <Text as="h2" weight="semibold">
        Create container registry
      </Text>
      <Field label="Subscription">
        <Select
          data-testid="acr-create-subscription"
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
        <Input data-testid="acr-create-rg" value={resourceGroup} onChange={(_, data) => setResourceGroup(data.value)} />
      </Field>
      <Field
        label="Registry name"
        hint="5–50 characters. Letters and numbers only. Must be unique across Azure."
      >
        <Input data-testid="acr-create-name" value={name} onChange={(_, data) => setName(data.value)} />
      </Field>
      <Field label="Region">
        <Input data-testid="acr-create-location" value={location} onChange={(_, data) => setLocation(data.value)} />
      </Field>
      <Field label="SKU">
        <Select data-testid="acr-create-sku" value={skuName} onChange={(event) => setSkuName(event.target.value)}>
          {ACR_SKUS.map((sku) => (
            <option key={sku} value={sku}>
              {sku}
            </option>
          ))}
        </Select>
      </Field>
      <div className={styles.formActions}>
        <Button type="submit" appearance="primary" data-testid="acr-create-submit" disabled={!valid || busy}>
          {busy ? "Creating…" : "Review + create"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// The Container registries blade: the real Microsoft.ContainerRegistry list
// across every subscription this directory can reach, and a Create flow
// that provisions a new registry through the real registry PUT (ensuring
// the scoping resource group exists first, the same as `az acr create`).
export function ACRRegistriesPage() {
  const queryClient = useQueryClient();
  const { data: subscriptions } = useQuery({ queryKey: ["subscriptions"], queryFn: fetchSubscriptions });
  const [creating, setCreating] = useState(false);

  const create = useMutation({
    mutationFn: createACRRegistry,
    onSuccess: () => {
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ["acr-registries"] });
    },
  });

  return (
    <AzureResourceTable<ACRRegistry>
      columns={columns}
      queryKey={["acr-registries"]}
      queryFn={fetchACRRegistries}
      filterPlaceholder="Filter by name"
      resourceNoun="container registries"
      emptyTitle="No container registries to display"
      emptyDescription="Registries created in this subscription appear here."
      rowKey={(row) => row.id}
      essentials={(rows) => [
        { label: "Subscription", value: "Simulator" },
        { label: "Registries", value: String(rows.length) },
        { label: "Locations", value: new Set(rows.map((row) => row.location)).size || "—" },
      ]}
      extraCommands={[{ label: "Create", icon: "add", testid: "acr-create", onSelect: () => setCreating(true) }]}
      onDelete={async (registries) => {
        for (const registry of registries) {
          await deleteACRRegistry(registry.id);
        }
      }}
      deleteWarning="Deleting a container registry is permanent and removes every repository and image it holds. This action can't be undone."
      deleteTestId="acr-delete"
      banner={
        <>
          {create.isError ? (
            <AzureErrorMessage testid="acr-create-error">
              <strong>The container registry could not be created.</strong>{" "}
              {create.error instanceof Error ? create.error.message : "Azure Resource Manager did not respond."}
            </AzureErrorMessage>
          ) : null}
          {creating ? (
            <ACRRegistryCreateForm
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
