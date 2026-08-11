import { authorizedFetch, authorizedJSON, authorizedJSONPost } from "./portal/federation.js";

// The portal reads the real Azure Resource Manager and Azure Monitor APIs over
// federated, Microsoft Entra-issued credentials, rendering the true resource
// shapes rather than a hand-picked subset.

interface ArmList<T> {
  value?: T[];
}

interface ArmResource {
  id?: string;
  name?: string;
  location?: string;
  type?: string;
  kind?: string;
}

// API versions for each provider — the ones the real Azure Resource Manager
// serves these list operations at.
export const API_VERSION = {
  subscriptions: "2022-12-01",
  // Microsoft.Subscription — the alias (subscription creation) API and the
  // cancel/rename/enable subscription actions.
  subscriptionAliases: "2021-10-01",
  jobs: "2024-03-01",
  sites: "2023-12-01",
  registries: "2023-07-01",
  storage: "2023-01-01",
  workspaces: "2022-10-01",
  // Microsoft.Resources/resourceGroups — the api-version armresources'
  // ResourceGroupsClient sends (go-azure-sdk / azurerm provider both pin the
  // same stable version).
  resourceGroups: "2021-04-01",
} as const;

// API versions for the provider surfaces the service blades read. Each is the
// version the real Azure Resource Manager serves that provider's list/get at,
// and the one the vendored Azure REST specification for it is published under.
export const BLADE_API_VERSION = {
  virtualMachines: "2022-03-01",
  network: "2025-03-01",
  dns: "2018-05-01",
  privateDns: "2024-06-01",
  keyVault: "2023-07-01",
  redis: "2024-11-01",
  postgresql: "2025-08-01",
  cosmos: "2024-08-15",
  serviceBus: "2021-11-01",
  serviceBusNamespaces: "2024-01-01",
  eventHub: "2024-01-01",
  eventGrid: "2022-06-15",
  apiManagement: "2022-08-01",
  logic: "2019-05-01",
  applicationInsights: "2020-02-02",
  containerInstance: "2021-10-01",
  managedIdentity: "2024-11-30",
} as const;

// Azure resources live under subscriptions; the portal enumerates them the way
// the real portal does before listing a provider's resources across them.
export async function subscriptionIds(): Promise<string[]> {
  const list = await authorizedJSON<ArmList<{ subscriptionId?: string }>>(
    `/subscriptions?api-version=${API_VERSION.subscriptions}`,
  );
  return (list.value ?? []).map((s) => s.subscriptionId ?? "").filter(Boolean);
}

// --- Subscriptions blade: Azure Resource Manager subscriptions + the
// Microsoft.Subscription alias API (programmatic subscription creation) ---

export interface Subscription {
  id: string;
  subscriptionId: string;
  displayName: string;
  state: string;
}

interface ArmSubscription {
  id?: string;
  subscriptionId?: string;
  displayName?: string;
  state?: string;
}

function subscriptionOf(sub: ArmSubscription): Subscription {
  return {
    id: sub.id ?? "",
    subscriptionId: sub.subscriptionId ?? "",
    displayName: sub.displayName ?? "",
    state: sub.state ?? "",
  };
}

// A resource's ARM tags map. Every ARM resource carries the same `tags`
// top-level object; the portal's reusable tags editor reads and writes it the
// same way for every resource type.
export type ResourceTags = Record<string, string>;

// armSend writes to an Azure Resource Manager path and surfaces ARM's own
// error message rather than masking it.
async function armSend(path: string, init: RequestInit = {}): Promise<Response> {
  const response = await authorizedFetch(path, "arm", init);
  if (!response.ok) {
    let detail = "";
    try {
      const body = (await response.json()) as { error?: { message?: string } };
      detail = body.error?.message ?? "";
    } catch {
      // A non-JSON error body keeps the HTTP status as the message.
    }
    throw new Error(`${path} returned HTTP ${response.status}${detail ? `: ${detail}` : ""}`);
  }
  return response;
}

export const fetchSubscriptions = async (): Promise<Subscription[]> => {
  const list = await authorizedJSON<ArmList<ArmSubscription>>(
    `/subscriptions?api-version=${API_VERSION.subscriptions}`,
  );
  return (list.value ?? []).map(subscriptionOf);
};

export const fetchSubscription = async (subscriptionId: string): Promise<Subscription> =>
  subscriptionOf(
    await authorizedJSON<ArmSubscription>(
      `/subscriptions/${subscriptionId}?api-version=${API_VERSION.subscriptions}`,
    ),
  );

export interface SubscriptionAlias {
  name: string;
  subscriptionId: string;
  provisioningState: string;
}

interface ArmSubscriptionAlias {
  name?: string;
  properties?: { subscriptionId?: string; provisioningState?: string };
}

function subscriptionAliasOf(alias: ArmSubscriptionAlias): SubscriptionAlias {
  return {
    name: alias.name ?? "",
    subscriptionId: alias.properties?.subscriptionId ?? "",
    provisioningState: alias.properties?.provisioningState ?? "",
  };
}

// createSubscriptionAlias drives the real subscription-creation flow the way
// the real portal (and `az account alias create`) does: a PUT on the
// Microsoft.Subscription alias with the display name, workload, and billing
// scope. The response reports provisioningState "Accepted" until the
// subscription materializes; getSubscriptionAlias is the poll.
export const createSubscriptionAlias = async (
  displayName: string,
  billingScope: string,
): Promise<SubscriptionAlias> => {
  const aliasName = crypto.randomUUID();
  const response = await armSend(
    `/providers/Microsoft.Subscription/aliases/${aliasName}?api-version=${API_VERSION.subscriptionAliases}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        properties: { displayName, workload: "Production", billingScope },
      }),
    },
  );
  return subscriptionAliasOf((await response.json()) as ArmSubscriptionAlias);
};

export const getSubscriptionAlias = async (aliasName: string): Promise<SubscriptionAlias> =>
  subscriptionAliasOf(
    await authorizedJSON<ArmSubscriptionAlias>(
      `/providers/Microsoft.Subscription/aliases/${aliasName}?api-version=${API_VERSION.subscriptionAliases}`,
    ),
  );

// cancelSubscription / enableSubscription are the real Microsoft.Subscription
// actions: a cancelled subscription becomes Disabled (it is never deleted —
// Azure has no subscription-delete API) and can be re-enabled.
export const cancelSubscription = async (subscriptionId: string): Promise<void> => {
  await armSend(
    `/subscriptions/${subscriptionId}/providers/Microsoft.Subscription/cancel?api-version=${API_VERSION.subscriptionAliases}`,
    { method: "POST" },
  );
};

export const enableSubscription = async (subscriptionId: string): Promise<void> => {
  await armSend(
    `/subscriptions/${subscriptionId}/providers/Microsoft.Subscription/enable?api-version=${API_VERSION.subscriptionAliases}`,
    { method: "POST" },
  );
};

// ensureResourceGroup PUTs the resource group a new resource is created
// into — idempotent, the same call `az group create` (or Terraform's
// azurerm_resource_group) issues before creating a resource inside it. Real
// Azure Resource Manager rejects a resource create against a resource group
// that doesn't already exist; the console creates it the way any real client
// does rather than assuming one is already there.
async function ensureResourceGroup(subscriptionId: string, resourceGroup: string, location: string): Promise<void> {
  await armSend(
    `/subscriptions/${subscriptionId}/resourceGroups/${resourceGroup}?api-version=${API_VERSION.resourceGroups}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ location }),
    },
  );
}

