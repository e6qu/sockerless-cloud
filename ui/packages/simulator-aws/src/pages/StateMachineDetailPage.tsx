import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import Table from "@cloudscape-design/components/table";
import { fontFamilyMonospace } from "@cloudscape-design/design-tokens";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsModal,
  AwsPageHeader,
  AwsResourceTable,
  AwsRowLink,
  AwsStatus,
  AwsTabs,
  removedKeys,
  TagsEditorModal,
  type AwsColumn,
  type AwsTab,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  fetchStateMachine,
  fetchStateMachineAliases,
  fetchStateMachineExecutions,
  fetchStateMachineTags,
  fetchStateMachineVersions,
  createStateMachineAlias,
  deleteStateMachineAlias,
  deleteStateMachineVersion,
  publishStateMachineVersion,
  startStateMachineExecution,
  tagStateMachineResource,
  untagStateMachineResource,
  updateStateMachine,
  updateStateMachineAlias,
  validateStateMachineDefinition,
  testStateMachineState,
  type StateMachineAlias,
  type StateMachineTestResult,
  type StateMachineExecution,
} from "../api.js";
import { DeleteStateMachinesModal } from "./StepFunctionsPage.js";
import { StateMachineGraph } from "./StateMachineGraph.js";

// AWS Step Functions — State machine detail. DescribeStateMachine for the
// summary and the definition, ListExecutions for the executions table, and
// StartExecution for the "Start execution" action — the same operations the
// real console's state machine page drives.

const executionColumns = (stateMachineArn: string): AwsColumn<StateMachineExecution>[] => [
  {
    id: "name",
    header: "Name",
    cell: (row) => (
      <AwsRowLink
        to={`/ui/stepfunctions/${encodeURIComponent(stateMachineArn)}/executions/${encodeURIComponent(row.executionArn)}`}
      >
        {row.name}
      </AwsRowLink>
    ),
    value: (row) => row.name,
  },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  {
    id: "startDate",
    header: "Started",
    cell: (row) => formatEpoch(row.startDate),
    value: (row) => String(row.startDate),
  },
  {
    id: "stopDate",
    header: "Ended",
    cell: (row) => (row.stopDate ? formatEpoch(row.stopDate) : "–"),
    value: (row) => String(row.stopDate),
  },
];

