import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import Textarea from "@cloudscape-design/components/textarea";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, AwsStatus, type AwsColumn } from "../console/index.js";
import {
  createEventBus,
  deleteEventBus,
  deleteEventBridgeRule,
  fetchEventBridgeTargets,
  fetchEventBridgeRules,
  fetchEventBuses,
  putEventBridgeEvent,
  putEventBridgeRule,
  putEventBridgeTarget,
  removeEventBridgeTarget,
  setEventBridgeRuleState,
  type EventBridgeRule,
  type EventBus,
} from "../api.js";

// Amazon EventBridge — Rules and Event buses. ListRules, EnableRule,
// DisableRule, DeleteRule, and ListEventBuses on the real EventBridge API
// (X-Amz-Target AWSEvents.<Op>).

const ruleColumns: AwsColumn<EventBridgeRule>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "state", header: "Status", cell: (row) => <AwsStatus status={row.state} />, value: (row) => row.state },
  { id: "eventBusName", header: "Event bus", cell: (row) => row.eventBusName, value: (row) => row.eventBusName },
  {
    id: "scheduleExpression",
    header: "Schedule",
    cell: (row) => row.scheduleExpression || "–",
    value: (row) => row.scheduleExpression,
  },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
];

const busColumns: AwsColumn<EventBus>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "arn", header: "ARN", cell: (row) => row.arn, value: (row) => row.arn },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
];