// armPatch merges a partial body into an existing resource through ARM's real
// PATCH verb — the same request `az resource update` and the vendor SDKs'
// BeginUpdate send. Every resource type below settles it synchronously (HTTP
// 200 with the merged resource), so the console awaits the response directly.
async function armPatch(id: string, apiVersion: string, body: unknown): Promise<Response> {
  return armSend(`${id}?api-version=${apiVersion}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

// updateResourceTags writes a resource's `tags` map through its real ARM PATCH
// — every ARM resource carries the same top-level `tags` object, and the real
// portal's "Tags" blade edits it with exactly this partial PATCH. The caller
// passes the resource-type api-version the tags PATCH is served at (the same
// one its GET/PUT use). The Function App site is the one exception (its PATCH
// handler needs httpsOnly preserved) — see updateFunctionAppTags.
export const updateResourceTags = async (id: string, apiVersion: string, tags: ResourceTags): Promise<void> => {
  await armPatch(id, apiVersion, { tags });
};

async function listAcrossSubscriptions<T extends ArmResource>(provider: string, apiVersion: string): Promise<T[]> {
  const subs = await subscriptionIds();
  const pages = await Promise.all(
    subs.map((sub) =>
      authorizedJSON<ArmList<T>>(`/subscriptions/${sub}/providers/${provider}?api-version=${apiVersion}`),
    ),
  );
  return pages.flatMap((page) => page.value ?? []);
}

export interface ContainerAppJob {
  id: string;
  name: string;
  location: string;
  type: string;
}

export const fetchContainerAppJobs = async (): Promise<ContainerAppJob[]> => {
  const jobs = await listAcrossSubscriptions<ArmResource>("Microsoft.App/jobs", API_VERSION.jobs);
  return jobs.map((job) => ({
    id: job.id ?? "",
    name: job.name ?? "",
    location: job.location ?? "",
    type: job.type ?? "Microsoft.App/jobs",
  }));
};

export interface FunctionSite {
  id: string;
  name: string;
  location: string;
  kind: string;
}

export const fetchFunctionSites = async (): Promise<FunctionSite[]> => {
  const sites = await listAcrossSubscriptions<ArmResource>("Microsoft.Web/sites", API_VERSION.sites);
  return sites.map((site) => ({
    id: site.id ?? "",
    name: site.name ?? "",
    location: site.location ?? "",
    kind: site.kind ?? "",
  }));
};

export interface ACRRegistry {
  id: string;
  name: string;
  location: string;
}

export const fetchACRRegistries = async (): Promise<ACRRegistry[]> => {
  const registries = await listAcrossSubscriptions<ArmResource>(
    "Microsoft.ContainerRegistry/registries",
    API_VERSION.registries,
  );
  return registries.map((registry) => ({
    id: registry.id ?? "",
    name: registry.name ?? "",
    location: registry.location ?? "",
  }));
};

// deleteACRRegistry drives the real Microsoft.ContainerRegistry registry
// DELETE — the same request `az acr delete` and the azurerm provider's
// destroy both send. The simulator settles this synchronously (HTTP 200 when
// the registry existed, 204 when it didn't — ARM's own idempotent-delete
// shape) rather than as a polled LRO, so the console awaits the DELETE
// response directly.
export const deleteACRRegistry = async (id: string): Promise<void> => {
  await armSend(`${id}?api-version=${API_VERSION.registries}`, { method: "DELETE" });
};

export interface CreateACRRegistryInput {
  subscriptionId: string;
  resourceGroup: string;
  name: string;
  location: string;
  skuName: string;
}

// createACRRegistry drives the real Microsoft.ContainerRegistry registry PUT
// — the same request `az acr create` and the azurerm provider's
// azurerm_container_registry resource send. The simulator settles this
// synchronously (HTTP 200, provisioningState "Succeeded" — go-azure-sdk
// treats the PUT itself as the completed operation) rather than as a polled
// Azure-AsyncOperation LRO, so the console awaits the PUT response directly
// instead of polling.
export const createACRRegistry = async (input: CreateACRRegistryInput): Promise<ACRRegistry> => {
  await ensureResourceGroup(input.subscriptionId, input.resourceGroup, input.location);
  const response = await armSend(
    `/subscriptions/${input.subscriptionId}/resourceGroups/${input.resourceGroup}/providers/Microsoft.ContainerRegistry/registries/${input.name}?api-version=${API_VERSION.registries}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ location: input.location, sku: { name: input.skuName } }),
    },
  );
  const registry = (await response.json()) as ArmResource;
  return { id: registry.id ?? "", name: registry.name ?? "", location: registry.location ?? "" };
};

export interface StorageAccount {
  id: string;
  name: string;
  location: string;
  kind: string;
}

export const fetchStorageAccounts = async (): Promise<StorageAccount[]> => {
  const accounts = await listAcrossSubscriptions<ArmResource>(
    "Microsoft.Storage/storageAccounts",
    API_VERSION.storage,
  );
  return accounts.map((account) => ({
    id: account.id ?? "",
    name: account.name ?? "",
    location: account.location ?? "",
    kind: account.kind ?? "",
  }));
};

// deleteStorageAccount drives the real Microsoft.Storage storage-account
// DELETE — the same request `az storage account delete` and the azurerm
// provider's destroy both send. Synchronous on this simulator (HTTP 200 when
// the account existed, 204 when it didn't), so the console awaits the
// DELETE response directly.
export const deleteStorageAccount = async (id: string): Promise<void> => {
  await armSend(`${id}?api-version=${API_VERSION.storage}`, { method: "DELETE" });
};

export interface CreateStorageAccountInput {
  subscriptionId: string;
  resourceGroup: string;
  name: string;
  location: string;
  skuName: string;
  kind: string;
}

