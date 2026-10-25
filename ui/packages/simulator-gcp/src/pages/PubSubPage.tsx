import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, type GcpColumn } from "../console/index.js";
import { Copyable, GcpPageHeader } from "../console/GcpConsole.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { DetailList, LoadError, Loading } from "../console/GcpResourceDetail.js";
import { ResourceTableView } from "../console/GcpTable.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { shortName } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  deletePubSubTopic,
  fetchPubSubSubscriptions,
  fetchPubSubTopic,
  fetchPubSubTopics,
  publishPubSubMessage,
  pubsubSubscriptionRequest,
  pubsubTopicRequest,
  sendRequest,
  type PubSubSubscription,
  type PubSubTopic,
} from "../api.js";

// Pub/Sub's resource-ID contract: 3–255 characters starting with a letter,
// then letters, digits and . - _ ~ + %, never the reserved "goog" prefix.
const PUBSUB_ID_PATTERN = /^[A-Za-z][A-Za-z0-9._~+%-]{2,254}$/;
export const isValidPubSubId = (id: string) => PUBSUB_ID_PATTERN.test(id) && !id.toLowerCase().startsWith("goog");

// Pub/Sub durations travel as "604800s"; the console shows them in days,
// hours and minutes.
export function formatDuration(value: string | undefined): string {
  const seconds = value ? Number(value.replace(/s$/, "")) : NaN;
  if (!Number.isFinite(seconds)) return "—";
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return [days ? `${days} day${days > 1 ? "s" : ""}` : "", hours ? `${hours} hr` : "", minutes ? `${minutes} min` : ""]
    .filter(Boolean)
    .join(" ") || `${seconds} s`;
}

function TopicName({ topic }: { topic: PubSubTopic }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(`/ui/cloudpubsub/topic/detail/${shortName(topic.name)}`)}>
      {shortName(topic.name)}
    </Link>
  );
}