function DeleteRulesModal({
  rules,
  onClose,
  clearSelection,
}: {
  rules: EventBridgeRule[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const rule of rules) {
        await deleteEventBridgeRule(rule.name, rule.eventBusName);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["eventbridge-rules"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={rules.length === 1 ? `Delete ${rules[0].name}?` : `Delete ${rules.length} rules?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="eventbridge-delete-rule-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>A rule with targets still attached must have them removed before EventBridge will delete it.</p>
      <ul>
        {rules.map((rule) => (
          <li key={rule.name}>
            <code>{rule.name}</code>
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

function CreateRuleModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [pattern, setPattern] = useState('{"source":["custom.application"]}');
  const [schedule, setSchedule] = useState("");
  const [eventBusName, setEventBusName] = useState("default");
  const create = useMutation({
    mutationFn: () => putEventBridgeRule({ name, eventBusName, description, eventPattern: schedule ? "" : pattern, scheduleExpression: schedule }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["eventbridge-rules"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create rule"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton variant="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Create rule</AwsButton>
        </>
      }
    >
      <SpaceBetween size="s">
        <FormField label="Name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
        <FormField label="Description"><Input value={description} onChange={(event) => setDescription(event.detail.value)} /></FormField>
        <FormField label="Event bus name"><Input value={eventBusName} onChange={(event) => setEventBusName(event.detail.value)} /></FormField>
        <FormField label="Event pattern" description="Used when no schedule expression is set.">
          <Textarea value={pattern} onChange={(event) => setPattern(event.detail.value)} rows={5} />
        </FormField>
        <FormField label="Schedule expression" description="Optional rate(...) or cron(...).">
          <Input value={schedule} onChange={(event) => setSchedule(event.detail.value)} />
        </FormField>
        {create.isError && <AwsErrorAlert>{create.error instanceof Error ? create.error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function ManageRuleModal({ rule, onClose }: { rule: EventBridgeRule; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [targetId, setTargetId] = useState("target-1");
  const [targetArn, setTargetArn] = useState("");
  const [targetInput, setTargetInput] = useState("");
  const targets = useQuery({
    queryKey: ["eventbridge-targets", rule.eventBusName, rule.name],
    queryFn: () => fetchEventBridgeTargets(rule.name, rule.eventBusName),
  });
  const add = useMutation({
    mutationFn: () => putEventBridgeTarget(rule.name, { id: targetId, arn: targetArn, roleArn: "", input: targetInput, inputPath: "" }, rule.eventBusName),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["eventbridge-targets", rule.eventBusName, rule.name] }),
  });
  const remove = useMutation({
    mutationFn: (id: string) => removeEventBridgeTarget(rule.name, id, rule.eventBusName),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["eventbridge-targets", rule.eventBusName, rule.name] }),
  });
  return (
    <AwsModal title={`Targets for ${rule.name}`} onDismiss={onClose} footer={<AwsButton onClick={onClose}>Close</AwsButton>}>
      <SpaceBetween size="m">
        {(targets.data ?? []).map((target) => (
          <div key={target.id}>
            <strong>{target.id}</strong><br /><code>{target.arn}</code>{" "}
            <AwsButton disabled={remove.isPending} onClick={() => remove.mutate(target.id)}>Remove</AwsButton>
          </div>
        ))}
        {targets.isSuccess && targets.data.length === 0 && <p>No targets are attached.</p>}
        <FormField label="Target ID"><Input value={targetId} onChange={(event) => setTargetId(event.detail.value)} /></FormField>
        <FormField label="Target ARN" description="AWS Lambda, Amazon SQS, Amazon SNS, AWS Step Functions, or CloudWatch Logs ARN.">
          <Input value={targetArn} onChange={(event) => setTargetArn(event.detail.value)} />
        </FormField>
        <FormField label="Constant JSON input" description="Optional. EventBridge sends the matched event when omitted.">
          <Textarea value={targetInput} onChange={(event) => setTargetInput(event.detail.value)} rows={4} />
        </FormField>
        <AwsButton variant="primary" disabled={!targetId || !targetArn || add.isPending} onClick={() => add.mutate()}>
          Add target
        </AwsButton>
      </SpaceBetween>
    </AwsModal>
  );
}

function SendEventModal({ onClose }: { onClose: () => void }) {
  const [source, setSource] = useState("custom.application");
  const [detailType, setDetailType] = useState("Application event");
  const [detail, setDetail] = useState('{"message":"hello"}');
  const [eventBusName, setEventBusName] = useState("default");
  const send = useMutation({ mutationFn: () => putEventBridgeEvent({ source, detailType, detail, eventBusName }) });
  return (
    <AwsModal title="Send event" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Close</AwsButton><AwsButton variant="primary" disabled={send.isPending} onClick={() => send.mutate()}>Send event</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Source"><Input value={source} onChange={(event) => setSource(event.detail.value)} /></FormField>
        <FormField label="Detail type"><Input value={detailType} onChange={(event) => setDetailType(event.detail.value)} /></FormField>
        <FormField label="Event bus name"><Input value={eventBusName} onChange={(event) => setEventBusName(event.detail.value)} /></FormField>
        <FormField label="Detail JSON"><Textarea value={detail} onChange={(event) => setDetail(event.detail.value)} rows={6} /></FormField>
        {send.isSuccess && <p>Event accepted with ID <code>{send.data}</code>.</p>}
        {send.isError && <AwsErrorAlert>{send.error instanceof Error ? send.error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function CreateEventBusModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => createEventBus(name, description),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["eventbridge-buses"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create event bus" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Create event bus</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
        <FormField label="Description"><Input value={description} onChange={(event) => setDescription(event.detail.value)} /></FormField>
        {create.isError && <AwsErrorAlert>{create.error instanceof Error ? create.error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

export function EventBridgePage() {
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<{ rules: EventBridgeRule[]; clearSelection: () => void } | null>(null);
  const [creating, setCreating] = useState(false);
  const [managing, setManaging] = useState<EventBridgeRule | null>(null);
  const [sending, setSending] = useState(false);
  const [creatingBus, setCreatingBus] = useState(false);
  const removeBuses = useMutation({
    mutationFn: async (buses: EventBus[]) => {
      for (const bus of buses) await deleteEventBus(bus.name);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["eventbridge-buses"] }),
  });
  const setState = useMutation({
    mutationFn: async ({ rules, enabled }: { rules: EventBridgeRule[]; enabled: boolean }) => {
      for (const rule of rules) {
        await setEventBridgeRuleState(rule.name, enabled, rule.eventBusName);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["eventbridge-rules"] }),
  });
  return (
    <>
      <AwsResourceTable<EventBridgeRule>
        title="Rules"
        description="EventBridge rules in this account and Region."
        columns={ruleColumns}
        queryKey={["eventbridge-rules"]}
        queryFn={fetchEventBridgeRules}
        filterPlaceholder="Find rules"
        emptyTitle="No rules"
        emptyDescription="No EventBridge rules exist in this account and Region."
        rowKey={(row) => row.arn || row.name}
        tableTestId="eventbridge-rules-table"
        errorTestId="eventbridge-rules-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length !== 1} onClick={() => setManaging(selected[0] ?? null)}>Manage targets</AwsButton>
            <AwsButton
              data-testid="eventbridge-enable-rule"
              disabled={selected.length === 0 || setState.isPending}
              onClick={() => setState.mutate({ rules: selected, enabled: true })}
            >
              Enable
            </AwsButton>
            <AwsButton
              data-testid="eventbridge-disable-rule"
              disabled={selected.length === 0 || setState.isPending}
              onClick={() => setState.mutate({ rules: selected, enabled: false })}
            >
              Disable
            </AwsButton>
            <AwsButton
              data-testid="eventbridge-delete-rule"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ rules: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton onClick={() => setSending(true)}>Send event</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create rule</AwsButton>
          </>
        )}
      />
      {setState.isError && (
        <AwsErrorAlert testId="eventbridge-rule-state-error">
          <strong>Could not change the rule state.</strong>{" "}
          {setState.error instanceof Error ? setState.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
      <AwsResourceTable<EventBus>
        title="Event buses"
        headingVariant="h2"
        description="The event buses rules match events on."
        columns={busColumns}
        queryKey={["eventbridge-buses"]}
        queryFn={fetchEventBuses}
        filterPlaceholder="Find event buses"
        emptyTitle="No event buses"
        emptyDescription="No EventBridge event buses exist in this account and Region."
        rowKey={(row) => row.arn || row.name}
        tableTestId="eventbridge-buses-table"
        errorTestId="eventbridge-buses-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              disabled={selected.length === 0 || selected.some((bus) => bus.name === "default") || removeBuses.isPending}
              onClick={() => removeBuses.mutate(selected, { onSuccess: clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" onClick={() => setCreatingBus(true)}>Create event bus</AwsButton>
          </>
        )}
      />
      {deleting && (
        <DeleteRulesModal rules={deleting.rules} clearSelection={deleting.clearSelection} onClose={() => setDeleting(null)} />
      )}
      {creating && <CreateRuleModal onClose={() => setCreating(false)} />}
      {managing && <ManageRuleModal rule={managing} onClose={() => setManaging(null)} />}
      {sending && <SendEventModal onClose={() => setSending(false)} />}
      {creatingBus && <CreateEventBusModal onClose={() => setCreatingBus(false)} />}
    </>
  );
}