// createStorageAccount drives the real Microsoft.Storage storage-account PUT
// — the same request `az storage account create` and the azurerm provider's
// azurerm_storage_account resource send. A real storage-account create on
// Azure itself runs as a polled Azure-AsyncOperation long-running PUT, but
// this simulator settles it synchronously (HTTP 200, provisioningState
// "Succeeded" — the comment on the sim's own handler notes go-azure-sdk
// treats it as a completed sync LRO), so the console awaits the PUT response
// directly rather than polling a provisioningState.
export const createStorageAccount = async (input: CreateStorageAccountInput): Promise<StorageAccount> => {
  await ensureResourceGroup(input.subscriptionId, input.resourceGroup, input.location);
  const response = await armSend(
    `/subscriptions/${input.subscriptionId}/resourceGroups/${input.resourceGroup}/providers/Microsoft.Storage/storageAccounts/${input.name}?api-version=${API_VERSION.storage}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        location: input.location,
        sku: { name: input.skuName },
        kind: input.kind,
      }),
    },
  );
  const account = (await response.json()) as ArmResource;
  return { id: account.id ?? "", name: account.name ?? "", location: account.location ?? "", kind: account.kind ?? "" };
};

// resourceIdByName resolves a resource's real ARM resource ID from its short
// name. ARM's per-resource Get operations need the full
// subscription/resourceGroup-scoped path, which a route carrying only the
// resource's name doesn't have; this reads the same subscription-wide list
// the listing blade already reads (a real ARM List call) and returns the
// matching resource's own `id`, so the detail blade's subsequent Get reads
// the exact resource ARM would read for that ID.
async function resourceIdByName(provider: string, apiVersion: string, name: string): Promise<string> {
  const matches = await listAcrossSubscriptions<ArmResource>(provider, apiVersion);
  const match = matches.find((resource) => resource.name === name);
  if (!match?.id) {
    throw new Error(`${provider}/${name} was not found in any subscription this directory can reach.`);
  }
  return match.id;
}

// --- Container App job detail: containers + execution history ---
//
// The Container App jobs blade this console lists (ContainerAppsPage) reads
// the real Microsoft.App/jobs resource — Azure Container Apps' run-to-completion
// model, the one sockerless deploys container tasks onto. The sim also
// implements the separate Microsoft.App/containerApps ("Apps") resource
// type, which is where `latestRevisionName` / `latestRevisionFqdn` live — it
// has its own descriptor-driven blade (services.ts's "Container Apps" entry,
// at /ui/containerapps) rather than reusing this one, because a container app
// and a container app job are different ARM resource types with different
// shapes (revisions and ingress vs. trigger configuration and executions).
// This detail blade stays on the resource type the list page already shows:
// its own real Essentials (provisioning state, trigger type, replica timeout,
// environment), its own containers (from the job's template), and its own run
// history (the real executions list) — the Container Apps Jobs equivalent of
// a Cloud Run job's executions.

export interface ContainerAppJobEnvVar {
  name: string;
  value: string;
}

export interface ContainerAppJobContainer {
  name: string;
  image: string;
  command: string[];
  args: string[];
  env: ContainerAppJobEnvVar[];
}

export interface ContainerAppJobDetail {
  id: string;
  name: string;
  location: string;
  tags: ResourceTags;
  provisioningState: string;
  environmentId: string;
  triggerType: string;
  replicaTimeout: number;
  replicaRetryLimit: number;
  // parallelism / cronExpression live under the trigger-type-specific config
  // block (manualTriggerConfig / scheduleTriggerConfig / eventTriggerConfig).
  // The detail surfaces them flattened so the edit form can round-trip them
  // back into the right block on a full-resource PUT.
  parallelism: number;
  cronExpression: string;
  containers: ContainerAppJobContainer[];
}

interface ArmTriggerConfig {
  parallelism?: number;
  replicaCompletionCount?: number;
  cronExpression?: string;
}

interface ArmContainerAppJob {
  id?: string;
  name?: string;
  location?: string;
  tags?: ResourceTags;
  properties?: {
    provisioningState?: string;
    environmentId?: string;
    configuration?: {
      triggerType?: string;
      replicaTimeout?: number;
      replicaRetryLimit?: number;
      manualTriggerConfig?: ArmTriggerConfig;
      scheduleTriggerConfig?: ArmTriggerConfig;
      eventTriggerConfig?: ArmTriggerConfig;
    };
    template?: {
      containers?: {
        name?: string;
        image?: string;
        command?: string[];
        args?: string[];
        env?: { name?: string; value?: string }[];
      }[];
    };
  };
}

function containerAppJobOf(job: ArmContainerAppJob): ContainerAppJobDetail {
  const configuration = job.properties?.configuration;
  const trigger =
    configuration?.manualTriggerConfig ??
    configuration?.scheduleTriggerConfig ??
    configuration?.eventTriggerConfig;
  return {
    id: job.id ?? "",
    name: job.name ?? "",
    location: job.location ?? "",
    tags: job.tags ?? {},
    provisioningState: job.properties?.provisioningState ?? "",
    environmentId: job.properties?.environmentId ?? "",
    triggerType: configuration?.triggerType ?? "",
    replicaTimeout: configuration?.replicaTimeout ?? 0,
    replicaRetryLimit: configuration?.replicaRetryLimit ?? 0,
    parallelism: trigger?.parallelism ?? 0,
    cronExpression: configuration?.scheduleTriggerConfig?.cronExpression ?? "",
    containers: (job.properties?.template?.containers ?? []).map((container) => ({
      name: container.name ?? "",
      image: container.image ?? "",
      command: container.command ?? [],
      args: container.args ?? [],
      env: (container.env ?? []).map((entry) => ({ name: entry.name ?? "", value: entry.value ?? "" })),
    })),
  };
}

export const fetchContainerAppJob = async (name: string): Promise<ContainerAppJobDetail> => {
  const id = await resourceIdByName("Microsoft.App/jobs", API_VERSION.jobs, name);
  return containerAppJobOf(await authorizedJSON<ArmContainerAppJob>(`${id}?api-version=${API_VERSION.jobs}`));
};

export interface ContainerAppJobExecution {
  name: string;
  status: string;
  startTime: string;
  endTime: string;
}

interface ArmContainerAppJobExecution {
  name?: string;
  properties?: { status?: string; startTime?: string; endTime?: string };
}

export const fetchContainerAppJobExecutions = async (jobId: string): Promise<ContainerAppJobExecution[]> => {
  const list = await authorizedJSON<ArmList<ArmContainerAppJobExecution>>(
    `${jobId}/executions?api-version=${API_VERSION.jobs}`,
  );
  return (list.value ?? []).map((execution) => ({
    name: execution.name ?? "",
    status: execution.properties?.status ?? "",
    startTime: execution.properties?.startTime ?? "",
    endTime: execution.properties?.endTime ?? "",
  }));
};

// startContainerAppJobExecution / stopContainerAppJobExecutions drive the
// real Container Apps Jobs start/stop actions — the same POSTs
// `az containerapp job start` / `az containerapp job stop-execution --all`
// issue.
export const startContainerAppJobExecution = async (jobId: string): Promise<void> => {
  await armSend(`${jobId}/start?api-version=${API_VERSION.jobs}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
};

export const stopContainerAppJobExecutions = async (jobId: string): Promise<void> => {
  await armSend(`${jobId}/stop?api-version=${API_VERSION.jobs}`, { method: "POST" });
};

// deleteContainerAppJob drives the real Microsoft.App/jobs DELETE — the same
// request `az containerapp job delete` and the azurerm provider's destroy
// both send, which also removes the job's execution history. Synchronous on
// this simulator (HTTP 200 when the job existed, 204 when it didn't), so the
// console awaits the DELETE response directly.
export const deleteContainerAppJob = async (jobId: string): Promise<void> => {
  await armSend(`${jobId}?api-version=${API_VERSION.jobs}`, { method: "DELETE" });
};

// --- Container Apps job create + config update (Microsoft.App/jobs PUT) ---

export const containerAppTriggerTypes = ["Manual", "Schedule", "Event"] as const;

export interface ContainerAppJobConfigInput {
  triggerType: string;
  replicaTimeout: number;
  replicaRetryLimit: number;
  parallelism: number;
  cronExpression: string;
  containers: ContainerAppJobContainer[];
}

// jobBody assembles the Microsoft.App/jobs resource body a create or a
// full-resource update PUTs. The parallelism (and cron, for Schedule triggers)
// belong in the trigger-type-specific config block the real armappcontainers
// v3 model nests them under — the same shape `az containerapp job create` and
// the azurerm_container_app_job resource send.
function jobBody(location: string, environmentId: string, config: ContainerAppJobConfigInput): unknown {
  const triggerBlock =
    config.triggerType === "Schedule"
      ? {
          scheduleTriggerConfig: {
            parallelism: config.parallelism,
            replicaCompletionCount: config.parallelism,
            cronExpression: config.cronExpression,
          },
        }
      : config.triggerType === "Event"
        ? { eventTriggerConfig: { parallelism: config.parallelism, replicaCompletionCount: config.parallelism } }
        : { manualTriggerConfig: { parallelism: config.parallelism, replicaCompletionCount: config.parallelism } };
  return {
    location,
    properties: {
      environmentId,
      configuration: {
        triggerType: config.triggerType,
        replicaTimeout: config.replicaTimeout,
        replicaRetryLimit: config.replicaRetryLimit,
        ...triggerBlock,
      },
      template: {
        containers: config.containers.map((container) => ({
          name: container.name,
          image: container.image,
          ...(container.command.length ? { command: container.command } : {}),
          ...(container.args.length ? { args: container.args } : {}),
          ...(container.env.length ? { env: container.env } : {}),
        })),
      },
    },
  };
}

// ensureManagedEnvironment PUTs the Container Apps managed environment a job
// runs in — idempotent, the same call `az containerapp env create` issues
// before a job that references it. Real Azure Resource Manager requires the
// environment to exist for a job to run in it; the console creates it the way
// any real client does. Returns the environment's ARM resource ID for the
// job's environmentId.
async function ensureManagedEnvironment(
  subscriptionId: string,
  resourceGroup: string,
  envName: string,
  location: string,
): Promise<string> {
  const response = await armSend(
    `/subscriptions/${subscriptionId}/resourceGroups/${resourceGroup}/providers/Microsoft.App/managedEnvironments/${envName}?api-version=${API_VERSION.jobs}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ location, properties: {} }),
    },
  );
  const env = (await response.json()) as ArmResource;
  return env.id ?? "";
}

export interface CreateContainerAppJobInput {
  subscriptionId: string;
  resourceGroup: string;
  name: string;
  location: string;
  environmentName: string;
  config: ContainerAppJobConfigInput;
}

// createContainerAppJob drives the real Microsoft.App/jobs PUT — the same
// request `az containerapp job create` and the azurerm_container_app_job
// resource send. It provisions the scoping resource group and managed
// environment first (the same order a real client uses), then PUTs the job.
// The simulator settles the PUT synchronously (HTTP 200/201, provisioningState
// "Succeeded"), so the console awaits the PUT response directly.
export const createContainerAppJob = async (input: CreateContainerAppJobInput): Promise<ContainerAppJob> => {
  await ensureResourceGroup(input.subscriptionId, input.resourceGroup, input.location);
  const environmentId = await ensureManagedEnvironment(
    input.subscriptionId,
    input.resourceGroup,
    input.environmentName,
    input.location,
  );
  const response = await armSend(
    `/subscriptions/${input.subscriptionId}/resourceGroups/${input.resourceGroup}/providers/Microsoft.App/jobs/${input.name}?api-version=${API_VERSION.jobs}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(jobBody(input.location, environmentId, input.config)),
    },
  );
  const job = (await response.json()) as ArmResource;
  return {
    id: job.id ?? "",
    name: job.name ?? "",
    location: job.location ?? "",
    type: job.type ?? "Microsoft.App/jobs",
  };
};

