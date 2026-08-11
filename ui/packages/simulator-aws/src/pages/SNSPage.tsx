import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import Textarea from "@cloudscape-design/components/textarea";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, type AwsColumn } from "../console/index.js";
import {
  createSNSTopic,
  deleteSNSTopic,
  fetchSNSSubscriptions,
  fetchSNSTopicAttributes,
  fetchSNSTopics,
  publishSNSMessage,
  setSNSTopicAttribute,
  subscribeSNSEndpoint,
  unsubscribeSNSEndpoint,
  type SNSSubscription,
  type SNSTopic,
} from "../api.js";

// Amazon Simple Notification Service (SNS) — Topics and Subscriptions.
// ListTopics, CreateTopic, DeleteTopic, and ListSubscriptions on the real SNS
// Query API (Version 2010-03-31).

const topicColumns: AwsColumn<SNSTopic>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "arn", header: "ARN", cell: (row) => row.arn, value: (row) => row.arn },
];

const subscriptionColumns: AwsColumn<SNSSubscription>[] = [
  { id: "arn", header: "Subscription ARN", cell: (row) => row.subscriptionArn, value: (row) => row.subscriptionArn },
  { id: "protocol", header: "Protocol", cell: (row) => row.protocol, value: (row) => row.protocol },
  { id: "endpoint", header: "Endpoint", cell: (row) => row.endpoint, value: (row) => row.endpoint },
  { id: "topicArn", header: "Topic ARN", cell: (row) => row.topicArn, value: (row) => row.topicArn },
];

// The topic-name shape real SNS enforces on CreateTopic: up to 256 characters
// of letters, numbers, hyphens, and underscores.
const SNS_TOPIC_NAME_PATTERN = /^[A-Za-z0-9_-]+$/;

function CreateTopicModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => createSNSTopic(name.trim()),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["sns-topics"] });
      onClose();
    },
  });
  const trimmed = name.trim();
  const valid = trimmed.length > 0 && trimmed.length <= 256 && SNS_TOPIC_NAME_PATTERN.test(trimmed);
  return (
    <AwsModal
      title="Create topic"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="sns-create-topic-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create topic"}
          </AwsButton>
        </>
      }
    >
      <p>A standard topic fans a published message out to every subscription attached to it.</p>
      <FormField label="Name" constraintText="Up to 256 characters. Letters, numbers, hyphens, and underscores.">
        <Input
          value={name}
          onChange={(event) => setName(event.detail.value)}
          nativeInputAttributes={{ "data-testid": "sns-topic-name-input" }}
        />
      </FormField>
      {create.isError && (
        <AwsErrorAlert>
          <strong>Could not create the topic.</strong>{" "}
          {create.error instanceof Error ? create.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function DeleteTopicsModal({
  topics,
  onClose,
  clearSelection,
}: {
  topics: SNSTopic[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const topic of topics) {
        await deleteSNSTopic(topic.arn);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["sns-topics"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={topics.length === 1 ? `Delete ${topics[0].name}?` : `Delete ${topics.length} topics?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="sns-delete-topic-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Deleting a topic also deletes every subscription attached to it.</p>
      <ul>
        {topics.map((topic) => (
          <li key={topic.arn}>
            <code>{topic.name}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

function ManageTopicModal({ topic, onClose }: { topic: SNSTopic; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [message, setMessage] = useState("");
  const [subject, setSubject] = useState("");
  const [protocol, setProtocol] = useState("sqs");
  const [endpoint, setEndpoint] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [deliveryPolicy, setDeliveryPolicy] = useState("");
  const attributes = useQuery({
    queryKey: ["sns-topic-attributes", topic.arn],
    queryFn: () => fetchSNSTopicAttributes(topic.arn),
  });
  useEffect(() => {
    if (!attributes.data) return;
    setDisplayName(attributes.data.DisplayName ?? "");
    setDeliveryPolicy(attributes.data.DeliveryPolicy ?? "");
  }, [attributes.data]);
  const publish = useMutation({ mutationFn: () => publishSNSMessage(topic.arn, message, subject), onSuccess: () => setMessage("") });
  const subscribe = useMutation({
    mutationFn: () => subscribeSNSEndpoint(topic.arn, protocol, endpoint),
    onSuccess: async () => {
      setEndpoint("");
      await queryClient.invalidateQueries({ queryKey: ["sns-subscriptions"] });
    },
  });
  const saveAttributes = useMutation({
    mutationFn: async () => {
      if (displayName !== (attributes.data?.DisplayName ?? "")) {
        await setSNSTopicAttribute(topic.arn, "DisplayName", displayName);
      }
      if (deliveryPolicy !== (attributes.data?.DeliveryPolicy ?? "")) {
        await setSNSTopicAttribute(topic.arn, "DeliveryPolicy", deliveryPolicy);
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["sns-topic-attributes", topic.arn] }),
  });
  const error = publish.error || subscribe.error || saveAttributes.error || attributes.error;
  return (
    <AwsModal
      title={topic.name}
      onDismiss={onClose}
      footer={<AwsButton onClick={onClose}>Close</AwsButton>}
    >
      <SpaceBetween size="l">
        <div>
          <h3>Publish message</h3>
          <SpaceBetween size="s">
            <FormField label="Subject"><Input value={subject} onChange={(event) => setSubject(event.detail.value)} /></FormField>
            <FormField label="Message"><Textarea value={message} onChange={(event) => setMessage(event.detail.value)} rows={5} /></FormField>
            <AwsButton variant="primary" disabled={!message || publish.isPending} onClick={() => publish.mutate()}>
              Publish message
            </AwsButton>
          </SpaceBetween>
        </div>
        <div>
          <h3>Create subscription</h3>
          <SpaceBetween size="s">
            <FormField label="Protocol">
              <Select
                selectedOption={{ label: protocol, value: protocol }}
                options={["sqs", "lambda", "http", "https", "email", "sms"].map((value) => ({ label: value, value }))}
                onChange={(event) => setProtocol(event.detail.selectedOption.value ?? "sqs")}
              />
            </FormField>
            <FormField label="Endpoint" description="Queue or function ARN, URL, email address, or phone number.">
              <Input value={endpoint} onChange={(event) => setEndpoint(event.detail.value)} />
            </FormField>
            <AwsButton variant="primary" disabled={!endpoint || subscribe.isPending} onClick={() => subscribe.mutate()}>
              Create subscription
            </AwsButton>
          </SpaceBetween>
        </div>
        <div>
          <h3>Topic attributes</h3>
          <SpaceBetween size="s">
            <FormField label="Display name">
              <Input value={displayName} disabled={attributes.isLoading} onChange={(event) => setDisplayName(event.detail.value)} />
            </FormField>
            <FormField label="Delivery policy" description="JSON delivery and retry policy used by this topic.">
              <Textarea
                value={deliveryPolicy}
                disabled={attributes.isLoading}
                onChange={(event) => setDeliveryPolicy(event.detail.value)}
                rows={5}
              />
            </FormField>
            <AwsButton
              variant="primary"
              disabled={attributes.isLoading || saveAttributes.isPending}
              onClick={() => saveAttributes.mutate()}
            >
              Save attributes
            </AwsButton>
          </SpaceBetween>
        </div>
        {error && <AwsErrorAlert>{error instanceof Error ? error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

export function SNSPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ topics: SNSTopic[]; clearSelection: () => void } | null>(null);
  const [managing, setManaging] = useState<SNSTopic | null>(null);
  const unsubscribe = useMutation({
    mutationFn: async (subscriptions: SNSSubscription[]) => {
      for (const subscription of subscriptions) await unsubscribeSNSEndpoint(subscription.subscriptionArn);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["sns-subscriptions"] }),
  });
  return (
    <>
      <AwsResourceTable<SNSTopic>
        title="Topics"
        description="Amazon SNS topics in this account and Region."
        columns={topicColumns}
        queryKey={["sns-topics"]}
        queryFn={fetchSNSTopics}
        filterPlaceholder="Find topics"
        emptyTitle="No topics"
        emptyDescription="No Amazon SNS topics exist in this account and Region."
        rowKey={(row) => row.arn}
        tableTestId="sns-topics-table"
        errorTestId="sns-topics-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length !== 1} onClick={() => setManaging(selected[0] ?? null)}>
              Publish and subscribe
            </AwsButton>
            <AwsButton
              data-testid="sns-delete-topic"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ topics: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="sns-create-topic" onClick={() => setCreating(true)}>
              Create topic
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<SNSSubscription>
        title="Subscriptions"
        headingVariant="h2"
        description="The endpoints subscribed to topics in this account and Region."
        columns={subscriptionColumns}
        queryKey={["sns-subscriptions"]}
        queryFn={fetchSNSSubscriptions}
        filterPlaceholder="Find subscriptions"
        emptyTitle="No subscriptions"
        emptyDescription="No subscriptions exist in this account and Region."
        rowKey={(row) => row.subscriptionArn}
        tableTestId="sns-subscriptions-table"
        errorTestId="sns-subscriptions-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              disabled={selected.length === 0 || unsubscribe.isPending}
              onClick={() => unsubscribe.mutate(selected, { onSuccess: clearSelection })}
            >
              Delete subscription
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateTopicModal onClose={() => setCreating(false)} />}
      {managing && <ManageTopicModal topic={managing} onClose={() => setManaging(null)} />}
      {deleting && (
        <DeleteTopicsModal topics={deleting.topics} clearSelection={deleting.clearSelection} onClose={() => setDeleting(null)} />
      )}
    </>
  );
}
