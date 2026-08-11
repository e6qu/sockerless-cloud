import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import {
  AwsButton,
  AwsContainer,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsStatus,
  AwsTabs,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  fetchStateMachine,
  fetchStateMachineExecution,
  fetchStateMachineExecutionHistory,
  redriveStateMachineExecution,
  stopStateMachineExecution,
} from "../api.js";
import { StateMachineGraph } from "./StateMachineGraph.js";

function JsonDocument({ value, label, testId }: { value: string; label: string; testId?: string }) {
  let formatted = value;
  try {
    formatted = JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    // AWS can carry a non-JSON cause string; preserve it verbatim.
  }
  return (
    <div data-testid={testId}>
      <Textarea readOnly value={formatted || "–"} rows={12} ariaLabel={label} spellcheck={false} />
    </div>
  );
}

export function StateMachineExecutionPage() {
  const { executionArn = "" } = useParams();
  const queryClient = useQueryClient();
  const execution = useQuery({
    queryKey: ["sfn-execution", executionArn],
    queryFn: () => fetchStateMachineExecution(executionArn),
    refetchInterval: (query) => (query.state.data?.status === "RUNNING" ? 1000 : false),
  });
  const machine = useQuery({
    queryKey: ["sfn-state-machine", execution.data?.stateMachineArn],
    queryFn: () => fetchStateMachine(execution.data!.stateMachineArn),
    enabled: Boolean(execution.data?.stateMachineArn),
  });
  const history = useQuery({
    queryKey: ["sfn-execution-history", executionArn],
    queryFn: () => fetchStateMachineExecutionHistory(executionArn),
    refetchInterval: execution.data?.status === "RUNNING" ? 1000 : false,
  });
  const stop = useMutation({
    mutationFn: () => stopStateMachineExecution(executionArn),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["sfn-execution", executionArn] }),
        queryClient.invalidateQueries({ queryKey: ["sfn-execution-history", executionArn] }),
      ]);
    },
  });
  const redrive = useMutation({
    mutationFn: () => redriveStateMachineExecution(executionArn),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["sfn-execution", executionArn] }),
        queryClient.invalidateQueries({ queryKey: ["sfn-execution-history", executionArn] }),
      ]);
    },
  });
  const activeStates = useMemo(
    () =>
      (history.data ?? [])
        .filter((event) => event.type.endsWith("StateEntered"))
        .map((event) => String(event.details.name ?? ""))
        .filter(Boolean)
        .slice(-1),
    [history.data],
  );

  return (
    <>
      <AwsPageHeader
        title={execution.data?.name || executionArn}
        description="AWS Step Functions execution details"
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <AwsButton
              data-testid="sfn-redrive-execution"
              disabled={
                !["FAILED", "TIMED_OUT", "ABORTED"].includes(execution.data?.status ?? "") || redrive.isPending
              }
              onClick={() => redrive.mutate()}
            >
              {redrive.isPending ? "Redriving…" : "Redrive"}
            </AwsButton>
            <AwsButton
              data-testid="sfn-stop-execution"
              disabled={execution.data?.status !== "RUNNING" || stop.isPending}
              onClick={() => stop.mutate()}
            >
              {stop.isPending ? "Stopping…" : "Stop execution"}
            </AwsButton>
          </SpaceBetween>
        }
      />
      {execution.isError ? (
        <AwsErrorAlert testId="sfn-execution-error">
          <strong>Could not load the execution.</strong>{" "}
          {execution.error instanceof Error ? execution.error.message : "The request failed."}
        </AwsErrorAlert>
      ) : execution.isLoading ? (
        <AwsContainer>
          <AwsEmptyState title="Loading execution…" loading />
        </AwsContainer>
      ) : execution.data ? (
        <SpaceBetween size="l">
          {stop.isError && (
            <AwsErrorAlert>
              <strong>Could not stop the execution.</strong>{" "}
              {stop.error instanceof Error ? stop.error.message : "The request failed."}
            </AwsErrorAlert>
          )}
          {redrive.isError && (
            <AwsErrorAlert>
              <strong>Could not redrive the execution.</strong>{" "}
              {redrive.error instanceof Error ? redrive.error.message : "The request failed."}
            </AwsErrorAlert>
          )}
          <AwsContainer>
            <AwsKeyValue
              ariaLabel="Execution summary"
              items={[
                { label: "Status", value: <AwsStatus status={execution.data.status} /> },
                { label: "Started", value: formatEpoch(execution.data.startDate) },
                { label: "Ended", value: execution.data.stopDate ? formatEpoch(execution.data.stopDate) : "–" },
                { label: "Execution ARN", value: execution.data.executionArn },
                { label: "State machine ARN", value: execution.data.stateMachineArn },
                ...(execution.data.error
                  ? [
                      { label: "Error", value: execution.data.error },
                      { label: "Cause", value: execution.data.cause || "–" },
                    ]
                  : []),
              ]}
            />
          </AwsContainer>
          <AwsTabs
            ariaLabel="Execution detail"
            tabs={[
              {
                id: "graph",
                label: "Graph view",
                content:
                  machine.isLoading || history.isLoading ? (
                    <AwsEmptyState title="Loading workflow…" loading />
                  ) : machine.data ? (
                    <StateMachineGraph definition={machine.data.definition} activeStates={activeStates} />
                  ) : (
                    <AwsErrorAlert>Could not load the workflow definition.</AwsErrorAlert>
                  ),
              },
              {
                id: "table",
                label: `Table view (${history.data?.length ?? 0})`,
                content: history.isError ? (
                  <AwsErrorAlert>
                    {history.error instanceof Error ? history.error.message : "Could not load execution history."}
                  </AwsErrorAlert>
                ) : (
                  <Table
                    variant="embedded"
                    loading={history.isLoading}
                    loadingText="Loading execution history"
                    items={history.data ?? []}
                    ariaLabels={{ tableLabel: "Execution event history" }}
                    columnDefinitions={[
                      { id: "id", header: "ID", cell: (event) => event.id },
                      { id: "type", header: "Type", cell: (event) => event.type },
                      { id: "timestamp", header: "Timestamp", cell: (event) => formatEpoch(event.timestamp) },
                      {
                        id: "details",
                        header: "Event details",
                        cell: (event) => (
                          <pre className="aws-sfn-event-details">{JSON.stringify(event.details, null, 2)}</pre>
                        ),
                      },
                    ]}
                    header={<Header variant="h2">Execution event history</Header>}
                    empty={<AwsEmptyState title="No events" description="This execution has no history events." />}
                  />
                ),
              },
              {
                id: "input-output",
                label: "Input and output",
                content: (
                  <SpaceBetween size="l">
                    <div>
                      <Header variant="h3">Execution input</Header>
                      <JsonDocument
                        value={execution.data.input}
                        label="Execution input"
                        testId="sfn-execution-input-document"
                      />
                    </div>
                    <div>
                      <Header variant="h3">Execution output</Header>
                      <JsonDocument
                        value={execution.data.output}
                        label="Execution output"
                        testId="sfn-execution-output-document"
                      />
                    </div>
                  </SpaceBetween>
                ),
              },
              {
                id: "definition",
                label: "Definition",
                content: <JsonDocument value={machine.data?.definition ?? ""} label="State machine definition" />,
              },
            ]}
          />
        </SpaceBetween>
      ) : null}
    </>
  );
}
