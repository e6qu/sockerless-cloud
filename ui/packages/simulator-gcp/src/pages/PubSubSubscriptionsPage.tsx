import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { Copyable } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpResourceDetail } from "../console/GcpResourceDetail.js";
import { ResourceTableView } from "../console/GcpTable.js";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { Icon } from "../console/icons.js";
import { formatTimestamp, shortName } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  acknowledgePubSubMessages,
  decodeBase64,
  deletePubSubSubscription,
  fetchPubSubSubscription,
  fetchPubSubSubscriptions,
  fetchPubSubTopics,
  pubsubSubscriptionRequest,
  pullPubSubMessages,
  sendRequest,
  type PubSubReceivedMessage,
  type PubSubSubscription,
} from "../api.js";
import { formatDuration, isValidPubSubId } from "./PubSubPage.js";

function SubscriptionName({ subscription }: { subscription: PubSubSubscription }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(`/ui/cloudpubsub/subscription/detail/${shortName(subscription.name)}`)}>
      {shortName(subscription.name)}
    </Link>
  );
}

const deliveryType = (subscription: PubSubSubscription) => (subscription.pushConfig?.pushEndpoint ? "Push" : "Pull");

const columns: GcpColumn<PubSubSubscription>[] = [
  { id: "id", header: "Subscription ID", cell: (row) => <SubscriptionName subscription={row} />, value: (row) => shortName(row.name) },
  { id: "topic", header: "Topic name", cell: (row) => row.topic ?? "—", value: (row) => row.topic ?? "" },
  { id: "delivery", header: "Delivery type", cell: deliveryType, value: deliveryType },
  {
    id: "ack",
    header: "Acknowledgement deadline",
    cell: (row) => (row.ackDeadlineSeconds ? `${row.ackDeadlineSeconds} s` : "—"),
    value: (row) => String(row.ackDeadlineSeconds ?? ""),
  },
  {
    id: "retention",
    header: "Message retention duration",
    cell: (row) => formatDuration(row.messageRetentionDuration),
    value: (row) => row.messageRetentionDuration ?? "",
  },
];

function DeleteSubscriptionsDialog({
  subscriptions,
  onClose,
  onDeleted,
}: {
  subscriptions: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const { track } = useOperations();
  const subject = subscriptions.length === 1 ? `subscription "${subscriptions[0]}"` : `${subscriptions.length} subscriptions`;
  const remove = useMutation({
    mutationFn: () =>
      track(
        `Delete ${subject}`,
        () => Promise.all(subscriptions.map((subscription) => deletePubSubSubscription(project, subscription))),
        `Deleted ${subject}`,
      ),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="pubsub-delete-sub-dialog" onClose={onClose}>
      <p>Deleting a subscription removes it permanently, with any messages it has not delivered.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary gc-button-danger"
          data-testid="pubsub-delete-sub-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function PubSubSubscriptionsPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string[] | null>(null);
  return (
    <>
      <GcpResourceTable<PubSubSubscription>
        title="Subscriptions"
        actions={[{ label: "Create subscription", icon: "add", to: "/ui/cloudpubsub/subscription/create", testId: "pubsub-create-sub" }]}
        selectionActions={(selected) => [
          { label: "Delete", icon: "delete", onSelect: () => setDeleting(selected.map((row) => shortName(row.name))) },
        ]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting([shortName(row.name)]) }]}
        columns={columns}
        queryKey={["pubsub-subscriptions", project]}
        queryFn={() => fetchPubSubSubscriptions(project)}
        resourceNoun="subscriptions"
        testId="pubsub-subscriptions-list-table"
        empty={{ headline: "No subscriptions", description: "A subscription delivers a topic's messages to a subscriber." }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteSubscriptionsDialog
          subscriptions={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            void queryClient.invalidateQueries({ queryKey: ["pubsub-subscriptions", project] });
          }}
        />
      ) : null}
    </>
  );
}

