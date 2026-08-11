import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsRowLink,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createStateMachine,
  deleteStateMachine,
  fetchStateMachines,
  validateStateMachineDefinition,
  type StateMachine,
} from "../api.js";
import { StateMachineGraph } from "./StateMachineGraph.js";

// AWS Step Functions — State machines. ListStateMachines and
// DeleteStateMachine on the real Step Functions API (X-Amz-Target
// AWSStepFunctions.<Op>).

const columns: AwsColumn<StateMachine>[] = [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <AwsRowLink to={`/ui/stepfunctions/${encodeURIComponent(row.stateMachineArn)}`}>{row.name}</AwsRowLink>
    ),
    value: (row) => row.name,
  },
  { id: "type", header: "Type", cell: (row) => row.type, value: (row) => row.type },
  { id: "arn", header: "ARN", cell: (row) => row.stateMachineArn, value: (row) => row.stateMachineArn },
  {
    id: "creationDate",
    header: "Created",
    cell: (row) => formatEpoch(row.creationDate),
    value: (row) => String(row.creationDate),
  },
];

const STARTER_DEFINITION = JSON.stringify({
  Comment: "A starter workflow",
  StartAt: "Pass",
  States: {
    Pass: { Type: "Pass", Result: { message: "Hello from AWS Step Functions" }, End: true },
  },
}, null, 2);

function CreateStateMachineModal({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [type, setType] = useState<"STANDARD" | "EXPRESS">("STANDARD");
  const [roleArn, setRoleArn] = useState("");
  const [definition, setDefinition] = useState(STARTER_DEFINITION);
  let definitionValid = true;
  try {
    const parsed = JSON.parse(definition) as { StartAt?: string; States?: Record<string, unknown> };
    definitionValid = Boolean(parsed.StartAt && parsed.States?.[parsed.StartAt]);
  } catch {
    definitionValid = false;
  }
  const valid = /^[a-zA-Z0-9-_]{1,80}$/.test(name) && roleArn.startsWith("arn:") && definitionValid;
  const create = useMutation({
    mutationFn: async () => {
      const validation = await validateStateMachineDefinition(definition);
      if (validation.result !== "OK") {
        throw new Error(validation.diagnostics.map((diagnostic) => diagnostic.message).join("; "));
      }
      return createStateMachine({ name, definition, roleArn, type });
    },
    onSuccess: async (arn) => {
      await queryClient.invalidateQueries({ queryKey: ["sfn-state-machines"] });
      onClose();
      if (arn) navigate(`/ui/stepfunctions/${encodeURIComponent(arn)}`);
    },
  });
  return (
    <AwsModal
      title="Create state machine"
      size="max"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="sfn-create-state-machine-submit"
            disabled={!valid || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <div className="aws-sfn-studio">
          <div className="aws-sfn-studio-pane">
            <h3>Design</h3>
            <StateMachineGraph definition={definition} />
          </div>
          <div className="aws-sfn-studio-pane aws-sfn-definition-editor">
            <h3>Code</h3>
            <FormField
              label="Amazon States Language definition"
              errorText={definitionValid ? undefined : "Enter a valid definition with StartAt and States."}
            >
              <Textarea
                value={definition}
                onChange={(event) => setDefinition(event.detail.value)}
                rows={20}
                spellcheck={false}
                ariaLabel="Amazon States Language definition"
              />
            </FormField>
          </div>
        </div>
        <h3>Config</h3>
        <FormField label="State machine name" constraintText="1–80 letters, numbers, hyphens, or underscores.">
          <Input value={name} onChange={(event) => setName(event.detail.value)} />
        </FormField>
        <FormField label="Type" description="Standard keeps durable execution history. Express is optimized for high-volume workflows.">
          <Select
            selectedOption={{ label: type === "STANDARD" ? "Standard" : "Express", value: type }}
            options={[
              { label: "Standard", value: "STANDARD", description: "Exactly-once execution for up to one year." },
              { label: "Express", value: "EXPRESS", description: "High-volume execution for up to five minutes." },
            ]}
            onChange={(event) => setType(event.detail.selectedOption.value as "STANDARD" | "EXPRESS")}
          />
        </FormField>
        <FormField label="Execution role ARN" description="AWS Step Functions assumes this IAM role when the workflow calls resources.">
          <Input
            value={roleArn}
            onChange={(event) => setRoleArn(event.detail.value)}
            placeholder="arn:aws:iam::123456789012:role/step-functions-role"
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the state machine.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function DeleteStateMachinesModal({
  machines,
  onClose,
  clearSelection,
}: {
  machines: StateMachine[];
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const machine of machines) {
        await deleteStateMachine(machine.stateMachineArn);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["sfn-state-machines"] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={machines.length === 1 ? `Delete ${machines[0].name}?` : `Delete ${machines.length} state machines?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="sfn-delete-state-machine-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>Step Functions marks a state machine for deletion and removes it once its running executions have finished.</p>
      <ul>
        {machines.map((machine) => (
          <li key={machine.stateMachineArn}>
            <code>{machine.name}</code>
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

export function StepFunctionsPage() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<{ machines: StateMachine[]; clearSelection: () => void } | null>(null);
  return (
    <>
      <AwsResourceTable<StateMachine>
        title="State machines"
        description="AWS Step Functions state machines in this account and Region."
        columns={columns}
        queryKey={["sfn-state-machines"]}
        queryFn={fetchStateMachines}
        filterPlaceholder="Find state machines"
        emptyTitle="No state machines"
        emptyDescription="No AWS Step Functions state machines exist in this account and Region."
        rowKey={(row) => row.stateMachineArn}
        tableTestId="sfn-state-machines-table"
        errorTestId="sfn-state-machines-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton
              data-testid="sfn-view-state-machine"
              disabled={selected.length !== 1}
              onClick={() => navigate(`/ui/stepfunctions/${encodeURIComponent(selected[0].stateMachineArn)}`)}
            >
              View details
            </AwsButton>
            <AwsButton
              data-testid="sfn-delete-state-machine"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ machines: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" data-testid="sfn-create-state-machine" onClick={() => setCreating(true)}>
              Create state machine
            </AwsButton>
          </>
        )}
      />
      {creating && <CreateStateMachineModal onClose={() => setCreating(false)} />}
      {deleting && (
        <DeleteStateMachinesModal
          machines={deleting.machines}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
