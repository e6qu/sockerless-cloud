import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  makeStyles,
  tokens,
  Field,
  Input,
  Button,
  Link,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  Text,
} from "@fluentui/react-components";
import { AzureCommandBar, AzureEssentials, AzureErrorMessage } from "../portal/AzurePortal.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import {
  createSubscriptionAlias,
  fetchSubscriptions,
  getSubscriptionAlias,
  type Subscription,
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
  formActions: { display: "flex", gap: "8px" },
});

// The default enrollment-account billing scope offered by the create form —
// the coordinate a real tenant reads from its Microsoft.Billing accounts; the
// operator can point the form at any scope they hold.
const DEFAULT_BILLING_SCOPE =
  "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment";

interface SubscriptionCreateFormProps {
  busy: boolean;
  // The alias's live provisioningState while a creation is in flight; empty
  // when idle.
  provisioningState: string;
  onCreate: (displayName: string, billingScope: string) => void;
  onDismiss: () => void;
}

// SubscriptionCreateForm is the "Add subscription" blade form: a display name
// and the billing scope the subscription is created under, driving the real
// Microsoft.Subscription alias PUT and reporting the alias's provisioning
// state until the subscription materializes.
export function SubscriptionCreateForm({ busy, provisioningState, onCreate, onDismiss }: SubscriptionCreateFormProps) {
  const styles = useStyles();
  const [name, setName] = useState("");
  const [scope, setScope] = useState(DEFAULT_BILLING_SCOPE);
  return (
    <form
      className={styles.form}
      data-testid="subs-create-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (name.trim() && scope.trim()) onCreate(name.trim(), scope.trim());
      }}
    >
      <Text as="h2" weight="semibold">
        Create a subscription
      </Text>
      <Field label="Subscription name">
        <Input
          data-testid="subs-create-name"
          value={name}
          placeholder="Enter a name for the subscription"
          onChange={(_, data) => setName(data.value)}
        />
      </Field>
      <Field label="Billing scope">
        <Input data-testid="subs-create-scope" value={scope} onChange={(_, data) => setScope(data.value)} />
      </Field>
      {provisioningState ? (
        <Text as="p" role="status" block data-testid="subs-provisioning">
          Creating subscription… provisioning state: {provisioningState}
        </Text>
      ) : null}
      <div className={styles.formActions}>
        <Button
          type="submit"
          appearance="primary"
          data-testid="subs-create-submit"
          disabled={!name.trim() || !scope.trim() || busy}
        >
          Create
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// The Subscriptions blade: every subscription in the directory from the real
// Azure Resource Manager subscriptions list, and an Add flow that creates one
// through the Microsoft.Subscription alias API, polling the alias until the
// subscription is provisioned.
export function SubscriptionsPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ["subscriptions"],
    queryFn: fetchSubscriptions,
  });
  const [adding, setAdding] = useState(false);
  const [provisioningState, setProvisioningState] = useState("");

  const create = useMutation({
    mutationFn: async ({ displayName, billingScope }: { displayName: string; billingScope: string }) => {
      let alias = await createSubscriptionAlias(displayName, billingScope);
      setProvisioningState(alias.provisioningState);
      while (alias.provisioningState !== "Succeeded") {
        if (alias.provisioningState === "Failed") {
          throw new Error(`subscription provisioning failed for alias ${alias.name}`);
        }
        await new Promise((resolve) => setTimeout(resolve, 1000));
        alias = await getSubscriptionAlias(alias.name);
        setProvisioningState(alias.provisioningState);
      }
      return alias;
    },
    onSuccess: () => {
      setAdding(false);
      void queryClient.invalidateQueries({ queryKey: ["subscriptions"] });
    },
    onSettled: () => setProvisioningState(""),
  });

  const rows = data ?? [];

  return (
    <>
      <AzureCommandBar
        commands={[
          { label: "Add", icon: "add", testid: "subs-add", onSelect: () => setAdding(true) },
          { label: "Refresh", icon: "refresh", onSelect: () => void refetch(), disabled: isFetching },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      <div className="az-main">
        <AzureEssentials
          properties={[
            { label: "Directory", value: "Simulator" },
            { label: "Subscriptions", value: String(rows.length) },
          ]}
        />

        {create.error ? (
          <AzureErrorMessage testid="subs-error">
            <strong>The subscription could not be created.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : null}

        {adding ? (
          <SubscriptionCreateForm
            busy={create.isPending}
            provisioningState={provisioningState}
            onCreate={(displayName, billingScope) => create.mutate({ displayName, billingScope })}
            onDismiss={() => setAdding(false)}
          />
        ) : null}

        <Table aria-label="Subscriptions" size="small" data-testid="subs-table">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Subscription name</TableHeaderCell>
              <TableHeaderCell>Subscription ID</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isError ? (
              <AzureTableErrorRow colSpan={3} testid="subs-list-error">
                <strong>Could not load subscriptions.</strong>{" "}
                {error instanceof Error ? error.message : "Azure Resource Manager did not respond."}
              </AzureTableErrorRow>
            ) : isLoading ? (
              <AzureTableLoadingRow colSpan={3} label="Loading subscriptions…" />
            ) : rows.length === 0 ? (
              <AzureTableEmptyRow
                colSpan={3}
                title="No subscriptions to display"
                description="Subscriptions this directory can reach appear here."
              />
            ) : (
              rows.map((row: Subscription) => (
                <TableRow key={row.subscriptionId} data-testid="subs-row">
                  <TableCell>
                    <Link
                      href={`/ui/subscriptions/${row.subscriptionId}`}
                      onClick={(event) => {
                        event.preventDefault();
                        navigate(`/ui/subscriptions/${row.subscriptionId}`);
                      }}
                    >
                      {row.displayName}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <code>{row.subscriptionId}</code>
                  </TableCell>
                  <TableCell>{row.state === "Enabled" ? "Active" : row.state === "Disabled" ? "Disabled" : row.state}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </>
  );
}
