import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  createPubSubSubscription,
  createPubSubTopic,
  deletePubSubSubscription,
  deletePubSubTopic,
  fetchPubSubSubscriptions,
  fetchPubSubTopic,
  fetchPubSubTopics,
  type PubSubSubscription,
  type PubSubTopic,
} from "../api.js";
import { useProject } from "../console/project.js";

// Pub/Sub's resource-ID contract: 3–255 characters, starting with a letter,
// then letters, digits and any of . - _ ~ + %, and never the reserved "goog"
// prefix.
const PUBSUB_ID_PATTERN = /^[A-Za-z][A-Za-z0-9._~+%-]{2,254}$/;
const isValidPubSubId = (id: string) => PUBSUB_ID_PATTERN.test(id) && !id.toLowerCase().startsWith("goog");

const topicColumns: GcpColumn<PubSubTopic>[] = [
  {
    id: "name",
    header: "Topic ID",
    cell: (row) => (
      <Link className="gc-cell-link" to={`/ui/pubsub/${shortName(row.name)}`}>
        {shortName(row.name)}
      </Link>
    ),
    value: (row) => shortName(row.name),
  },
  {
    id: "retention",
    header: "Message retention",
    cell: (row) => row.messageRetentionDuration ?? "—",
    value: (row) => row.messageRetentionDuration ?? "",
  },
  {
    id: "encryption",
    header: "Encryption key",
    cell: (row) => (row.kmsKeyName ? shortName(row.kmsKeyName) : "Google-managed"),
    value: (row) => row.kmsKeyName ?? "",
  },
];