function StartExecutionModal({ stateMachineArn, onClose }: { stateMachineArn: string; onClose: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [input, setInput] = useState("{}");
  const [executionName, setExecutionName] = useState("");
  const start = useMutation({
    mutationFn: () => startStateMachineExecution(stateMachineArn, input, executionName || undefined),
    onSuccess: async (executionArn) => {
      await queryClient.invalidateQueries({ queryKey: ["sfn-executions", stateMachineArn] });
      onClose();
      if (executionArn) {
        navigate(
          `/ui/stepfunctions/${encodeURIComponent(stateMachineArn)}/executions/${encodeURIComponent(executionArn)}`,
        );
      }
    },
  });
  let inputIsJson = true;
  try {
    JSON.parse(input);
  } catch {
    inputIsJson = false;
  }
  return (
    <AwsModal
      title="Start execution"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="sfn-start-execution-submit"
            disabled={!inputIsJson || start.isPending}
            onClick={() => start.mutate()}
          >
            {start.isPending ? "Starting…" : "Start execution"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField label="Execution name" description="Optional. AWS Step Functions generates a UUID when omitted.">
          <Input value={executionName} onChange={(event) => setExecutionName(event.detail.value)} />
        </FormField>
        <FormField
          label="Input"
          constraintText="A JSON document passed to the state machine's first state."
          errorText={inputIsJson ? undefined : "Input must be a JSON document."}
        >
          <Textarea
            value={input}
            rows={6}
            onChange={(event) => setInput(event.detail.value)}
            ariaLabel="Execution input"
            spellcheck={false}
            data-testid="sfn-execution-input"
          />
        </FormField>
        {start.isError && (
          <AwsErrorAlert>
            <strong>Could not start the execution.</strong>{" "}
            {start.error instanceof Error ? start.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function EditStateMachineModal({
  machine,
  onClose,
}: {
  machine: { stateMachineArn: string; definition: string; roleArn: string };
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [definition, setDefinition] = useState(machine.definition);
  const [roleArn, setRoleArn] = useState(machine.roleArn);
  let definitionValid = true;
  try {
    const parsed = JSON.parse(definition) as { StartAt?: string; States?: Record<string, unknown> };
    definitionValid = Boolean(parsed.StartAt && parsed.States?.[parsed.StartAt]);
  } catch {
    definitionValid = false;
  }
  const update = useMutation({
    mutationFn: async () => {
      const validation = await validateStateMachineDefinition(definition);
      if (validation.result !== "OK") {
        throw new Error(validation.diagnostics.map((diagnostic) => diagnostic.message).join("; "));
      }
      return updateStateMachine(machine.stateMachineArn, definition, roleArn);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["sfn-state-machine", machine.stateMachineArn] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Edit state machine"
      size="max"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            disabled={!definitionValid || !roleArn.startsWith("arn:") || update.isPending}
            onClick={() => update.mutate()}
          >
            {update.isPending ? "Saving…" : "Save"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="l">
        <div className="aws-sfn-studio">
          <div className="aws-sfn-studio-pane">
            <Header variant="h3">Design</Header>
            <StateMachineGraph definition={definition} />
          </div>
          <div className="aws-sfn-studio-pane aws-sfn-definition-editor">
            <FormField
              label="Amazon States Language definition"
              errorText={definitionValid ? undefined : "Enter a valid definition with StartAt and States."}
            >
              <Textarea
                value={definition}
                rows={22}
                spellcheck={false}
                onChange={(event) => setDefinition(event.detail.value)}
                ariaLabel="Amazon States Language definition"
              />
            </FormField>
          </div>
        </div>
        <FormField label="Execution role ARN">
          <Input value={roleArn} onChange={(event) => setRoleArn(event.detail.value)} />
        </FormField>
        {update.isError && <AwsErrorAlert>{update.error instanceof Error ? update.error.message : "Update failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function PublishStateMachineModal({ stateMachineArn, onClose }: { stateMachineArn: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [description, setDescription] = useState("");
  const publish = useMutation({
    mutationFn: () => publishStateMachineVersion(stateMachineArn, description),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["sfn-state-machine-versions", stateMachineArn] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Publish version"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton variant="primary" disabled={publish.isPending} onClick={() => publish.mutate()}>
            {publish.isPending ? "Publishing…" : "Publish"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>Publish an immutable version of this workflow definition and configuration.</p>
        <FormField label="Version description">
          <Input value={description} onChange={(event) => setDescription(event.detail.value)} />
        </FormField>
        {publish.isError && <AwsErrorAlert>{publish.error instanceof Error ? publish.error.message : "Publish failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function CreateStateMachineAliasModal({
  versions,
  current,
  onClose,
  stateMachineArn,
}: {
  versions: { stateMachineVersionArn: string; version: number }[];
  current?: StateMachineAlias;
  onClose: () => void;
  stateMachineArn: string;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(current?.name ?? "");
  const [description, setDescription] = useState(current?.description ?? "");
  const [versionArn, setVersionArn] = useState(
    current?.routingConfiguration[0]?.stateMachineVersionArn ?? versions.at(-1)?.stateMachineVersionArn ?? "",
  );
  const [secondVersionArn, setSecondVersionArn] = useState(
    current?.routingConfiguration[1]?.stateMachineVersionArn ?? "",
  );
  const [firstWeight, setFirstWeight] = useState(String(current?.routingConfiguration[0]?.weight ?? 100));
  const weight = Number(firstWeight);
  const routingConfiguration = secondVersionArn
    ? [
        { stateMachineVersionArn: versionArn, weight },
        { stateMachineVersionArn: secondVersionArn, weight: 100 - weight },
      ]
    : [{ stateMachineVersionArn: versionArn, weight: 100 }];
  const save = useMutation({
    mutationFn: () =>
      current
        ? updateStateMachineAlias(current.stateMachineAliasArn, description, routingConfiguration)
        : createStateMachineAlias(name, versionArn, description),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["sfn-state-machine-aliases", stateMachineArn] });
      onClose();
    },
  });
  return (
    <AwsModal
      title={current ? `Edit ${current.name}` : "Create alias"}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            disabled={
              !/^[a-zA-Z0-9-_]{1,80}$/.test(name) ||
              !versionArn ||
              (Boolean(secondVersionArn) &&
                (secondVersionArn === versionArn || !Number.isInteger(weight) || weight < 1 || weight > 99)) ||
              save.isPending
            }
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : current ? "Save" : "Create"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField label="Alias name">
          <Input disabled={Boolean(current)} value={name} onChange={(event) => setName(event.detail.value)} />
        </FormField>
        {current && (
          <>
            <FormField label="Optional second version" description="Route a percentage of executions to another version.">
              <Select
                selectedOption={
                  secondVersionArn
                    ? {
                        label: String(
                          versions.find((version) => version.stateMachineVersionArn === secondVersionArn)?.version ?? "",
                        ),
                        value: secondVersionArn,
                      }
                    : null
                }
                options={[
                  { label: "No second version", value: "" },
                  ...versions.map((version) => ({
                    label: String(version.version),
                    value: version.stateMachineVersionArn,
                  })),
                ]}
                onChange={(event) => setSecondVersionArn(event.detail.selectedOption.value ?? "")}
                placeholder="No second version"
              />
            </FormField>
            {secondVersionArn && (
              <FormField
                label="Primary version traffic weight"
                description={`The second version receives ${100 - (Number(firstWeight) || 0)}%.`}
              >
                <Input type="number" value={firstWeight} onChange={(event) => setFirstWeight(event.detail.value)} />
              </FormField>
            )}
          </>
        )}
        <FormField label="State machine version">
          <Select
            selectedOption={
              versionArn
                ? { label: String(versions.find((version) => version.stateMachineVersionArn === versionArn)?.version ?? ""), value: versionArn }
                : null
            }
            options={versions.map((version) => ({
              label: String(version.version),
              value: version.stateMachineVersionArn,
            }))}
            onChange={(event) => setVersionArn(event.detail.selectedOption.value ?? "")}
            placeholder="Choose a published version"
          />
        </FormField>
        <FormField label="Description">
          <Input value={description} onChange={(event) => setDescription(event.detail.value)} />
        </FormField>
        {versions.length === 0 && <AwsErrorAlert>Publish a state machine version before creating an alias.</AwsErrorAlert>}
        {save.isError && <AwsErrorAlert>{save.error instanceof Error ? save.error.message : "Save failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function formattedDefinition(definition: string): string {
  try {
    return JSON.stringify(JSON.parse(definition), null, 2);
  } catch {
    return definition;
  }
}

function TestStateModal({
  definition,
  onClose,
}: {
  definition: string;
  onClose: () => void;
}) {
  const [stateName, setStateName] = useState("");
  const [input, setInput] = useState("{}");
  const [result, setResult] = useState<StateMachineTestResult | null>(null);
  const test = useMutation({
    mutationFn: () => testStateMachineState(definition, input, stateName),
    onSuccess: setResult,
  });
  let inputValid = true;
  try {
    JSON.parse(input);
  } catch {
    inputValid = false;
  }
  return (
    <AwsModal
      title="Test state"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Close</AwsButton>
          <AwsButton
            variant="primary"
            disabled={!inputValid || test.isPending}
            onClick={() => test.mutate()}
          >
            {test.isPending ? "Testing…" : "Test state"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField label="State name" description="Leave empty to test the workflow's StartAt state.">
          <Input value={stateName} onChange={(event) => setStateName(event.detail.value)} />
        </FormField>
        <FormField label="State input" errorText={inputValid ? undefined : "Input must be valid JSON."}>
          <Textarea value={input} rows={7} spellcheck={false} onChange={(event) => setInput(event.detail.value)} />
        </FormField>
        {test.isError && (
          <AwsErrorAlert>{test.error instanceof Error ? test.error.message : "The state test failed."}</AwsErrorAlert>
        )}
        {result && (
          <AwsContainer>
            <AwsKeyValue
              items={[
                { label: "Status", value: <AwsStatus status={result.status} /> },
                { label: "Next state", value: result.nextState || "–" },
                ...(result.error
                  ? [
                      { label: "Error", value: result.error },
                      { label: "Cause", value: result.cause || "–" },
                    ]
                  : [{ label: "Output", value: <pre>{formattedDefinition(result.output)}</pre> }]),
              ]}
            />
          </AwsContainer>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function StateMachineDetailPage() {
  const { stateMachineArn = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [starting, setStarting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [creatingAlias, setCreatingAlias] = useState(false);
  const [editingAlias, setEditingAlias] = useState<StateMachineAlias | null>(null);
  const [testingState, setTestingState] = useState(false);
  const [editingTags, setEditingTags] = useState(false);
  const machine = useQuery({
    queryKey: ["sfn-state-machine", stateMachineArn],
    queryFn: () => fetchStateMachine(stateMachineArn),
  });
  const versions = useQuery({
    queryKey: ["sfn-state-machine-versions", stateMachineArn],
    queryFn: () => fetchStateMachineVersions(stateMachineArn),
  });
  const aliases = useQuery({
    queryKey: ["sfn-state-machine-aliases", stateMachineArn],
    queryFn: () => fetchStateMachineAliases(stateMachineArn),
  });
  const tags = useQuery({
    queryKey: ["sfn-state-machine-tags", stateMachineArn],
    queryFn: () => fetchStateMachineTags(stateMachineArn),
  });
  const removeAlias = useMutation({
    mutationFn: deleteStateMachineAlias,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["sfn-state-machine-aliases", stateMachineArn] }),
  });
  const removeVersion = useMutation({
    mutationFn: deleteStateMachineVersion,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["sfn-state-machine-versions", stateMachineArn] }),
  });

  const tabs: AwsTab[] = machine.data
    ? [
        {
          id: "graph",
          label: "Graph",
          content: (
            <SpaceBetween size="m">
              <Header
                variant="h3"
                actions={<AwsButton onClick={() => setTestingState(true)}>Test state</AwsButton>}
              >
                Workflow
              </Header>
              <StateMachineGraph definition={machine.data.definition} />
            </SpaceBetween>
          ),
        },
        {
          id: "definition",
          label: "Definition",
          content: (
            <pre style={{ fontFamily: fontFamilyMonospace, margin: 0, whiteSpace: "pre-wrap" }}>
              {formattedDefinition(machine.data.definition)}
            </pre>
          ),
        },
        {
          id: "versions",
          label: `Versions (${versions.data?.length ?? 0})`,
          content: versions.isError ? (
            <AwsErrorAlert>Could not load state machine versions.</AwsErrorAlert>
          ) : (
            <Table
              variant="embedded"
              loading={versions.isLoading}
              loadingText="Loading versions"
              items={versions.data ?? []}
              ariaLabels={{ tableLabel: "State machine versions" }}
              header={
                <Header
                  variant="h3"
                  actions={<AwsButton onClick={() => setPublishing(true)}>Publish version</AwsButton>}
                >
                  Versions
                </Header>
              }
              empty={<AwsEmptyState title="No published versions" description="This state machine has no published versions." />}
              columnDefinitions={[
                { id: "version", header: "Version", cell: (version) => version.version },
                { id: "arn", header: "ARN", cell: (version) => version.stateMachineVersionArn },
                { id: "created", header: "Created", cell: (version) => formatEpoch(version.creationDate) },
                {
                  id: "actions",
                  header: "Actions",
                  cell: (version) => (
                    <AwsButton
                      disabled={removeVersion.isPending}
                      onClick={() => removeVersion.mutate(version.stateMachineVersionArn)}
                    >
                      Delete
                    </AwsButton>
                  ),
                },
              ]}
            />
          ),
        },
        {
          id: "aliases",
          label: `Aliases (${aliases.data?.length ?? 0})`,
          content: aliases.isError ? (
            <AwsErrorAlert>Could not load state machine aliases.</AwsErrorAlert>
          ) : (
            <Table
              variant="embedded"
              loading={aliases.isLoading}
              loadingText="Loading aliases"
              items={aliases.data ?? []}
              ariaLabels={{ tableLabel: "State machine aliases" }}
              header={
                <Header
                  variant="h3"
                  actions={<AwsButton onClick={() => setCreatingAlias(true)}>Create alias</AwsButton>}
                >
                  Aliases
                </Header>
              }
              empty={<AwsEmptyState title="No aliases" description="This state machine has no aliases." />}
              columnDefinitions={[
                { id: "name", header: "Name", cell: (alias) => alias.name },
                { id: "arn", header: "ARN", cell: (alias) => alias.stateMachineAliasArn },
                {
                  id: "routing",
                  header: "Routing",
                  cell: (alias) =>
                    alias.routingConfiguration
                      .map((route) => `${route.stateMachineVersionArn.slice(route.stateMachineVersionArn.lastIndexOf(":") + 1)} (${route.weight}%)`)
                      .join(", "),
                },
                { id: "description", header: "Description", cell: (alias) => alias.description || "–" },
                { id: "updated", header: "Updated", cell: (alias) => formatEpoch(alias.updateDate) },
                {
                  id: "actions",
                  header: "Actions",
                  cell: (alias) => (
                    <SpaceBetween direction="horizontal" size="xs">
                      <AwsButton onClick={() => setEditingAlias(alias)}>Edit</AwsButton>
                      <AwsButton
                        disabled={removeAlias.isPending}
                        onClick={() => removeAlias.mutate(alias.stateMachineAliasArn)}
                      >
                        Delete
                      </AwsButton>
                    </SpaceBetween>
                  ),
                },
              ]}
            />
          ),
        },
        {
          id: "tags",
          label: `Tags (${Object.keys(tags.data ?? {}).length})`,
          content: tags.isError ? (
            <AwsErrorAlert>Could not load state machine tags.</AwsErrorAlert>
          ) : (
            <Table
              variant="embedded"
              loading={tags.isLoading}
              loadingText="Loading tags"
              items={Object.entries(tags.data ?? {}).map(([key, value]) => ({ key, value }))}
              ariaLabels={{ tableLabel: "State machine tags" }}
              header={
                <Header
                  variant="h3"
                  actions={<AwsButton onClick={() => setEditingTags(true)}>Manage tags</AwsButton>}
                >
                  Tags
                </Header>
              }
              empty={<AwsEmptyState title="No tags" description="This state machine has no tags." />}
              columnDefinitions={[
                { id: "key", header: "Key", cell: (tag) => tag.key },
                { id: "value", header: "Value", cell: (tag) => tag.value },
              ]}
            />
          ),
        },
      ]
    : [];

  return (
    <>
      <AwsPageHeader
        title={machine.data?.name || stateMachineArn}
        description="AWS Step Functions state machine in this account and Region."
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton
              data-testid="sfn-state-machine-start"
              disabled={!machine.isSuccess}
              onClick={() => setStarting(true)}
            >
              Start execution
            </AwsButton>
            <AwsButton disabled={!machine.isSuccess} onClick={() => setEditing(true)}>
              Edit
            </AwsButton>
            <AwsButton disabled={!machine.isSuccess} onClick={() => setPublishing(true)}>
              Publish version
            </AwsButton>
            <AwsButton
              data-testid="sfn-state-machine-delete"
              disabled={!machine.isSuccess}
              onClick={() => setDeleting(true)}
            >
              Delete
            </AwsButton>
          </SpaceBetween>
        }
      />
      <AwsContainer>
        {machine.isError ? (
          <AwsErrorAlert testId="sfn-state-machine-error">
            <strong>Could not load the state machine.</strong>{" "}
            {machine.error instanceof Error ? machine.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : machine.isLoading ? (
          <AwsEmptyState title="Loading state machine…" loading />
        ) : machine.data ? (
          <div data-testid="sfn-state-machine-summary">
            <AwsKeyValue
              ariaLabel="State machine details"
              items={[
                { label: "Status", value: <AwsStatus status={machine.data.status} /> },
                { label: "Type", value: machine.data.type },
                { label: "IAM role", value: machine.data.roleArn || "–" },
                { label: "Created", value: formatEpoch(machine.data.creationDate) },
                { label: "ARN", value: machine.data.stateMachineArn },
              ]}
            />
            <div style={{ marginTop: 20 }}>
              <AwsTabs ariaLabel="State machine detail" tabs={tabs} />
            </div>
          </div>
        ) : null}
      </AwsContainer>
      <AwsResourceTable<StateMachineExecution>
        title="Executions"
        headingVariant="h2"
        description="Executions of this state machine."
        columns={executionColumns(stateMachineArn)}
        queryKey={["sfn-executions", stateMachineArn]}
        queryFn={() => fetchStateMachineExecutions(stateMachineArn)}
        filterPlaceholder="Find executions"
        emptyTitle="No executions"
        emptyDescription="This state machine has not been executed yet."
        rowKey={(row) => row.executionArn}
        tableTestId="sfn-executions-table"
        errorTestId="sfn-executions-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {starting && <StartExecutionModal stateMachineArn={stateMachineArn} onClose={() => setStarting(false)} />}
      {editing && machine.data && <EditStateMachineModal machine={machine.data} onClose={() => setEditing(false)} />}
      {publishing && machine.data && (
        <PublishStateMachineModal stateMachineArn={stateMachineArn} onClose={() => setPublishing(false)} />
      )}
      {creatingAlias && machine.data && (
        <CreateStateMachineAliasModal
          stateMachineArn={stateMachineArn}
          versions={versions.data ?? []}
          onClose={() => setCreatingAlias(false)}
        />
      )}
      {editingAlias && machine.data && (
        <CreateStateMachineAliasModal
          stateMachineArn={stateMachineArn}
          versions={versions.data ?? []}
          current={editingAlias}
          onClose={() => setEditingAlias(null)}
        />
      )}
      {testingState && machine.data && (
        <TestStateModal definition={machine.data.definition} onClose={() => setTestingState(false)} />
      )}
      {editingTags && machine.data && (
        <TagsEditorModal
          title="Manage tags"
          intro={`Tags applied to the ${machine.data.name} state machine.`}
          initialTags={tags.data ?? {}}
          testIdPrefix="sfn-state-machine"
          onClose={() => setEditingTags(false)}
          onSaved={() => queryClient.invalidateQueries({ queryKey: ["sfn-state-machine-tags", stateMachineArn] })}
          save={async (next) => {
            const remove = removedKeys(tags.data ?? {}, next);
            if (Object.keys(next).length > 0) await tagStateMachineResource(stateMachineArn, next);
            if (remove.length > 0) await untagStateMachineResource(stateMachineArn, remove);
          }}
        />
      )}
      {deleting && machine.data && (
        <DeleteStateMachinesModal
          machines={[machine.data]}
          clearSelection={() => navigate("/ui/stepfunctions")}
          onClose={() => setDeleting(false)}
        />
      )}
    </>
  );
}
