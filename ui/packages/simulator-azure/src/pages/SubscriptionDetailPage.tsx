import { useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AzureCommandBar, AzureEssentials, AzureErrorMessage, AzureEmptyState } from "../portal/AzurePortal.js";
import { cancelSubscription, enableSubscription, fetchSubscription } from "../api.js";

// A subscription's detail blade: the Essentials the real portal leads with,
// plus the two lifecycle actions the real API offers — cancel (a subscription
// becomes Disabled; Azure never deletes subscriptions) and reactivate.
export function SubscriptionDetailPage() {
  const { subscriptionId = "" } = useParams();
  const queryClient = useQueryClient();
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ["subscription", subscriptionId],
    queryFn: () => fetchSubscription(subscriptionId),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["subscription", subscriptionId] });
    void queryClient.invalidateQueries({ queryKey: ["subscriptions"] });
  };
  const cancel = useMutation({
    mutationFn: () => cancelSubscription(subscriptionId),
    onSuccess: invalidate,
  });
  const enable = useMutation({
    mutationFn: () => enableSubscription(subscriptionId),
    onSuccess: invalidate,
  });

  const mutationError = cancel.error ?? enable.error;

  return (
    <>
      <AzureCommandBar
        commands={[
          {
            label: "Cancel subscription",
            icon: "delete",
            testid: "subs-cancel",
            disabled: !data || data.state !== "Enabled" || cancel.isPending,
            onSelect: () => cancel.mutate(),
          },
          {
            label: "Reactivate",
            icon: "add",
            testid: "subs-enable",
            disabled: !data || data.state !== "Disabled" || enable.isPending,
            onSelect: () => enable.mutate(),
          },
          { label: "Refresh", icon: "refresh", onSelect: () => void refetch(), disabled: isFetching },
        ]}
      />
      <div className="az-main" data-testid="subs-detail">
        {isError ? (
          <AzureErrorMessage testid="subs-detail-error">
            <strong>Could not load the subscription.</strong>{" "}
            {error instanceof Error ? error.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : isLoading ? (
          <AzureEmptyState title="Loading subscription…" loading />
        ) : data ? (
          <AzureEssentials
            properties={[
              { label: "Subscription name", value: data.displayName || "—" },
              { label: "Subscription ID", value: data.subscriptionId },
              { label: "Status", value: data.state === "Enabled" ? "Active" : data.state },
            ]}
          />
        ) : null}

        {mutationError ? (
          <AzureErrorMessage testid="subs-action-error">
            <strong>The subscription operation failed.</strong>{" "}
            {mutationError instanceof Error ? mutationError.message : "Azure Resource Manager did not respond."}
          </AzureErrorMessage>
        ) : null}
      </div>
    </>
  );
}
