import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Dropdown, Field, Option, Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow, Text, Textarea } from "@fluentui/react-components";
import { API_VERSION } from "../api.js";
import { listProviderPage, listSubscriptions } from "../azure.js";
import { serviceById } from "../catalog.js";
import { armFetch } from "../portal/arm.js";
import { armText } from "../portal/format.js";
import { Blade, EmptyState, ErrorBar, errorText, InfoBar, ServiceMenu } from "../portal/AzurePortal.js";
import { AzureIcon } from "../portal/icons.js";
import { ActivityLogSection } from "./resource/sections.js";

const MENU = [
  { label: "Overview", to: "/ui/monitor", icon: "info" as const },
  { label: "Activity log", to: "/ui/monitor/activitylog", icon: "activity" as const },
  { label: "Logs", to: "/ui/monitor/logs", icon: "document" as const },
];

function MonitorBlade({ title, children, testid }: { title: string; children: ReactNode; testid: string }) {
  const service = serviceById("monitor");
  return (
    <Blade
      title={title === "Overview" ? "Monitor" : `Monitor | ${title}`}
      subtitle="Microsoft"
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: "Monitor" }]}
      favoriteServiceId="monitor"
      testid={testid}
    >
      <div className="az-resource-layout">
        <ServiceMenu label="Monitor menu" top={MENU} groups={[]} />
        <div className="az-resource-content">{children}</div>
      </div>
    </Blade>
  );
}

export function MonitorOverviewPage() {
  return (
    <MonitorBlade title="Overview" testid="monitor-overview">
      <InfoBar title="Azure Monitor">Collect, analyze and act on telemetry from your Azure resources.</InfoBar>
      <ul className="az-monitor-cards">
        <li>
          <AzureIcon name="activity" size={24} />
          <div>
            <Text weight="semibold" block>
              Activity log
            </Text>
            <Text size={200} block>
              Subscription-level events: who created, changed or deleted what, and when.
            </Text>
          </div>
        </li>
        <li>
          <AzureIcon name="document" size={24} />
          <div>
            <Text weight="semibold" block>
              Logs
            </Text>
            <Text size={200} block>
              Query a Log Analytics workspace with the Kusto Query Language.
            </Text>
          </div>
        </li>
      </ul>
    </MonitorBlade>
  );
}

export function MonitorActivityLogPage() {
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const [picked, setSubscription] = useState("");
  const subscription = picked || subscriptions.data?.[0]?.subscriptionId || "";
  return (
    <MonitorBlade title="Activity log" testid="monitor-activity">
      <Field label="Subscription" orientation="horizontal">
        <Dropdown
          value={subscriptions.data?.find((sub) => sub.subscriptionId === subscription)?.displayName ?? ""}
          selectedOptions={[subscription]}
          onOptionSelect={(_, data) => setSubscription(data.optionValue ?? "")}
        >
          {(subscriptions.data ?? []).map((sub) => (
            <Option key={sub.subscriptionId} value={sub.subscriptionId}>
              {sub.displayName}
            </Option>
          ))}
        </Dropdown>
      </Field>
      {subscriptions.isError ? <ErrorBar title="The subscriptions could not be listed.">{errorText(subscriptions.error)}</ErrorBar> : null}
      {subscriptions.isLoading ? <EmptyState title="Loading subscriptions…" loading /> : null}
      {subscription ? <ActivityLogSection key={subscription} scope={`/subscriptions/${subscription}`} /> : null}
    </MonitorBlade>
  );
}

interface QueryTable {
  name?: string;
  columns?: { name?: string; type?: string }[];
  rows?: unknown[][];
}

/** Logs: a Kusto query run against a workspace's own query endpoint, the Log
 *  Analytics query API. */
export function MonitorLogsPage() {
  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  const workspaces = useQuery({
    queryKey: ["workspaces", subscriptions.data?.map((sub) => sub.subscriptionId)],
    enabled: Boolean(subscriptions.data),
    queryFn: async () =>
      (
        await Promise.all(
          (subscriptions.data ?? []).map(async (sub) => (await listProviderPage(sub.subscriptionId, "Microsoft.OperationalInsights/workspaces", API_VERSION.workspaces)).value),
        )
      ).flat(),
  });
  const [picked, setWorkspace] = useState("");
  const [query, setQuery] = useState("AppTraces\n| take 100");
  const [result, setResult] = useState<QueryTable | null>(null);
  const [error, setError] = useState<string>();
  const [running, setRunning] = useState(false);
  const workspace = picked || workspaces.data?.[0]?.id || "";
  const chosen = workspaces.data?.find((entry) => entry.id === workspace);
  const run = async () => {
    if (!chosen) return;
    setRunning(true);
    setError(undefined);
    try {
      const response = await armFetch(
        `/v1/workspaces/${armText(chosen.properties, "customerId")}/query`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ query }) },
        "logs",
      );
      const body = (await response.json()) as { tables?: QueryTable[] };
      setResult(body.tables?.[0] ?? { columns: [], rows: [] });
    } catch (err) {
      setError(errorText(err));
      setResult(null);
    } finally {
      setRunning(false);
    }
  };
  return (
    <MonitorBlade title="Logs" testid="monitor-logs">
      {subscriptions.isError || workspaces.isError ? (
        <ErrorBar title="The Log Analytics workspaces could not be listed.">{errorText(subscriptions.error ?? workspaces.error)}</ErrorBar>
      ) : !workspaces.data ? (
        <EmptyState title="Loading Log Analytics workspaces…" loading />
      ) : workspaces.data.length === 0 ? (
        <EmptyState title="No Log Analytics workspaces" description="Create a Log Analytics workspace to query logs." />
      ) : (
        <>
          <div className="az-logs-bar">
            <Field label="Scope" orientation="horizontal">
              <Dropdown value={chosen?.name ?? ""} selectedOptions={[workspace]} onOptionSelect={(_, data) => setWorkspace(data.optionValue ?? "")}>
                {(workspaces.data ?? []).map((entry) => (
                  <Option key={entry.id} value={entry.id}>
                    {entry.name}
                  </Option>
                ))}
              </Dropdown>
            </Field>
            <Button appearance="primary" icon={<AzureIcon name="play" />} disabled={!chosen || running || !query.trim()} onClick={() => void run()} data-testid="logs-run">
              {running ? "Running…" : "Run"}
            </Button>
          </div>
          <Textarea className="az-kql" value={query} onChange={(_, data) => setQuery(data.value)} aria-label="Query" resize="vertical" rows={5} />
          {error ? <ErrorBar title="The query failed.">{error}</ErrorBar> : null}
          {result ? (
            <>
              <Text size={200} block className="az-records" role="status">
                {`${result.rows?.length ?? 0} results`}
              </Text>
              <div className="az-grid-wrap">
                <Table aria-label="Results" size="small" className="az-grid" data-testid="logs-results">
                  <TableHeader>
                    <TableRow>
                      {(result.columns ?? []).map((column) => (
                        <TableHeaderCell key={column.name}>{column.name}</TableHeaderCell>
                      ))}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(result.rows ?? []).map((row, index) => (
                      <TableRow key={index}>
                        {row.map((cell, position) => (
                          <TableCell key={position}>{cell == null ? "" : String(cell)}</TableCell>
                        ))}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </>
          ) : null}
        </>
      )}
    </MonitorBlade>
  );
}