export function PubSubSubscriptionCreatePage() {
  const { project } = useProject();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const topics = useQuery({ queryKey: ["pubsub-topics", project], queryFn: () => fetchPubSubTopics(project) });
  const [subscriptionId, setSubscriptionId] = useState("");
  const [topic, setTopic] = useState(params.get("topic") ?? "");
  const [delivery, setDelivery] = useState<"pull" | "push">("pull");
  const [endpoint, setEndpoint] = useState("");
  const [retentionDays, setRetentionDays] = useState(7);
  const [ackDeadline, setAckDeadline] = useState(10);
  const [retainAcked, setRetainAcked] = useState(false);
  const request = pubsubSubscriptionRequest(project, subscriptionId, {
    topic,
    ackDeadlineSeconds: ackDeadline,
    retentionSeconds: retentionDays * 86400,
    retainAckedMessages: retainAcked,
    pushEndpoint: delivery === "push" ? endpoint : "",
  });
  const idError = subscriptionId && !isValidPubSubId(subscriptionId)
    ? "Use 3 to 255 letters, numbers, or . - _ ~ + %, starting with a letter and not with goog."
    : null;
  const create = useMutation({
    mutationFn: () =>
      track(`Create subscription "${subscriptionId}"`, () => sendRequest(request), `Created subscription "${subscriptionId}"`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["pubsub-subscriptions", project] });
      navigate(path(`/ui/cloudpubsub/subscription/detail/${subscriptionId}`));
    },
  });
  return (
    <CreatePage
      title="Add subscription to topic"
      backTo="/ui/cloudpubsub/subscription/list"
      testId="pubsub-create-sub-form"
      submitting={create.isPending}
      canSubmit={Boolean(subscriptionId && !idError && topic && ackDeadline >= 10 && ackDeadline <= 600 && (delivery === "pull" || endpoint))}
      error={create.error}
      errorTitle="Couldn't create the subscription."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Subscription">
        <Field label="Subscription ID" error={idError} hint={`Subscription name: projects/${project}/subscriptions/${subscriptionId || "<subscription ID>"}`}>
          <input type="text" value={subscriptionId} data-testid="pubsub-create-sub-id" onChange={(event) => setSubscriptionId(event.target.value)} />
        </Field>
        <Field label="Select a Cloud Pub/Sub topic">
          <select value={topic} data-testid="pubsub-create-sub-topic" onChange={(event) => setTopic(event.target.value)}>
            <option value="">Select a topic</option>
            {(topics.data ?? []).map((candidate) => (
              <option key={candidate.name} value={shortName(candidate.name)}>
                {candidate.name}
              </option>
            ))}
          </select>
        </Field>
      </FormSection>
      <FormSection title="Delivery type">
        <fieldset className="gc-radio-group">
          <legend className="gc-visually-hidden">Delivery type</legend>
          <label className="gc-radio">
            <input type="radio" name="delivery" checked={delivery === "pull"} onChange={() => setDelivery("pull")} />
            Pull
          </label>
          <label className="gc-radio">
            <input type="radio" name="delivery" checked={delivery === "push"} onChange={() => setDelivery("push")} />
            Push
          </label>
        </fieldset>
        {delivery === "push" ? (
          <Field label="Endpoint URL">
            <input type="url" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} />
          </Field>
        ) : null}
      </FormSection>
      <FormSection title="Message retention duration" description="How long unacknowledged messages are retained.">
        <Field label="Days" hint="Between 1 and 7 days">
          <input type="number" min={1} max={7} value={retentionDays} onChange={(event) => setRetentionDays(Number(event.target.value))} />
        </Field>
        <label className="gc-checkbox">
          <input type="checkbox" checked={retainAcked} onChange={(event) => setRetainAcked(event.target.checked)} />
          Retain acknowledged messages
        </label>
      </FormSection>
      <FormSection title="Acknowledgement deadline">
        <Field label="Acknowledgement deadline (seconds)" hint="Between 10 and 600 seconds">
          <input type="number" min={10} max={600} value={ackDeadline} data-testid="pubsub-create-sub-ack" onChange={(event) => setAckDeadline(Number(event.target.value))} />
        </Field>
      </FormSection>
    </CreatePage>
  );
}