// updateContainerAppJob re-PUTs the whole job with the edited configuration and
// template — the full-resource update the way the create does. A Container Apps
// job PUT replaces the resource, so the caller passes the preserved
// environmentId/triggerType alongside the edited replica timeout, parallelism,
// and container image/env, and the console awaits the synchronous PUT response.
export const updateContainerAppJob = async (
  id: string,
  location: string,
  environmentId: string,
  config: ContainerAppJobConfigInput,
): Promise<void> => {
  await armSend(`${id}?api-version=${API_VERSION.jobs}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(jobBody(location, environmentId, config)),
  });
};

// --- Function App detail: app settings + functions ---

export interface FunctionAppDetail {
  id: string;
  name: string;
  location: string;
  kind: string;
  tags: ResourceTags;
  state: string;
  defaultHostName: string;
  httpsOnly: boolean;
  enabled: boolean;
}

interface ArmSite {
  id?: string;
  name?: string;
  location?: string;
  kind?: string;
  tags?: ResourceTags;
  properties?: { state?: string; defaultHostName?: string; httpsOnly?: boolean; enabled?: boolean };
}

function functionAppOf(site: ArmSite): FunctionAppDetail {
  return {
    id: site.id ?? "",
    name: site.name ?? "",
    location: site.location ?? "",
    kind: site.kind ?? "",
    tags: site.tags ?? {},
    state: site.properties?.state ?? "",
    defaultHostName: site.properties?.defaultHostName ?? "",
    httpsOnly: site.properties?.httpsOnly ?? false,
    enabled: site.properties?.enabled ?? false,
  };
}

export const fetchFunctionApp = async (name: string): Promise<FunctionAppDetail> => {
  const id = await resourceIdByName("Microsoft.Web/sites", API_VERSION.sites, name);
  return functionAppOf(await authorizedJSON<ArmSite>(`${id}?api-version=${API_VERSION.sites}`));
};

// deleteFunctionApp drives the real Microsoft.Web/sites DELETE — the same
// request `az functionapp delete` and the azurerm provider's destroy both
// send, which also removes the app's deployed functions. Synchronous on this
// simulator (HTTP 200 when the site existed, 204 when it didn't), so the
// console awaits the DELETE response directly.
export const deleteFunctionApp = async (id: string): Promise<void> => {
  await armSend(`${id}?api-version=${API_VERSION.sites}`, { method: "DELETE" });
};

// start/stop/restartFunctionApp drive the real Microsoft.Web/sites lifecycle
// actions — the same POSTs `az functionapp start` / `stop` / `restart` issue,
// each transitioning the site's state (start → Running, stop → Stopped) or
// cycling it (restart). Synchronous on this simulator.
export const startFunctionApp = async (id: string): Promise<void> => {
  await armSend(`${id}/start?api-version=${API_VERSION.sites}`, { method: "POST" });
};

export const stopFunctionApp = async (id: string): Promise<void> => {
  await armSend(`${id}/stop?api-version=${API_VERSION.sites}`, { method: "POST" });
};

export const restartFunctionApp = async (id: string): Promise<void> => {
  await armSend(`${id}/restart?api-version=${API_VERSION.sites}`, { method: "POST" });
};

// updateFunctionAppTags writes a Function App's tags through the real
// Microsoft.Web/sites PATCH. The site PATCH merges tags but reads httpsOnly
// unconditionally, so the console re-sends the current httpsOnly to preserve
// it (exactly what the real portal's Tags blade does — it PATCHes the object
// it read) rather than letting a tags-only edit clear it.
export const updateFunctionAppTags = async (
  id: string,
  tags: ResourceTags,
  httpsOnly: boolean,
): Promise<void> => {
  await armPatch(id, API_VERSION.sites, { tags, properties: { httpsOnly } });
};

export const functionAppRuntimes = ["dotnet", "node", "python", "java", "powershell"] as const;

export interface CreateFunctionAppInput {
  subscriptionId: string;
  resourceGroup: string;
  name: string;
  location: string;
  runtime: string;
  planName: string;
}

// createFunctionApp drives the real Microsoft.Web/sites PUT — the same request
// `az functionapp create` and the azurerm_linux_function_app resource send. It
// provisions the scoping resource group and the App Service (hosting) plan
// first, then PUTs the site referencing the plan, carrying the selected
// runtime as the FUNCTIONS_WORKER_RUNTIME app setting the real create sends.
// Synchronous (HTTP 200, state "Running"), so the console awaits the PUT.
export const createFunctionApp = async (input: CreateFunctionAppInput): Promise<FunctionSite> => {
  await ensureResourceGroup(input.subscriptionId, input.resourceGroup, input.location);
  const planResponse = await armSend(
    `/subscriptions/${input.subscriptionId}/resourceGroups/${input.resourceGroup}/providers/Microsoft.Web/serverfarms/${input.planName}?api-version=${API_VERSION.sites}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ location: input.location, sku: { name: "Y1", tier: "Dynamic" }, properties: {} }),
    },
  );
  const plan = (await planResponse.json()) as ArmResource;
  const response = await armSend(
    `/subscriptions/${input.subscriptionId}/resourceGroups/${input.resourceGroup}/providers/Microsoft.Web/sites/${input.name}?api-version=${API_VERSION.sites}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        location: input.location,
        kind: "functionapp",
        properties: {
          serverFarmId: plan.id ?? "",
          siteConfig: {
            appSettings: [
              { name: "FUNCTIONS_WORKER_RUNTIME", value: input.runtime },
              { name: "FUNCTIONS_EXTENSION_VERSION", value: "~4" },
            ],
          },
        },
      }),
    },
  );
  const site = (await response.json()) as ArmResource;
  return { id: site.id ?? "", name: site.name ?? "", location: site.location ?? "", kind: site.kind ?? "functionapp" };
};

// fetchFunctionAppSettings reads app settings the real way: Microsoft.Web
// models `config/appsettings` as a write-only PUT resource and serves the
// current values only through the dedicated `/list` action POST (the same
// request `az functionapp config appsettings list` sends), because the
// values can carry secrets that stay out of GET URLs and proxy logs.
export const fetchFunctionAppSettings = async (siteId: string): Promise<Record<string, string>> => {
  const response = await armSend(`${siteId}/config/appsettings/list?api-version=${API_VERSION.sites}`, {
    method: "POST",
  });
  const body = (await response.json()) as { properties?: Record<string, string> };
  return body.properties ?? {};
};

export interface AzureFunctionSummary {
  name: string;
  language: string;
  isDisabled: boolean;
}

interface ArmFunctionEnvelope {
  name?: string;
  properties?: { language?: string; isDisabled?: boolean };
}

export const fetchFunctions = async (siteId: string): Promise<AzureFunctionSummary[]> => {
  const list = await authorizedJSON<ArmList<ArmFunctionEnvelope>>(`${siteId}/functions?api-version=${API_VERSION.sites}`);
  return (list.value ?? []).map((fn) => ({
    name: fn.name ?? "",
    language: fn.properties?.language ?? "",
    isDisabled: fn.properties?.isDisabled ?? false,
  }));
};

// --- Container registry (ACR) detail: repositories + tags ---

export interface ACRRegistryDetail {
  id: string;
  name: string;
  location: string;
  tags: ResourceTags;
  loginServer: string;
  skuName: string;
  skuTier: string;
  adminUserEnabled: boolean;
  provisioningState: string;
}

interface ArmRegistry {
  id?: string;
  name?: string;
  location?: string;
  tags?: ResourceTags;
  sku?: { name?: string; tier?: string };
  properties?: { loginServer?: string; adminUserEnabled?: boolean; provisioningState?: string };
}

function acrRegistryOf(registry: ArmRegistry): ACRRegistryDetail {
  return {
    id: registry.id ?? "",
    name: registry.name ?? "",
    location: registry.location ?? "",
    tags: registry.tags ?? {},
    loginServer: registry.properties?.loginServer ?? "",
    skuName: registry.sku?.name ?? "",
    skuTier: registry.sku?.tier ?? "",
    adminUserEnabled: registry.properties?.adminUserEnabled ?? false,
    provisioningState: registry.properties?.provisioningState ?? "",
  };
}

export const fetchACRRegistry = async (name: string): Promise<ACRRegistryDetail> => {
  const id = await resourceIdByName("Microsoft.ContainerRegistry/registries", API_VERSION.registries, name);
  return acrRegistryOf(await authorizedJSON<ArmRegistry>(`${id}?api-version=${API_VERSION.registries}`));
};

// The sku.name choices the real registry "Update" blade offers.
export const acrRegistrySkus = ["Basic", "Standard", "Premium"] as const;

export interface UpdateACRRegistryInput {
  skuName: string;
  adminUserEnabled: boolean;
}

// updateACRRegistry drives the real Microsoft.ContainerRegistry registry PATCH
// — the same request `az acr update --sku …/--admin-enabled …` and the
// azurerm_container_registry update both send. The simulator settles it
// synchronously (HTTP 200 with the merged registry), so the console awaits the
// PATCH response directly. Enabling the admin user is what makes the registry's
// admin credentials (and its repository browsing on this console) available.
export const updateACRRegistry = async (id: string, input: UpdateACRRegistryInput): Promise<void> => {
  await armPatch(id, API_VERSION.registries, {
    sku: { name: input.skuName },
    properties: { adminUserEnabled: input.adminUserEnabled },
  });
};

interface ACRAdminCredentials {
  username: string;
  password: string;
}

// mintACRAdminCredentials calls the real `listCredentials` ARM action — the
// same request `az acr credential show` sends — to obtain the registry's
// admin username/password. The ACR data plane (the registry's own login
// server, a distinct host from Azure Resource Manager) authenticates that
// pair as HTTP Basic, exactly the way `docker login <loginServer>` does with
// admin credentials.
async function mintACRAdminCredentials(registryId: string): Promise<ACRAdminCredentials> {
  const response = await armSend(`${registryId}/listCredentials?api-version=${API_VERSION.registries}`, {
    method: "POST",
  });
  const body = (await response.json()) as { username?: string; passwords?: { value?: string }[] };
  return { username: body.username ?? "", password: body.passwords?.[0]?.value ?? "" };
}

async function acrDataPlaneFetch(loginServer: string, path: string, credentials: ACRAdminCredentials): Promise<Response> {
  const url = `https://${loginServer}${path}`;
  const response = await fetch(url, {
    headers: { Authorization: `Basic ${btoa(`${credentials.username}:${credentials.password}`)}` },
  });
  if (!response.ok) {
    throw new Error(`${url} returned HTTP ${response.status}`);
  }
  return response;
}