const topicColumns: GcpColumn<PubSubTopic>[] = [
  { id: "id", header: "Topic ID", cell: (row) => <TopicName topic={row} />, value: (row) => shortName(row.name) },
  {
    id: "encryption",
    header: "Encryption key",
    cell: (row) => (row.kmsKeyName ? shortName(row.kmsKeyName) : "Google-managed"),
    value: (row) => row.kmsKeyName ?? "Google-managed",
  },
  { id: "name", header: "Topic name", cell: (row) => row.name, value: (row) => row.name },
  {
    id: "retention",
    header: "Retention",
    cell: (row) => formatDuration(row.messageRetentionDuration),
    value: (row) => row.messageRetentionDuration ?? "",
  },
  {
    id: "labels",
    header: "Labels",
    cell: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}: ${value}`).join(", ") || "—",
    value: (row) => Object.entries(row.labels ?? {}).map(([key, value]) => `${key}:${value}`).join(" "),
    defaultHidden: true,
  },
];

// The real Create topic dialog: the topic ID, a default subscription named
// <topic>-sub that it creates alongside, and optional message retention.
export function CreateTopicDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { project } = useProject();
  const { track } = useOperations();
  const [topicId, setTopicId] = useState("");
  const [defaultSubscription, setDefaultSubscription] = useState(true);
  const [retain, setRetain] = useState(false);
  const [retentionDays, setRetentionDays] = useState(7);
  const create = useMutation({
    mutationFn: () =>
      track(
        `Create topic "${topicId}"`,
        async () => {
          await sendRequest(pubsubTopicRequest(project, topicId, retain ? retentionDays * 86400 : undefined));
          if (defaultSubscription) {
            await sendRequest(
              pubsubSubscriptionRequest(project, `${topicId}-sub`, {
                topic: topicId,
                ackDeadlineSeconds: 10,
                retentionSeconds: 7 * 86400,
                retainAckedMessages: false,
                pushEndpoint: "",
              }),
            );
          }
        },
        `Created topic "${topicId}"`,
      ),
    onSuccess: onCreated,
  });
  return (
    <GcpDialog title="Create topic" testId="pubsub-create-topic-dialog" onClose={onClose}>
      <label className="gc-field">
        <span className="gc-field-label">Topic ID</span>
        <input type="text" value={topicId} data-testid="pubsub-create-topic-id" onChange={(event) => setTopicId(event.target.value)} />
        <span className="gc-field-hint">Topic name: projects/{project}/topics/{topicId || "<topic ID>"}</span>
      </label>
      <label className="gc-checkbox">
        <input type="checkbox" checked={defaultSubscription} onChange={(event) => setDefaultSubscription(event.target.checked)} />
        Add a default subscription
      </label>
      <label className="gc-checkbox">
        <input type="checkbox" checked={retain} onChange={(event) => setRetain(event.target.checked)} />
        Enable message retention
      </label>
      {retain ? (
        <label className="gc-field">
          <span className="gc-field-label">Days</span>
          <input type="number" min={1} max={31} value={retentionDays} onChange={(event) => setRetentionDays(Number(event.target.value))} />
        </label>
      ) : null}
      {create.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't create the topic.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
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
  topics,
  onClose,
  onDeleted,
}: {
  topics: string[];
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { project } = useProject();
  const { track } = useOperations();
  const subject = topics.length === 1 ? `topic "${topics[0]}"` : `${topics.length} topics`;
  const remove = useMutation({
    mutationFn: () =>
      track(`Delete ${subject}`, () => Promise.all(topics.map((topic) => deletePubSubTopic(project, topic))), `Deleted ${subject}`),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="pubsub-delete-topic-dialog" onClose={onClose}>
      <p>Deleting a topic removes it permanently. Its subscriptions are detached and stop receiving messages.</p>
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

export function PubSubPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string[] | null>(null);
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["pubsub-topics", project] });
    void queryClient.invalidateQueries({ queryKey: ["pubsub-subscriptions", project] });
  };
  return (
    <>
      <GcpResourceTable<PubSubTopic>
        title="Topics"
        actions={[{ label: "Create topic", icon: "add", testId: "pubsub-create-topic", onSelect: () => setCreating(true) }]}
        selectionActions={(selected) => [
          { label: "Delete", icon: "delete", onSelect: () => setDeleting(selected.map((row) => shortName(row.name))) },
        ]}
        rowActions={(row) => [{ label: "Delete", onSelect: () => setDeleting([shortName(row.name)]) }]}
        columns={topicColumns}
        queryKey={["pubsub-topics", project]}
        queryFn={() => fetchPubSubTopics(project)}
        resourceNoun="topics"
        testId="pubsub-topics-table"
        empty={{
          headline: "No topics",
          description: "Publishers send messages to a topic; subscriptions deliver them to your services.",
        }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
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
          topics={deleting}
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

function PublishMessageDialog({ topic, onClose }: { topic: string; onClose: () => void }) {
  const { project } = useProject();
  const { track } = useOperations();
  const [body, setBody] = useState("");
  const [attributes, setAttributes] = useState<LabelPair[]>([]);
  const publish = useMutation({
    mutationFn: () =>
      track(`Publish message to "${topic}"`, () => publishPubSubMessage(project, topic, body, pairsToLabels(attributes)), `Published message to "${topic}"`),
    onSuccess: onClose,
  });
  return (
    <GcpDialog title="Publish message" testId="pubsub-publish-dialog" onClose={onClose}>
      <label className="gc-field">
        <span className="gc-field-label">Message body</span>
        <textarea rows={4} value={body} data-testid="pubsub-publish-body" onChange={(event) => setBody(event.target.value)} />
      </label>
      <LabelsEditor pairs={attributes} onChange={setAttributes} idPrefix="pubsub-publish" title="Message attributes" addLabel="Add an attribute" entryNoun="Attribute" />
      {publish.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't publish the message.</strong>{" "}
          {publish.error instanceof Error ? publish.error.message : "The API did not respond."}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="gc-button-primary"
          data-testid="pubsub-publish-submit"
          disabled={!body || publish.isPending}
          onClick={() => publish.mutate()}
        >
          {publish.isPending ? "Publishing…" : "Publish"}
        </button>
      </div>
    </GcpDialog>
  );
}

function SubscriptionName({ subscription }: { subscription: PubSubSubscription }) {
  const path = useProjectPath();
  return (
    <Link className="gc-cell-link" to={path(`/ui/cloudpubsub/subscription/detail/${shortName(subscription.name)}`)}>
      {shortName(subscription.name)}
    </Link>
  );
}

export function PubSubTopicDetailPage() {
  const { name = "" } = useParams();
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const [deleting, setDeleting] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const topic = useQuery({ queryKey: ["pubsub-topic", project, name], queryFn: () => fetchPubSubTopic(project, name) });
  const subscriptions = useQuery({
    queryKey: ["pubsub-subscriptions", project],
    queryFn: () => fetchPubSubSubscriptions(project),
    select: (all: PubSubSubscription[]) => all.filter((sub) => shortName(sub.topic ?? "") === name),
  });
  const data = topic.data;

  return (
    <>
      <GcpPageHeader
        backTo="/ui/cloudpubsub/topic/list"
        title={name}
        actions={[
          {
            label: "Create subscription",
            icon: "add",
            testId: "pubsub-topic-create-sub",
            to: `/ui/cloudpubsub/subscription/create?topic=${encodeURIComponent(name)}`,
          },
          { label: "Publish message", icon: "send", testId: "pubsub-topic-publish", onSelect: () => setPublishing(true) },
          { label: "Delete", icon: "delete", testId: "pubsub-topic-delete", onSelect: () => setDeleting(true) },
        ]}
        onRefresh={() => {
          void topic.refetch();
          void subscriptions.refetch();
        }}
        refreshing={topic.isFetching || subscriptions.isFetching}
      />
      {topic.isError ? (
        <LoadError error={topic.error} noun="this topic" />
      ) : topic.isLoading || !data ? (
        <Loading noun="topic" />
      ) : (
        <GcpTabs
          label="Topic details"
          tabs={[
            {
              id: "subscriptions",
              label: "Subscriptions",
              content: (
                <ResourceTableView<PubSubSubscription>
                  rows={subscriptions.data}
                  isLoading={subscriptions.isLoading}
                  error={subscriptions.error}
                  tableId="pubsub-subscriptions-table"
                  resourceNoun="subscriptions"
                  rowKey={(row) => row.name}
                  columns={[
                    { id: "id", header: "Subscription ID", cell: (row) => <SubscriptionName subscription={row} />, value: (row) => shortName(row.name) },
                    {
                      id: "delivery",
                      header: "Delivery type",
                      cell: (row) => (row.pushConfig?.pushEndpoint ? "Push" : "Pull"),
                      value: (row) => (row.pushConfig?.pushEndpoint ? "Push" : "Pull"),
                    },
                    {
                      id: "ack",
                      header: "Acknowledgement deadline",
                      cell: (row) => (row.ackDeadlineSeconds ? `${row.ackDeadlineSeconds} s` : "—"),
                      value: (row) => String(row.ackDeadlineSeconds ?? ""),
                    },
                  ]}
                  empty={{ headline: "No subscriptions", description: "Subscriptions attached to this topic appear here." }}
                />
              ),
            },
            {
              id: "details",
              label: "Details",
              content: (
                <DetailList
                  properties={[
                    { label: "Topic name", value: <Copyable value={data.name} /> },
                    { label: "Message retention", value: formatDuration(data.messageRetentionDuration) },
                    { label: "Encryption key", value: data.kmsKeyName ? shortName(data.kmsKeyName) : "Google-managed" },
                  ]}
                />
              ),
            },
          ]}
        />
      )}
      {deleting ? (
        <DeleteTopicDialog topics={[name]} onClose={() => setDeleting(false)} onDeleted={() => navigate(path("/ui/cloudpubsub/topic/list"))} />
      ) : null}
      {publishing ? <PublishMessageDialog topic={name} onClose={() => setPublishing(false)} /> : null}
    </>
  );
}
