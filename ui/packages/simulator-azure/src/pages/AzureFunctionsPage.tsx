import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { makeStyles, tokens, Field, Input, Select, Button, Text } from "@fluentui/react-components";
import { AzureResourceTable, AzureErrorMessage, type AzureColumn } from "../portal/index.js";
import { resourceGroupOf, locationLabel } from "../portal/format.js";
import {
  createFunctionApp,
  deleteFunctionApp,
  fetchFunctionSites,
  fetchSubscriptions,
  functionAppRuntimes,
  type CreateFunctionAppInput,
  type FunctionSite,
  type Subscription,
} from "../api.js";

const columns: AzureColumn<FunctionSite>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => <Link to={`/ui/functions/${encodeURIComponent(row.name)}`}>{row.name}</Link>,
    value: (row) => row.name,
  },
  { id: "resourceGroup", header: "Resource group", cell: (row) => resourceGroupOf(row.id), value: (row) => resourceGroupOf(row.id) },
  { id: "location", header: "Location", cell: (row) => locationLabel(row.location), value: (row) => row.location },
  { id: "kind", header: "App kind", cell: (row) => row.kind, value: (row) => row.kind },
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

// Real Azure enforces this on a Function App (site) name: 2–60 characters,
// letters, numbers, and hyphens, globally unique (it becomes
// `<name>.azurewebsites.net`) — validated inline the way the real "Create
// Function App" blade does before the request goes out.
const FUNCTION_APP_NAME_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9-]{0,58}[a-zA-Z0-9]$/;
export function isValidFunctionAppName(name: string): boolean {
  return FUNCTION_APP_NAME_PATTERN.test(name);
}

export interface FunctionAppCreateFormProps {
  subscriptions: Subscription[];
  busy: boolean;
  onCreate: (input: CreateFunctionAppInput) => void;
  onDismiss: () => void;
}

// FunctionAppCreateForm is the "Create Function App" blade form: the scoping
// subscription and resource group, the app's name and region, its runtime
// stack, and the hosting plan — the fields the real Microsoft.Web/sites PUT
// needs, with the same inline name validation the real blade runs.
export function FunctionAppCreateForm({ subscriptions, busy, onCreate, onDismiss }: FunctionAppCreateFormProps) {
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
  const [runtime, setRuntime] = useState<string>(functionAppRuntimes[1]);
  const [planName, setPlanName] = useState("sockerless-plan");

  const trimmedName = name.trim();
  const valid =
    isValidFunctionAppName(trimmedName) &&
    subscriptionId.trim() !== "" &&
    resourceGroup.trim() !== "" &&
    location.trim() !== "" &&
    planName.trim() !== "";

  return (
    <form
      className={styles.form}
      data-testid="fn-create-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!valid) return;
        onCreate({
          subscriptionId: subscriptionId.trim(),
          resourceGroup: resourceGroup.trim(),
          name: trimmedName,
          location: location.trim(),
          runtime,
          planName: planName.trim(),
        });
      }}
    >
      <Text as="h2" weight="semibold">
        Create Function App
      </Text>
      <Field label="Subscription">
        <Select
          data-testid="fn-create-subscription"
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
        <Input data-testid="fn-create-rg" value={resourceGroup} onChange={(_, data) => setResourceGroup(data.value)} />
      </Field>
      <Field label="Function App name" hint="2–60 characters. Letters, numbers, and hyphens. Must be unique across Azure.">
        <Input data-testid="fn-create-name" value={name} onChange={(_, data) => setName(data.value)} />
      </Field>
      <Field label="Region">
        <Input data-testid="fn-create-location" value={location} onChange={(_, data) => setLocation(data.value)} />
      </Field>
      <Field label="Runtime stack">
        <Select data-testid="fn-create-runtime" value={runtime} onChange={(event) => setRuntime(event.target.value)}>
          {functionAppRuntimes.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Hosting plan" hint="Created automatically as a Consumption (Y1) plan if it doesn't already exist.">
        <Input data-testid="fn-create-plan" value={planName} onChange={(_, data) => setPlanName(data.value)} />
      </Field>
      <div className={styles.formActions}>
        <Button type="submit" appearance="primary" data-testid="fn-create-submit" disabled={!valid || busy}>
          {busy ? "Creating…" : "Review + create"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

export function AzureFunctionsPage() {
  const queryClient = useQueryClient();
  const { data: subscriptions } = useQuery({ queryKey: ["subscriptions"], queryFn: fetchSubscriptions });
  const [creating, setCreating] = useState(false);

  const create = useMutation({
    mutationFn: createFunctionApp,
    onSuccess: () => {
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ["fn-sites"] });
    },
  });

  return (
    <AzureResourceTable<FunctionSite>
      columns={columns}
      queryKey={["fn-sites"]}
      queryFn={fetchFunctionSites}
      filterPlaceholder="Filter by name"
      resourceNoun="Function Apps"
      emptyTitle="No Function Apps to display"
      emptyDescription="Function Apps created in this subscription appear here."
      rowKey={(row) => row.id}
      essentials={(rows) => [
        { label: "Subscription", value: "Simulator" },
        { label: "Function Apps", value: String(rows.length) },
        { label: "Locations", value: new Set(rows.map((row) => row.location)).size || "—" },
      ]}
      extraCommands={[{ label: "Create", icon: "add", testid: "fn-create", onSelect: () => setCreating(true) }]}
      onDelete={async (sites) => {
        for (const site of sites) {
          await deleteFunctionApp(site.id);
        }
      }}
      deleteWarning="Deleting a Function App is permanent and removes every function deployed to it. This action can't be undone."
      deleteTestId="fn-delete"
      banner={
        <>
          {create.isError ? (
            <AzureErrorMessage testid="fn-create-error">
              <strong>The Function App could not be created.</strong>{" "}
              {create.error instanceof Error ? create.error.message : "Azure Resource Manager did not respond."}
            </AzureErrorMessage>
          ) : null}
          {creating ? (
            <FunctionAppCreateForm
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