// fetchACRRepositories reads the registry's own data-plane catalog — ACR's
// `/acr/v1/_catalog` convenience API, the same one `az acr repository list`
// calls — authenticated with the admin credentials minted above. Real ACR
// (and this simulator's registries.go) requires the admin user to be
// enabled for that credential pair to exist; a registry without it stays
// honestly unreadable from here rather than silently showing nothing.
export const fetchACRRepositories = async (registry: ACRRegistryDetail): Promise<string[]> => {
  if (!registry.adminUserEnabled) {
    throw new Error("Enable the admin user on this registry to browse its repositories from this console.");
  }
  const credentials = await mintACRAdminCredentials(registry.id);
  const response = await acrDataPlaneFetch(registry.loginServer, "/acr/v1/_catalog", credentials);
  const body = (await response.json()) as { repositories?: string[] };
  return body.repositories ?? [];
};

export interface ACRTag {
  name: string;
  digest: string;
}

export const fetchACRTags = async (registry: ACRRegistryDetail, repository: string): Promise<ACRTag[]> => {
  const credentials = await mintACRAdminCredentials(registry.id);
  const response = await acrDataPlaneFetch(
    registry.loginServer,
    `/acr/v1/${encodeURIComponent(repository)}/_tags`,
    credentials,
  );
  const body = (await response.json()) as { tags?: { name?: string; digest?: string }[] };
  return (body.tags ?? []).map((tag) => ({ name: tag.name ?? "", digest: tag.digest ?? "" }));
};