// CreateTopicDialog runs the real projects.topics.create method — a PUT on the
// topic's own resource name, the shape Pub/Sub uses instead of a POST to the
// collection.
export function CreateTopicDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const [topicId, setTopicId] = useState("");
  const create = useMutation({ mutationFn: () => createPubSubTopic(project, topicId), onSuccess: onCreated });
  return (
    <GcpDialog title="Create a topic" testId="pubsub-create-topic-dialog" onClose={onClose}>
      <label className="gc-field">
        Topic ID
        <input
          type="text"
          value={topicId}
          data-testid="pubsub-create-topic-id"
          onChange={(event) => setTopicId(event.target.value)}
        />
        <p className="gc-field-hint">3–255 characters starting with a letter; must not start with “goog”.</p>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the topic.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="pubsub-create-topic-submit"
          disabled={!isValidPubSubId(topicId) || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteTopicDialog({
  topic,
  onClose,
  onDeleted,
}: {
  topic: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({ mutationFn: () => deletePubSubTopic(project, topic), onSuccess: onDeleted });
  return (
    <GcpDialog title="Delete topic?" testId="pubsub-delete-topic-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{topic}</strong> permanently removes the topic. Its subscriptions are detached and
        stop receiving messages.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the topic.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="pubsub-delete-topic-confirm"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

// CreateSubscriptionDialog runs the real projects.subscriptions.create method
// (likewise a PUT on the subscription's resource name), attaching it to the
// topic the detail page is showing.
export function CreateSubscriptionDialog({
  topic,
  onClose,
  onCreated,
}: {
  topic: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { project } = useProject();
  const [subscriptionId, setSubscriptionId] = useState("");
  const [ackDeadline, setAckDeadline] = useState("10");
  const create = useMutation({
    mutationFn: () => createPubSubSubscription(project, subscriptionId, topic, Number(ackDeadline)),
    onSuccess: onCreated,
  });
  const valid = isValidPubSubId(subscriptionId) && Number(ackDeadline) >= 10 && Number(ackDeadline) <= 600;
  return (
    <GcpDialog title="Create a subscription" testId="pubsub-create-sub-dialog" onClose={onClose}>
      <label className="gc-field">
        Subscription ID
        <input
          type="text"
          value={subscriptionId}
          data-testid="pubsub-create-sub-id"
          onChange={(event) => setSubscriptionId(event.target.value)}
        />
      </label>
      <label className="gc-field">
        Topic
        <input type="text" value={topic} data-testid="pubsub-create-sub-topic" readOnly />
      </label>
      <label className="gc-field">
        Acknowledgement deadline (seconds)
        <input
          type="number"
          min={10}
          max={600}
          value={ackDeadline}
          data-testid="pubsub-create-sub-ack"
          onChange={(event) => setAckDeadline(event.target.value)}
        />
        <p className="gc-field-hint">Between 10 and 600 seconds.</p>
      </label>
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the subscription.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="pubsub-create-sub-submit"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          {create.isPending ? "Creating…" : "Create"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function DeleteSubscriptionDialog({
  subscription,
  onClose,
  onDeleted,
}: {
  subscription: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const remove = useMutation({
    mutationFn: () => deletePubSubSubscription(project, subscription),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title="Delete subscription?" testId="pubsub-delete-sub-dialog" onClose={onClose}>
      <p>
        Deleting <strong>{subscription}</strong> permanently removes the subscription and any messages it
        has not yet delivered.
      </p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete the subscription.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="gc-button-primary"
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

export function PubSubPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["pubsub-topics", project] });

  const columnsWithActions: GcpColumn<PubSubTopic>[] = [
    ...topicColumns,
    {
      id: "actions",
      header: "Actions",
      cell: (row) => {
        const id = shortName(row.name);
        return (
          <span className="gc-row-actions">
            <button
              type="button"
              className="gc-button-text"
              data-testid={`pubsub-delete-${id}`}
              aria-label={`Delete ${id}`}
              onClick={() => setDeleting(id)}
            >
              Delete
            </button>
          </span>
        );
      },
      value: () => "",
    },
  ];

  return (
    <>
      <GcpResourceTable<PubSubTopic>
        title="Pub/Sub topics"
        description="Pub/Sub is a messaging service that decouples senders from receivers. A topic is the named resource publishers send messages to."
        actions={[
          { label: "Create topic", icon: "add", primary: true, testId: "pubsub-create-topic", onSelect: () => setCreating(true) },
        ]}
        columns={columnsWithActions}
        queryKey={["pubsub-topics", project]}
        queryFn={() => fetchPubSubTopics(project)}
        filterPlaceholder="Filter topics"
        resourceNoun="topics"
        empty={{
          headline: "Create a topic to start publishing",
          description: "Publishers send messages to a topic; subscriptions deliver them to your services.",
          primaryLabel: "Create topic",
          onPrimary: () => setCreating(true),
        }}
        rowKey={(row) => row.name}
      />
      {creating ? (
        <CreateTopicDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            refresh();
          }}
        />
      ) : null}
      {deleting ? (
        <DeleteTopicDialog
          topic={deleting}
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

export function PubSubTopicDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const [creatingSubscription, setCreatingSubscription] = useState(false);
  const [deletingSubscription, setDeletingSubscription] = useState<string | null>(null);

  const topic = useQuery({ queryKey: ["pubsub-topic", project, name], queryFn: () => fetchPubSubTopic(project, name) });
  // The project's subscriptions carry the topic they attach to, so the topic's
  // own subscription list is read from the full Subscription resources rather
  // than the bare names topics.subscriptions.list returns.
  const subscriptions = useQuery({
    queryKey: ["pubsub-subscriptions", project],
    queryFn: () => fetchPubSubSubscriptions(project),
    select: (all: PubSubSubscription[]) => all.filter((sub) => shortName(sub.topic ?? "") === name),
  });

  const data = topic.data;

  return (
    <>
      <div className="gc-detail-back">
        <Link to="/ui/pubsub">‹ Pub/Sub topics</Link>
      </div>
      <GcpPageHeader
        title={name}
        description="Pub/Sub topic"
        actions={[
          {
            label: "Create subscription",
            icon: "add",
            testId: "pubsub-topic-create-sub",
            onSelect: () => setCreatingSubscription(true),
          },
          { label: "Delete", testId: "pubsub-topic-delete", onSelect: () => setDeleting(true) },
        ]}
        onRefresh={() => {
          void topic.refetch();
          void subscriptions.refetch();
        }}
        refreshing={topic.isFetching || subscriptions.isFetching}
      />
      {topic.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't load this topic.</strong>{" "}
          {topic.error instanceof Error ? topic.error.message : "The simulator did not respond."}
        </div>
      ) : topic.isLoading || !data ? (
        <div className="gc-loading" role="status">Loading topic…</div>
      ) : (
        <GcpTabs
          label="Topic detail"
          tabs={[
            {
              id: "subscriptions",
              label: "Subscriptions",
              content: (
                <SubResourceTable<PubSubSubscription>
                  query={subscriptions}
                  testId="pubsub-subscriptions-table"
                  noun="subscriptions"
                  emptyHeadline="This topic has no subscriptions"
                  emptyDescription="Subscriptions attached to this topic appear here."
                  rowKey={(row) => row.name}
                  columns={[
                    { header: "Subscription ID", cell: (row) => shortName(row.name) },
                    { header: "Delivery type", cell: (row) => (row.pushConfig?.pushEndpoint ? "Push" : "Pull") },
                    {
                      header: "Ack deadline",
                      cell: (row) => (row.ackDeadlineSeconds ? `${row.ackDeadlineSeconds}s` : "—"),
                    },
                    { header: "Message retention", cell: (row) => row.messageRetentionDuration ?? "—" },
                    {
                      header: "Actions",
                      cell: (row) => (
                        <button
                          type="button"
                          className="gc-button-text"
                          data-testid={`pubsub-delete-sub-${shortName(row.name)}`}
                          aria-label={`Delete ${shortName(row.name)}`}
                          onClick={() => setDeletingSubscription(shortName(row.name))}
                        >
                          Delete
                        </button>
                      ),
                    },
                  ]}
                />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <dl className="gc-detail-grid">
                  {[
                    { label: "Topic name", value: data.name },
                    { label: "Message retention", value: data.messageRetentionDuration ?? "—" },
                    {
                      label: "Encryption key",
                      value: data.kmsKeyName ? shortName(data.kmsKeyName) : "Google-managed",
                    },
                  ].map((property) => (
                    <div className="gc-detail-pair" key={property.label}>
                      <dt>{property.label}</dt>
                      <dd>{property.value}</dd>
                    </div>
                  ))}
                </dl>
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteTopicDialog topic={name} onClose={() => setDeleting(false)} onDeleted={() => navigate("/ui/pubsub")} />
      ) : null}
      {creatingSubscription ? (
        <CreateSubscriptionDialog
          topic={name}
          onClose={() => setCreatingSubscription(false)}
          onCreated={() => {
            setCreatingSubscription(false);
            void subscriptions.refetch();
          }}
        />
      ) : null}
      {deletingSubscription ? (
        <DeleteSubscriptionDialog
          subscription={deletingSubscription}
          onClose={() => setDeletingSubscription(null)}
          onDeleted={() => {
            setDeletingSubscription(null);
            void subscriptions.refetch();
          }}
        />
      ) : null}
    </>
  );
}