function MessagesTab({ subscription }: { subscription: string }) {
  const { project } = useProject();
  const { track } = useOperations();
  const [acknowledge, setAcknowledge] = useState(false);
  const [messages, setMessages] = useState<PubSubReceivedMessage[] | null>(null);
  const pull = useMutation({
    mutationFn: () =>
      track(`Pull messages from "${subscription}"`, async () => {
        const received = await pullPubSubMessages(project, subscription, 10);
        if (acknowledge && received.length > 0) {
          await acknowledgePubSubMessages(project, subscription, received.map((message) => message.ackId));
        }
        return received;
      }),
    onSuccess: setMessages,
  });
  return (
    <>
      <div className="gc-toolbar">
        <button type="button" className="gc-button-outlined" data-testid="pubsub-pull" disabled={pull.isPending} onClick={() => pull.mutate()}>
          <Icon name="download" size="1.25em" />
          Pull
        </button>
        <label className="gc-checkbox">
          <input type="checkbox" checked={acknowledge} onChange={(event) => setAcknowledge(event.target.checked)} />
          Enable ack messages
        </label>
      </div>
      {pull.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't pull messages.</strong> {pull.error instanceof Error ? pull.error.message : String(pull.error)}
        </div>
      ) : null}
      {messages ? (
        <ResourceTableView<PubSubReceivedMessage>
          rows={messages}
          isLoading={false}
          error={null}
          tableId="pubsub-messages-table"
          resourceNoun="messages"
          rowKey={(row) => row.ackId}
          columns={[
            { id: "publishTime", header: "Publish time", cell: (row) => formatTimestamp(row.message.publishTime ?? ""), value: (row) => row.message.publishTime ?? "" },
            { id: "body", header: "Message body", cell: (row) => decodeBase64(row.message.data ?? ""), value: (row) => decodeBase64(row.message.data ?? "") },
            { id: "id", header: "Message ID", cell: (row) => row.message.messageId ?? "—", value: (row) => row.message.messageId ?? "" },
            {
              id: "attributes",
              header: "Attributes",
              cell: (row) => Object.entries(row.message.attributes ?? {}).map(([key, value]) => `${key}=${value}`).join(", ") || "—",
              value: (row) => Object.entries(row.message.attributes ?? {}).map(([key, value]) => `${key}=${value}`).join(" "),
            },
            {
              id: "ack",
              header: "Ack",
              cell: () => (acknowledge ? "Acknowledged" : "Deadline exceeded"),
              value: () => (acknowledge ? "Acknowledged" : "Deadline exceeded"),
            },
          ]}
          empty={{ headline: "No messages", description: "The pull returned no messages. Publish to the topic and pull again." }}
        />
      ) : (
        <p className="gc-tab-intro">Pull messages to view them. Pulled messages that aren't acknowledged are redelivered after the acknowledgement deadline.</p>
      )}
    </>
  );
}

export function PubSubSubscriptionDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const [deleting, setDeleting] = useState(false);
  return (
    <>
      <GcpResourceDetail<PubSubSubscription>
        title={name}
        backTo="/ui/cloudpubsub/subscription/list"
        queryKey={["pubsub-subscription", project, name]}
        queryFn={() => fetchPubSubSubscription(project, name)}
        actions={[{ label: "Delete", icon: "delete", testId: "pubsub-sub-delete", onSelect: () => setDeleting(true) }]}
        properties={(subscription) => [
          { label: "Subscription name", value: <Copyable value={subscription.name} /> },
          { label: "Topic name", value: subscription.topic ?? "—" },
          { label: "Delivery type", value: deliveryType(subscription) },
          ...(subscription.pushConfig?.pushEndpoint ? [{ label: "Endpoint URL", value: subscription.pushConfig.pushEndpoint }] : []),
          { label: "Acknowledgement deadline", value: subscription.ackDeadlineSeconds ? `${subscription.ackDeadlineSeconds} s` : "—" },
          { label: "Message retention duration", value: formatDuration(subscription.messageRetentionDuration) },
          { label: "Retain acknowledged messages", value: subscription.retainAckedMessages ? "Yes" : "No" },
        ]}
        tabs={() => [{ id: "messages", label: "Messages", content: <MessagesTab subscription={name} /> }]}
      />
      {deleting ? (
        <DeleteSubscriptionsDialog
          subscriptions={[name]}
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate(path("/ui/cloudpubsub/subscription/list"))}
        />
      ) : null}
    </>
  );
}