// --- Storage account detail: blob containers + blobs ---

export interface StorageAccountDetail {
  id: string;
  name: string;
  location: string;
  kind: string;
  tags: ResourceTags;
  skuName: string;
  provisioningState: string;
  accessTier: string;
  blobEndpoint: string;
}

interface ArmStorageAccount {
  id?: string;
  name?: string;
  location?: string;
  kind?: string;
  tags?: ResourceTags;
  sku?: { name?: string };
  properties?: { provisioningState?: string; accessTier?: string; primaryEndpoints?: { blob?: string } };
}

function storageAccountOf(account: ArmStorageAccount): StorageAccountDetail {
  return {
    id: account.id ?? "",
    name: account.name ?? "",
    location: account.location ?? "",
    kind: account.kind ?? "",
    tags: account.tags ?? {},
    skuName: account.sku?.name ?? "",
    provisioningState: account.properties?.provisioningState ?? "",
    accessTier: account.properties?.accessTier ?? "",
    blobEndpoint: account.properties?.primaryEndpoints?.blob ?? "",
  };
}

export const fetchStorageAccount = async (name: string): Promise<StorageAccountDetail> => {
  const id = await resourceIdByName("Microsoft.Storage/storageAccounts", API_VERSION.storage, name);
  return storageAccountOf(await authorizedJSON<ArmStorageAccount>(`${id}?api-version=${API_VERSION.storage}`));
};

// The replication (sku.name) and access-tier choices the real storage account
// "Configuration" blade edits.
export const storageAccountSkus = ["Standard_LRS", "Standard_GRS", "Standard_RAGRS", "Standard_ZRS", "Premium_LRS"] as const;
export const storageAccessTiers = ["Hot", "Cool"] as const;

export interface UpdateStorageAccountInput {
  skuName: string;
  accessTier: string;
}

// updateStorageAccount drives the real Microsoft.Storage storage-account PATCH
// — the same request `az storage account update --sku …/--access-tier …` and
// the azurerm_storage_account update both send. The simulator merges only the
// keys present (settling synchronously, HTTP 200 with the merged account), so
// the console awaits the PATCH response directly.
export const updateStorageAccount = async (id: string, input: UpdateStorageAccountInput): Promise<void> => {
  await armPatch(id, API_VERSION.storage, {
    sku: { name: input.skuName },
    properties: { accessTier: input.accessTier },
  });
};

// mintAccountSas calls the real `ListAccountSas` ARM action — the same
// request the portal's own Storage Browser issues — to obtain a read-only
// account SAS scoped to the blob service, then uses it as the credential for
// the direct blob data-plane reads below.
async function mintAccountSas(accountId: string): Promise<string> {
  const signedExpiry = new Date(Date.now() + 60 * 60 * 1000).toISOString();
  const response = await armSend(`${accountId}/ListAccountSas?api-version=${API_VERSION.storage}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      signedServices: "b",
      signedResourceTypes: "sco",
      signedPermission: "rl",
      signedProtocol: "https",
      signedExpiry,
    }),
  });
  const body = (await response.json()) as { accountSasToken?: string };
  return body.accountSasToken ?? "";
}

function parseAzureStorageXML(xml: string): Document {
  return new DOMParser().parseFromString(xml, "application/xml");
}

function xmlText(scope: Element | null, tag: string): string {
  return scope?.querySelector(tag)?.textContent ?? "";
}

export interface BlobContainerSummary {
  name: string;
}

// parseContainerListXML reads the real Azure Storage `ListContainers`
// response shape (`EnumerationResults><Containers><Container><Name>`) — the
// same XML the .NET/Go/Python SDKs and `az storage container list` parse.
export function parseContainerListXML(xml: string): BlobContainerSummary[] {
  const doc = parseAzureStorageXML(xml);
  return Array.from(doc.querySelectorAll("Containers > Container")).map((node) => ({
    name: xmlText(node, "Name"),
  }));
}

export interface BlobSummary {
  name: string;
  contentLength: number;
  lastModified: string;
}

// parseBlobListXML reads the real `ListBlobs` response shape
// (`EnumerationResults><Blobs><Blob><Name>`/`<Properties>`).
export function parseBlobListXML(xml: string): BlobSummary[] {
  const doc = parseAzureStorageXML(xml);
  return Array.from(doc.querySelectorAll("Blobs > Blob")).map((node) => {
    const properties = node.querySelector("Properties");
    return {
      name: xmlText(node, "Name"),
      contentLength: Number(xmlText(properties, "Content-Length") || "0"),
      lastModified: xmlText(properties, "Last-Modified"),
    };
  });
}

export const fetchBlobContainers = async (account: StorageAccountDetail): Promise<BlobContainerSummary[]> => {
  if (!account.blobEndpoint) {
    throw new Error("This storage account has no blob endpoint to read.");
  }
  const sas = await mintAccountSas(account.id);
  const url = `${account.blobEndpoint}?comp=list&${sas}`;
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`${url} returned HTTP ${response.status}`);
  }
  return parseContainerListXML(await response.text());
};

export const fetchBlobs = async (account: StorageAccountDetail, container: string): Promise<BlobSummary[]> => {
  const sas = await mintAccountSas(account.id);
  const url = `${account.blobEndpoint}${encodeURIComponent(container)}?restype=container&comp=list&${sas}`;
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`${url} returned HTTP ${response.status}`);
  }
  return parseBlobListXML(await response.text());
};

// A Kusto result names its own columns; the portal reads the ones a container
// log query is expected to carry.
export interface MonitorLogRow {
  [key: string]: string;
}

interface WorkspaceResource extends ArmResource {
  properties?: { customerId?: string };
}

interface QueryTable {
  name?: string;
  columns?: { name?: string }[];
  rows?: unknown[][];
}

interface QueryResponse {
  tables?: QueryTable[];
}

// The portal reads logs the real way: it lists the Log Analytics workspaces,
// then runs a Kusto query against the workspace's own query endpoint (a distinct
// host and token audience from Azure Resource Manager) and shapes the tabular
// result into rows.
export const fetchMonitorLogs = async (): Promise<MonitorLogRow[]> => {
  const workspaces = await listAcrossSubscriptions<WorkspaceResource>(
    "Microsoft.OperationalInsights/workspaces",
    API_VERSION.workspaces,
  );
  const customerIds = workspaces.map((workspace) => workspace.properties?.customerId ?? "").filter(Boolean);

  const rows: MonitorLogRow[] = [];
  for (const customerId of customerIds) {
    const response = await authorizedJSONPost<QueryResponse>(
      `/v1/workspaces/${customerId}/query`,
      { query: "ContainerAppConsoleLogs_CL | take 100" },
      "logs",
    );
    for (const table of response.tables ?? []) {
      const names = (table.columns ?? []).map((column) => column.name ?? "");
      for (const row of table.rows ?? []) {
        const record: MonitorLogRow = {};
        names.forEach((name, index) => {
          record[name] = row[index] == null ? "" : String(row[index]);
        });
        rows.push(record);
      }
    }
    if (rows.length >= 100) break;
  }
  return rows.slice(0, 100);
};

// --- Microsoft Entra ID: App registrations (Microsoft Graph) ---
//
// The App registrations blade reads and writes the real Microsoft Graph
// application + service-principal surface (graph.microsoft.com's /v1.0 paths at
// the configured Graph coordinate) with a Graph-scoped federated token — the
// same requests the real Azure portal issues.

export interface ClientSecretMetadata {
  keyId: string;
  displayName: string;
  hint: string;
  startDateTime: string;
  endDateTime: string;
}

// MintedClientSecret is the addPassword response: the only place Microsoft
// Graph ever returns the secretText.
export interface MintedClientSecret extends ClientSecretMetadata {
  secretText: string;
}

export interface AppRegistration {
  id: string;
  appId: string;
  displayName: string;
  signInAudience: string;
  passwordCredentials: ClientSecretMetadata[];
}

interface GraphApplication {
  id?: string;
  appId?: string;
  displayName?: string;
  signInAudience?: string;
  passwordCredentials?: Partial<ClientSecretMetadata>[];
}

function appRegistrationOf(app: GraphApplication): AppRegistration {
  return {
    id: app.id ?? "",
    appId: app.appId ?? "",
    displayName: app.displayName ?? "",
    signInAudience: app.signInAudience ?? "",
    passwordCredentials: (app.passwordCredentials ?? []).map((credential) => ({
      keyId: credential.keyId ?? "",
      displayName: credential.displayName ?? "",
      hint: credential.hint ?? "",
      startDateTime: credential.startDateTime ?? "",
      endDateTime: credential.endDateTime ?? "",
    })),
  };
}

// graphSend writes to a Microsoft Graph path and surfaces Graph's own error
// message rather than masking it.
async function graphSend(path: string, init: RequestInit): Promise<Response> {
  const response = await authorizedFetch(path, "graph", init);
  if (!response.ok) {
    let detail = "";
    try {
      const body = (await response.json()) as { error?: { message?: string } };
      detail = body.error?.message ?? "";
    } catch {
      // A non-JSON error body keeps the HTTP status as the message.
    }
    throw new Error(`${path} returned HTTP ${response.status}${detail ? `: ${detail}` : ""}`);
  }
  return response;
}

export const fetchAppRegistrations = async (): Promise<AppRegistration[]> => {
  const list = await authorizedJSON<{ value?: GraphApplication[] }>("/v1.0/applications", "graph");
  return (list.value ?? []).map(appRegistrationOf);
};

export const fetchAppRegistration = async (objectId: string): Promise<AppRegistration> =>
  appRegistrationOf(await authorizedJSON<GraphApplication>(`/v1.0/applications/${objectId}`, "graph"));

// createAppRegistration provisions what the real portal's "New registration"
// provisions: the application object plus the service principal that
// materializes it in the tenant — the principal the client_credentials grant
// issues app-only tokens for.
export const createAppRegistration = async (displayName: string): Promise<AppRegistration> => {
  const created = await graphSend("/v1.0/applications", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ displayName, signInAudience: "AzureADMyOrg" }),
  });
  const app = appRegistrationOf((await created.json()) as GraphApplication);
  await graphSend("/v1.0/servicePrincipals", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ appId: app.appId }),
  });
  return app;
};

export const deleteAppRegistration = async (objectId: string): Promise<void> => {
  await graphSend(`/v1.0/applications/${objectId}`, { method: "DELETE" });
};

// addClientSecret mints a client secret on the application object — Microsoft
// Graph's addPassword, the call behind the Certificates & secrets blade. The
// response is the one time Graph returns the secretText.
export const addClientSecret = async (
  objectId: string,
  displayName: string,
  endDateTime: string,
): Promise<MintedClientSecret> => {
  const response = await graphSend(`/v1.0/applications/${objectId}/addPassword`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ passwordCredential: { displayName, endDateTime } }),
  });
  const credential = (await response.json()) as Partial<MintedClientSecret>;
  return {
    keyId: credential.keyId ?? "",
    displayName: credential.displayName ?? "",
    hint: credential.hint ?? "",
    startDateTime: credential.startDateTime ?? "",
    endDateTime: credential.endDateTime ?? "",
    secretText: credential.secretText ?? "",
  };
};

export const removeClientSecret = async (objectId: string, keyId: string): Promise<void> => {
  await graphSend(`/v1.0/applications/${objectId}/removePassword`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ keyId }),
  });
};

// --- The generic Azure Resource Manager envelope every service blade reads ---
//
// Every ARM resource — a virtual machine, a Cosmos DB account, a Service Bus
// namespace — is returned in the same envelope: `id`, `name`, `type`,
// `location`, `kind`, `sku`, `tags`, and a provider-specific `properties`
// object. The service blades below read that envelope directly from the
// provider's own real List/Get operations rather than through a
// per-resource-type mapper, which is what lets one blade implementation serve
// every provider while still rendering the true response shape: the columns
// and Essentials of each blade name the exact `properties` path the real Azure
// REST specification documents for that resource.

export interface ArmSku {
  name?: string;
  tier?: string;
  family?: string;
  size?: string;
  capacity?: number;
}

export interface ArmGenericResource {
  id: string;
  name: string;
  type: string;
  location: string;
  kind: string;
  sku: ArmSku;
  tags: ResourceTags;
  properties: Record<string, unknown>;
}

interface ArmEnvelope {
  id?: string;
  name?: string;
  type?: string;
  location?: string;
  kind?: string;
  sku?: ArmSku;
  tags?: ResourceTags;
  properties?: Record<string, unknown>;
}

interface ArmPage<T> {
  value?: T[];
  nextLink?: string;
}

function armResourceOf(resource: ArmEnvelope): ArmGenericResource {
  return {
    id: resource.id ?? "",
    name: resource.name ?? "",
    type: resource.type ?? "",
    location: resource.location ?? "",
    kind: resource.kind ?? "",
    sku: resource.sku ?? {},
    tags: resource.tags ?? {},
    properties: resource.properties ?? {},
  };
}

// nextLinkPath turns ARM's absolute `nextLink` into the path this portal's
// cloud coordinate is prefixed onto. Real ARM emits the continuation as a
// fully-qualified URL on the same host the list was read from, so keeping its
// path and query — and letting authorizedFetch re-apply the configured cloud
// base — reaches exactly the resource ARM pointed at, whether that base is the
// portal's own origin or a separate Azure Resource Manager host.
function nextLinkPath(nextLink: string): string {
  const url = new URL(nextLink, "http://arm.invalid");
  return `${url.pathname}${url.search}`;
}

// armList reads one ARM list operation to exhaustion, following `nextLink` the
// way every real ARM client (the SDK pagers, `az`, the portal itself) does
// rather than silently rendering only the first page.
async function armList(path: string): Promise<ArmGenericResource[]> {
  const resources: ArmGenericResource[] = [];
  let next: string | undefined = path;
  // ARM guarantees termination by never repeating a continuation token; a
  // page count bound would silently truncate, so this follows the links.
  while (next) {
    const page: ArmPage<ArmEnvelope> = await authorizedJSON<ArmPage<ArmEnvelope>>(next);
    resources.push(...(page.value ?? []).map(armResourceOf));
    next = page.nextLink ? nextLinkPath(page.nextLink) : undefined;
  }
  return resources;
}

// fetchArmResources reads a provider's real subscription-wide List operation
// (`GET /subscriptions/{id}/providers/{provider}?api-version=…`) across every
// subscription this directory can reach — the same request `az <service> list`
// and the provider's SDK ListBySubscription pager send.
export const fetchArmResources = async (provider: string, apiVersion: string): Promise<ArmGenericResource[]> => {
  const subs = await subscriptionIds();
  const pages = await Promise.all(
    subs.map((sub) => armList(`/subscriptions/${sub}/providers/${provider}?api-version=${apiVersion}`)),
  );
  return pages.flat();
};

// fetchArmResourceByName resolves a resource from the short name a blade route
// carries to its full ARM resource ID, then reads the resource's own real Get
// operation at that ID. ARM's Get needs the subscription- and
// resource-group-scoped path a name alone doesn't have; the subscription-wide
// List the listing blade already reads supplies it.
export const fetchArmResourceByName = async (
  provider: string,
  apiVersion: string,
  name: string,
): Promise<ArmGenericResource> => {
  const matches = await fetchArmResources(provider, apiVersion);
  const match = matches.find((resource) => resource.name === name);
  if (!match) {
    throw new Error(`${provider}/${name} was not found in any subscription this directory can reach.`);
  }
  return armResourceOf(await authorizedJSON<ArmEnvelope>(`${match.id}?api-version=${apiVersion}`));
};

// fetchArmChildren reads a resource's real sub-resource List operation — the
// child collection ARM nests under the parent's own resource ID (a virtual
// network's `subnets`, a Service Bus namespace's `queues`, a Cosmos DB
// account's `sqlDatabases`), each its own documented ARM operation.
export const fetchArmChildren = async (
  parentId: string,
  childPath: string,
  apiVersion: string,
): Promise<ArmGenericResource[]> => armList(`${parentId}/${childPath}?api-version=${apiVersion}`);

// deleteArmResource drives a resource's real ARM DELETE — the same request
// `az <service> delete` and the azurerm provider's destroy both send.
export const deleteArmResource = async (id: string, apiVersion: string): Promise<void> => {
  await armSend(`${id}?api-version=${apiVersion}`, { method: "DELETE" });
};

// postArmAction drives a resource's real ARM action POST — the lifecycle verbs
// ARM models as `POST {resourceId}/{action}` (a virtual machine's
// `start`/`restart`/`powerOff`/`deallocate`, a PostgreSQL flexible server's
// `start`/`stop`/`restart`, a container group's `start`/`stop`/`restart`).
export const postArmAction = async (id: string, action: string, apiVersion: string): Promise<void> => {
  await armSend(`${id}/${action}?api-version=${apiVersion}`, { method: "POST" });
};

// --- Resource groups (Microsoft.Resources) ---
//
// Resource groups are not a provider resource: ARM serves them at
// `/subscriptions/{id}/resourceGroups`, its own path, which is why they read
// through their own function rather than fetchArmResources.

export const fetchResourceGroups = async (): Promise<ArmGenericResource[]> => {
  const subs = await subscriptionIds();
  const pages = await Promise.all(
    subs.map((sub) => armList(`/subscriptions/${sub}/resourceGroups?api-version=${API_VERSION.resourceGroups}`)),
  );
  return pages.flat();
};

export const fetchResourceGroup = async (name: string): Promise<ArmGenericResource> => {
  const groups = await fetchResourceGroups();
  const match = groups.find((group) => group.name === name);
  if (!match) {
    throw new Error(`Resource group ${name} was not found in any subscription this directory can reach.`);
  }
  return armResourceOf(await authorizedJSON<ArmEnvelope>(`${match.id}?api-version=${API_VERSION.resourceGroups}`));
};

// fetchResourceGroupResources reads the resource group's real
// `Resources_ListByResourceGroup` operation — the same request
// `az resource list --resource-group` sends — so the group's blade shows what
// ARM says the group actually holds rather than a count assembled per
// provider.
export const fetchResourceGroupResources = async (groupId: string): Promise<ArmGenericResource[]> =>
  armList(`${groupId}/resources?api-version=${API_VERSION.resourceGroups}`);
