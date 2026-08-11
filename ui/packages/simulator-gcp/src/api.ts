import {
  authorizedFetch,
  authorizedJSON,
  authorizedJSONDelete,
  authorizedJSONPatch,
  authorizedJSONPost,
} from "./console/federation.js";

// The console reads one project and region at a time, the way the real console
// shows the selected project and region. The region is a console coordinate; a
// deployment points it at the region its workloads run in. The project is SPA
// state: the header's project picker selects it (console/project.tsx persists
// the choice), so every fetcher takes the selected project and every query key
// carries it.
export const DEFAULT_CONSOLE_PROJECT = "sockerless";
export const CONSOLE_REGION = "us-central1";

const jobsParent = (project: string) => `/v2/projects/${project}/locations/${CONSOLE_REGION}/jobs`;

// The Cloud Run v2 Job resource, as the real API returns it. The console reads
// the true shape rather than a hand-picked subset.
export interface CloudRunJobCondition {
  type?: string;
  state?: string;
  message?: string;
}

// The Cloud Run v2 execution/task template, as the API nests it under a Job.
// The console reads and round-trips the real shape so an edit or create carries
// the same fields a real client (gcloud, terraform-provider-google) sends.
export interface CloudRunEnvVar {
  name: string;
  value?: string;
}

export interface CloudRunContainer {
  name?: string;
  image: string;
  command?: string[];
  args?: string[];
  env?: CloudRunEnvVar[];
  resources?: { limits?: Record<string, string> };
}

export interface CloudRunTaskTemplate {
  containers?: CloudRunContainer[];
  timeout?: string;
  maxRetries?: number;
  serviceAccount?: string;
}

export interface CloudRunExecutionTemplate {
  parallelism?: number;
  taskCount?: number;
  template?: CloudRunTaskTemplate;
}

export interface CloudRunJob {
  name: string;
  uid?: string;
  createTime?: string;
  updateTime?: string;
  launchStage?: string;
  executionCount?: number;
  reconciling?: boolean;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  template?: CloudRunExecutionTemplate;
  terminalCondition?: CloudRunJobCondition;
  conditions?: CloudRunJobCondition[];
  latestCreatedExecution?: { name?: string; createTime?: string; completionTime?: string };
}

export interface CloudRunExecution {
  name: string;
  createTime?: string;
  startTime?: string;
  completionTime?: string;
  succeededCount?: number;
  failedCount?: number;
  runningCount?: number;
  cancelledCount?: number;
  taskCount?: number;
  conditions?: CloudRunJobCondition[];
}

export const fetchCloudRunJobsReal = async (project: string): Promise<CloudRunJob[]> => {
  const page = await authorizedJSON<{ jobs?: CloudRunJob[] }>(jobsParent(project));
  return page.jobs ?? [];
};

export const fetchCloudRunJob = (project: string, name: string): Promise<CloudRunJob> =>
  authorizedJSON<CloudRunJob>(`${jobsParent(project)}/${name}`);

export const fetchCloudRunJobExecutions = async (project: string, name: string): Promise<CloudRunExecution[]> => {
  const page = await authorizedJSON<{ executions?: CloudRunExecution[] }>(`${jobsParent(project)}/${name}/executions`);
  return page.executions ?? [];
};

// projects.locations.jobs.delete — a long-running operation, driven through
// the same operations.get poll waitV2Operation runs for Cloud Functions.
export const deleteCloudRunJob = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`${jobsParent(project)}/${name}`);

// The fields the Create job / Edit job forms collect. The console composes
// them into the real Cloud Run v2 Job body (a nested execution + task
// template) rather than a flattened sockerless shape.
export interface CloudRunJobConfig {
  image: string;
  taskCount: number;
  timeoutSeconds: number;
  env: CloudRunEnvVar[];
}

// jobBodyFromConfig builds the real projects.locations.jobs Job body from the
// form's fields — the same nested template shape gcloud and
// terraform-provider-google send.
const jobBodyFromConfig = (config: CloudRunJobConfig, labels?: Record<string, string>): CloudRunJob => ({
  name: "",
  ...(labels && Object.keys(labels).length > 0 ? { labels } : {}),
  template: {
    taskCount: config.taskCount,
    template: {
      timeout: `${config.timeoutSeconds}s`,
      containers: [
        {
          image: config.image,
          ...(config.env.length > 0 ? { env: config.env } : {}),
        },
      ],
    },
  },
});

// projects.locations.jobs.create — POST ?jobId=, a long-running operation the
// console drives to completion through the same operations.get poll
// (waitV2Operation) the delete flow uses.
export const createCloudRunJob = (
  project: string,
  jobId: string,
  config: CloudRunJobConfig,
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(
    `${jobsParent(project)}?jobId=${encodeURIComponent(jobId)}`,
    jobBodyFromConfig(config),
  );

// projects.locations.jobs.run — POST {job}:run creates an execution and
// returns a long-running Operation. The `:run` verb rides on the resource path
// (not query), so the job name is templated in directly rather than
// URL-encoded (which would escape the colon).
export const runCloudRunJob = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(`${jobsParent(project)}/${name}:run`, {});

// projects.locations.jobs.patch — UpdateJob replaces the full mutable
// resource (the real API and the simulator both round-trip the whole Job), so
// the console reads the loaded job, applies the edited template fields, and
// sends the complete Job back rather than a partial patch that would drop the
// rest of the template. Returns a long-running Operation.
export const updateCloudRunJob = (
  project: string,
  name: string,
  job: CloudRunJob,
): Promise<CrmOperation> =>
  authorizedJSONPatch<CrmOperation>(`${jobsParent(project)}/${name}`, job);

// Cloud Functions (Gen2) lifecycle states.
export type CloudFunctionState =
  | "ACTIVE"
  | "FAILED"
  | "DEPLOYING"
  | "DELETING"
  | "UNKNOWN";

// The real Cloud Functions (Gen2) resource, as the API returns it.
export interface CloudFunction {
  name: string;
  state?: CloudFunctionState;
  environment?: string;
  description?: string;
  createTime?: string;
  updateTime?: string;
  labels?: Record<string, string>;
  serviceConfig?: {
    uri?: string;
    service?: string;
    timeoutSeconds?: number;
    availableMemory?: string;
    availableCpu?: string;
    maxInstanceCount?: number;
    minInstanceCount?: number;
    ingressSettings?: string;
    environmentVariables?: Record<string, string>;
  };
  buildConfig?: { runtime?: string; entryPoint?: string };
}

// The real Artifact Registry repository resource.
export interface ARRepo {
  name: string;
  format?: string;
  mode?: string;
  description?: string;
  createTime?: string;
  updateTime?: string;
  labels?: Record<string, string>;
}

// The real Artifact Registry DockerImage resource
// (artifactregistry.googleapis.com/v1 projects.locations.repositories.dockerImages).
export interface ARDockerImage {
  name: string;
  uri?: string;
  tags?: string[];
  uploadTime?: string;
  mediaType?: string;
  buildTime?: string;
}

// The real Cloud Storage bucket resource (storage#bucket).
export interface GCSBucket {
  name: string;
  id?: string;
  location?: string;
  storageClass?: string;
  timeCreated?: string;
  updated?: string;
  labels?: Record<string, string>;
}

// The real Cloud Storage object resource (storage#object).
export interface GCSObject {
  name: string;
  bucket: string;
  size?: string;
  contentType?: string;
  storageClass?: string;
  timeCreated?: string;
  updated?: string;
  generation?: string;
}

// Cloud Logging LogSeverity enum (proto .String()).
export type LogSeverity =
  | "DEFAULT"
  | "DEBUG"
  | "INFO"
  | "NOTICE"
  | "WARNING"
  | "ERROR"
  | "CRITICAL"
  | "ALERT"
  | "EMERGENCY";

// The monitored resource that produced a log entry (logging/v2
// MonitoredResource), as the real API returns it.
export interface LogEntryResource {
  type: string;
  labels?: Record<string, string>;
}

export interface LogEntry {
  logName: string;
  timestamp: string;
  // Omitted by the server (json:"severity,omitempty") when unset (DEFAULT).
  severity?: LogSeverity;
  textPayload?: string;
  jsonPayload?: Record<string, unknown>;
  insertId?: string;
  resource?: LogEntryResource;
  labels?: Record<string, string>;
}


const functionsParent = (project: string) => `/v2/projects/${project}/locations/${CONSOLE_REGION}/functions`;
const repositoriesParent = (project: string) => `/v1/projects/${project}/locations/${CONSOLE_REGION}/repositories`;

export const fetchCloudFunctions = async (project: string): Promise<CloudFunction[]> =>
  (await authorizedJSON<{ functions?: CloudFunction[] }>(functionsParent(project))).functions ?? [];

export const fetchCloudFunction = (project: string, name: string): Promise<CloudFunction> =>
  authorizedJSON<CloudFunction>(`${functionsParent(project)}/${name}`);

// projects.locations.functions.delete — a long-running operation, driven
// through the same operations.get poll waitV2Operation runs for Cloud Run jobs.
export const deleteCloudFunction = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`${functionsParent(project)}/${name}`);

// projects.locations.functions.patch — UpdateFunction, a long-running
// operation. The updateMask names the serviceConfig fields the form edits; the
// body carries the full merged serviceConfig (the whole object is replaced at
// serviceConfig granularity), so the caller passes the loaded function's
// serviceConfig with the edited fields applied to avoid dropping the rest.
export const updateCloudFunction = (
  project: string,
  name: string,
  serviceConfig: CloudFunction["serviceConfig"],
): Promise<CrmOperation> => {
  const mask = [
    "serviceConfig.availableMemory",
    "serviceConfig.timeoutSeconds",
    "serviceConfig.minInstanceCount",
    "serviceConfig.maxInstanceCount",
    "serviceConfig.environmentVariables",
  ].join(",");
  return authorizedJSONPatch<CrmOperation>(
    `${functionsParent(project)}/${name}?updateMask=${encodeURIComponent(mask)}`,
    { serviceConfig },
  );
};

// The fields the Create function form collects. Image-based deploys are the
// simplest faithful path against the simulator, but the real console's form
// leads with runtime + entry point, so the console collects those.
export interface CloudFunctionCreateConfig {
  runtime: string;
  entryPoint: string;
}

// projects.locations.functions.create — POST ?functionId=, a long-running
// operation. The console sends the real buildConfig (runtime + entry point)
// the way the create form collects it.
export const createCloudFunction = (
  project: string,
  functionId: string,
  config: CloudFunctionCreateConfig,
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(
    `${functionsParent(project)}?functionId=${encodeURIComponent(functionId)}`,
    { buildConfig: { runtime: config.runtime, entryPoint: config.entryPoint }, environment: "GEN_2" },
  );

// operations.get for the /v2/projects/.../locations/.../operations/{id}
// collection Cloud Functions and Cloud Run Jobs LROs live under — the v2
// counterpart to fetchArOperation's v1 collection.
export const fetchV2Operation = (name: string): Promise<CrmOperation> =>
  authorizedJSON<CrmOperation>(`/v2/${name}`);

// waitV2Operation drives a returned Operation to completion the way every
// real client does: poll operations.get until done, then surface the
// operation's own error if it failed.
export const waitV2Operation = async (operation: CrmOperation): Promise<CrmOperation> => {
  let current = operation;
  while (!current.done) {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchV2Operation(current.name);
  }
  if (current.error) {
    throw new Error(current.error.message ?? `operation ${current.name} failed`);
  }
  return current;
};

export const fetchARRepos = async (project: string): Promise<ARRepo[]> =>
  (await authorizedJSON<{ repositories?: ARRepo[] }>(repositoriesParent(project))).repositories ?? [];

export const fetchARRepo = (project: string, name: string): Promise<ARRepo> =>
  authorizedJSON<ARRepo>(`${repositoriesParent(project)}/${name}`);

// projects.locations.repositories.create — a long-running operation. The
// simulator (like real Artifact Registry for a plain repository create)
// settles it synchronously and returns it already `done`, but the console
// drives it through the same operations.get poll loop a real client uses
// (waitArOperation below) rather than assuming immediate completion.
export const createARRepository = (
  project: string,
  location: string,
  repositoryId: string,
  format = "DOCKER",
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(
    `/v1/projects/${project}/locations/${location}/repositories?repositoryId=${encodeURIComponent(repositoryId)}`,
    { format },
  );

// operations.get for the /v1/projects/.../locations/.../operations/{id}
// collection Artifact Registry (and Cloud Run Jobs/Services) LROs live
// under — distinct from the Cloud Resource Manager v3 operations collection
// fetchCrmOperation reads.
export const fetchArOperation = (name: string): Promise<CrmOperation> =>
  authorizedJSON<CrmOperation>(`/v1/${name}`);

// waitArOperation drives a returned Operation to completion the way every
// real client does: poll operations.get until done, then surface the
// operation's own error if it failed.
export const waitArOperation = async (operation: CrmOperation): Promise<CrmOperation> => {
  let current = operation;
  while (!current.done) {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchArOperation(current.name);
  }
  if (current.error) {
    throw new Error(current.error.message ?? `operation ${current.name} failed`);
  }
  return current;
};

// projects.locations.repositories.delete — a long-running operation, driven
// through the same operations.get poll (waitArOperation) the create flow uses.
export const deleteARRepository = (project: string, repo: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`${repositoriesParent(project)}/${repo}`);

// projects.locations.repositories.patch — UpdateRepository. Unlike create and
// delete (which are long-running operations), UpdateRepository is synchronous:
// the real API (and the simulator) return the updated Repository directly, so
// there is no operation to poll.
export const updateARRepository = (
  project: string,
  repo: string,
  patch: { labels?: Record<string, string>; description?: string },
): Promise<ARRepo> =>
  authorizedJSONPatch<ARRepo>(`${repositoriesParent(project)}/${repo}`, patch);

// projects.locations.repositories.dockerImages.list — the repository's
// stored images, the real console's "Images" tab.
export const fetchARImages = async (project: string, repo: string): Promise<ARDockerImage[]> =>
  (
    await authorizedJSON<{ dockerImages?: ARDockerImage[] }>(`${repositoriesParent(project)}/${repo}/dockerImages`)
  ).dockerImages ?? [];

export const fetchGCSBuckets = async (project: string): Promise<GCSBucket[]> =>
  (await authorizedJSON<{ items?: GCSBucket[] }>(`/storage/v1/b?project=${project}`)).items ?? [];

// storage.buckets.insert — the wire body is just `{ "name": <name> }`; the
// simulator (and real GCS) fill in the rest (id, selfLink, timeCreated,
// location defaulting to "US", storageClass defaulting to "STANDARD").
export const createGCSBucket = (project: string, name: string): Promise<GCSBucket> =>
  authorizedJSONPost<GCSBucket>(`/storage/v1/b?project=${encodeURIComponent(project)}`, { name });

// Bucket names are global in Cloud Storage; the read addresses the bucket
// directly, without a project segment.
export const fetchGCSBucket = (name: string): Promise<GCSBucket> =>
  authorizedJSON<GCSBucket>(`/storage/v1/b/${name}`);

// storage.buckets.delete — bucket names are global, so (like the read) this
// addresses the bucket directly, without a project segment. The real API
// answers 204 No Content, which authorizedJSONDelete surfaces as void.
export const deleteGCSBucket = (name: string): Promise<void> =>
  authorizedJSONDelete<void>(`/storage/v1/b/${name}`);

// storage.buckets.patch — a synchronous update (the real API returns the
// updated bucket, not an operation). Bucket names are global, so (like the
// read) this addresses the bucket directly. `labels` is deep-merged by the
// API — a null value removes a label key — and the default storage class is
// overwritten wholesale.
export const updateGCSBucket = (
  name: string,
  patch: { labels?: Record<string, string | null>; storageClass?: string },
): Promise<GCSBucket> => authorizedJSONPatch<GCSBucket>(`/storage/v1/b/${name}`, patch);

// objects.list — the bucket's stored objects, the real console's "Objects" tab.
export const fetchGCSObjects = async (bucket: string): Promise<GCSObject[]> =>
  (await authorizedJSON<{ items?: GCSObject[] }>(`/storage/v1/b/${bucket}/o`)).items ?? [];

// The real IAM ServiceAccount resource (iam.googleapis.com v1), as the API
// returns it.
export interface ServiceAccount {
  name: string;
  projectId?: string;
  uniqueId?: string;
  email: string;
  displayName?: string;
  description?: string;
  disabled?: boolean;
}

// The real IAM ServiceAccountKey resource. privateKeyData — the base64-encoded
// credential file — is returned by the API on create only, never on get/list.
export interface ServiceAccountKey {
  name: string;
  keyAlgorithm?: string;
  validAfterTime?: string;
  validBeforeTime?: string;
  keyType?: string;
  privateKeyData?: string;
  privateKeyType?: string;
}

const serviceAccountsParent = (project: string) => `/v1/projects/${project}/serviceAccounts`;

export const fetchServiceAccounts = async (project: string): Promise<ServiceAccount[]> =>
  (await authorizedJSON<{ accounts?: ServiceAccount[] }>(serviceAccountsParent(project))).accounts ?? [];

export const fetchServiceAccount = (project: string, email: string): Promise<ServiceAccount> =>
  authorizedJSON<ServiceAccount>(`${serviceAccountsParent(project)}/${email}`);

// serviceAccounts.create — the wire body is { accountId, serviceAccount }.
export const createServiceAccount = (
  project: string,
  accountId: string,
  displayName: string,
  description: string,
): Promise<ServiceAccount> =>
  authorizedJSONPost<ServiceAccount>(serviceAccountsParent(project), {
    accountId,
    serviceAccount: { displayName, description },
  });

export const deleteServiceAccount = (project: string, email: string): Promise<unknown> =>
  authorizedJSONDelete(`${serviceAccountsParent(project)}/${email}`);

export const fetchServiceAccountKeys = async (project: string, email: string): Promise<ServiceAccountKey[]> =>
  (await authorizedJSON<{ keys?: ServiceAccountKey[] }>(`${serviceAccountsParent(project)}/${email}/keys`)).keys ?? [];

// serviceAccounts.keys.create — the one response that carries privateKeyData.
export const createServiceAccountKey = (project: string, email: string): Promise<ServiceAccountKey> =>
  authorizedJSONPost<ServiceAccountKey>(`${serviceAccountsParent(project)}/${email}/keys`, {});

export const deleteServiceAccountKey = (project: string, email: string, keyId: string): Promise<unknown> =>
  authorizedJSONDelete(`${serviceAccountsParent(project)}/${email}/keys/${keyId}`);

// Cloud Logging lists entries by POST, filtered to the project's logs and,
// when given, the operator's own Cloud Logging query-language filter — the
// same `filter` field the real Logs Explorer's query box sends.
export const fetchLogEntries = async (project: string, filter?: string): Promise<LogEntry[]> =>
  (
    await authorizedJSONPost<{ entries?: LogEntry[] }>("/v2/entries:list", {
      resourceNames: [`projects/${project}`],
      orderBy: "timestamp desc",
      pageSize: 100,
      ...(filter ? { filter } : {}),
    })
  ).entries ?? [];

// The Cloud Resource Manager v3 Project resource, as the real API returns it:
// name carries the generated project number ("projects/415104041262"),
// projectId the caller-chosen ID, and state the ACTIVE ⇄ DELETE_REQUESTED
// soft-delete lifecycle.
export interface CrmProject {
  name: string;
  projectId: string;
  state?: "ACTIVE" | "DELETE_REQUESTED";
  displayName?: string;
  parent?: string;
  createTime?: string;
  deleteTime?: string;
  labels?: Record<string, string>;
}

// A google.longrunning.Operation, as projects.create/delete return it.
export interface CrmOperation {
  name: string;
  done?: boolean;
  response?: Record<string, unknown>;
  error?: { code?: number; message?: string };
}

// crmProjectNumber reads the project number out of the v3 resource name.
export const crmProjectNumber = (project: CrmProject): string =>
  project.name.replace(/^projects\//, "");

// projects.search — the read behind the real console's picker and Manage
// resources page: every project the caller can see, without requiring a
// parent. The query is the API's own search syntax (e.g. "state:ACTIVE").
export const searchProjects = async (query?: string): Promise<CrmProject[]> => {
  const path = query ? `/v3/projects:search?query=${encodeURIComponent(query)}` : "/v3/projects:search";
  return (await authorizedJSON<{ projects?: CrmProject[] }>(path)).projects ?? [];
};

// projects.create returns a long-running Operation.
export const createProject = (projectId: string, displayName: string): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>("/v3/projects", { projectId, displayName });

// operations.get — the poll the create dialog runs until the operation is
// done. The operation name is "operations/{id}", addressed under /v3.
export const fetchCrmOperation = (name: string): Promise<CrmOperation> =>
  authorizedJSON<CrmOperation>(`/v3/${name}`);

// projects.delete soft-deletes: the project enters DELETE_REQUESTED (the
// 30-day pending-deletion window) and the API returns an Operation.
export const deleteProject = (projectId: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`/v3/projects/${projectId}`);

// waitCrmOperation drives a returned Operation to completion the way every
// real client does: poll operations.get until done, then surface the
// operation's own error if it failed.
export const waitCrmOperation = async (operation: CrmOperation): Promise<CrmOperation> => {
  let current = operation;
  while (!current.done) {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchCrmOperation(current.name);
  }
  if (current.error) {
    throw new Error(current.error.message ?? `operation ${current.name} failed`);
  }
  return current;
};

// ---------------------------------------------------------------------------
// Compute Engine (compute.googleapis.com/v1)
// ---------------------------------------------------------------------------

// authorizedJSONPut sends a full-resource write to a real Google Cloud API
// path. Pub/Sub's topics.create and subscriptions.create are PUT-on-the-
// resource-name methods (the resource name is the URL, the body is the
// resource), unlike the POST-to-the-collection methods the rest of the console
// uses. Built here on the shared authorizedFetch so the federated-credential
// path is exactly the one every other call takes.
const authorizedJSONPut = async <T>(path: string, body: unknown): Promise<T> => {
  const response = await authorizedFetch(path, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`${path} returned HTTP ${response.status}`);
  }
  return (await response.json()) as T;
};

const computeParent = (project: string) => `/compute/v1/projects/${project}`;

// A compute v1 Operation, as every mutating Compute Engine method returns it.
// Unlike the google.longrunning.Operation the newer services use, it reports
// completion through `status: "DONE"` and carries the failure in `error.errors`.
export interface ComputeOperation {
  name: string;
  status?: "PENDING" | "RUNNING" | "DONE";
  operationType?: string;
  targetLink?: string;
  zone?: string;
  region?: string;
  error?: { errors?: { code?: string; message?: string }[] };
  httpErrorMessage?: string;
}

export interface ComputeInstance {
  name: string;
  id?: string;
  status?: string;
  statusMessage?: string;
  zone?: string;
  machineType?: string;
  creationTimestamp?: string;
  description?: string;
  labels?: Record<string, string>;
  tags?: { items?: string[] };
  networkInterfaces?: { name?: string; network?: string; subnetwork?: string; networkIP?: string }[];
  disks?: { deviceName?: string; source?: string; boot?: boolean; mode?: string }[];
}

export interface ComputeDisk {
  name: string;
  id?: string;
  status?: string;
  zone?: string;
  sizeGb?: string;
  type?: string;
  sourceImage?: string;
  users?: string[];
  creationTimestamp?: string;
}

export interface ComputeNetwork {
  name: string;
  id?: string;
  selfLink?: string;
  autoCreateSubnetworks?: boolean;
  routingConfig?: { routingMode?: string };
  creationTimestamp?: string;
}

export interface ComputeSubnetwork {
  name: string;
  id?: string;
  network?: string;
  ipCidrRange?: string;
  region?: string;
  gatewayAddress?: string;
  privateIpGoogleAccess?: boolean;
  creationTimestamp?: string;
}

export interface ComputeFirewall {
  name: string;
  id?: string;
  network?: string;
  direction?: string;
  priority?: number;
  disabled?: boolean;
  sourceRanges?: string[];
  targetTags?: string[];
  allowed?: { IPProtocol?: string; ports?: string[] }[];
  denied?: { IPProtocol?: string; ports?: string[] }[];
  creationTimestamp?: string;
}

// A compute aggregatedList answers `items` as a map of scope
// ("zones/us-central1-a") to a per-scope object holding the collection — or a
// `warning` when the scope has none. flattenAggregated reads the real shape
// rather than assuming a flat array.
const flattenAggregated = <T>(items: Record<string, Record<string, T[] | unknown>> | undefined, key: string): T[] => {
  const out: T[] = [];
  for (const scope of Object.values(items ?? {})) {
    const bucket = scope?.[key];
    if (Array.isArray(bucket)) out.push(...(bucket as T[]));
  }
  return out;
};

// compute.instances.aggregatedList — the read behind the real console's VM
// instances page, which lists every zone at once rather than one zone at a time.
export const fetchComputeInstances = async (project: string): Promise<ComputeInstance[]> => {
  const page = await authorizedJSON<{ items?: Record<string, Record<string, unknown>> }>(
    `${computeParent(project)}/aggregated/instances`,
  );
  return flattenAggregated<ComputeInstance>(page.items, "instances");
};

export const fetchComputeInstance = (project: string, zone: string, name: string): Promise<ComputeInstance> =>
  authorizedJSON<ComputeInstance>(`${computeParent(project)}/zones/${zone}/instances/${name}`);

// compute.instances.start / .stop / .delete — each returns a zonal Operation
// the console drives to DONE through compute.zoneOperations.get.
export const startComputeInstance = (project: string, zone: string, name: string): Promise<ComputeOperation> =>
  authorizedJSONPost<ComputeOperation>(`${computeParent(project)}/zones/${zone}/instances/${name}/start`, {});

export const stopComputeInstance = (project: string, zone: string, name: string): Promise<ComputeOperation> =>
  authorizedJSONPost<ComputeOperation>(`${computeParent(project)}/zones/${zone}/instances/${name}/stop`, {});

export const deleteComputeInstance = (project: string, zone: string, name: string): Promise<ComputeOperation> =>
  authorizedJSONDelete<ComputeOperation>(`${computeParent(project)}/zones/${zone}/instances/${name}`);

export const fetchComputeZoneOperation = (project: string, zone: string, name: string): Promise<ComputeOperation> =>
  authorizedJSON<ComputeOperation>(`${computeParent(project)}/zones/${zone}/operations/${name}`);

// waitComputeZoneOperation drives a zonal Compute Engine Operation to
// completion the way gcloud and terraform-provider-google do: poll
// zoneOperations.get until status is DONE, then surface the operation's own
// error. Never assumes a returned operation already settled.
export const waitComputeZoneOperation = async (
  project: string,
  zone: string,
  operation: ComputeOperation,
): Promise<ComputeOperation> => {
  let current = operation;
  while (current.status !== "DONE") {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchComputeZoneOperation(project, zone, current.name);
  }
  const failure = current.error?.errors?.[0];
  if (failure) {
    throw new Error(failure.message ?? `operation ${current.name} failed`);
  }
  return current;
};

export const fetchComputeDisks = async (project: string): Promise<ComputeDisk[]> => {
  const page = await authorizedJSON<{ items?: Record<string, Record<string, unknown>> }>(
    `${computeParent(project)}/aggregated/disks`,
  );
  return flattenAggregated<ComputeDisk>(page.items, "disks");
};

export const fetchComputeNetworks = async (project: string): Promise<ComputeNetwork[]> =>
  (await authorizedJSON<{ items?: ComputeNetwork[] }>(`${computeParent(project)}/global/networks`)).items ?? [];

export const fetchComputeNetwork = (project: string, name: string): Promise<ComputeNetwork> =>
  authorizedJSON<ComputeNetwork>(`${computeParent(project)}/global/networks/${name}`);

// compute.subnetworks.list — the regional subnet list the real console's VPC
// network detail shows, and the read `gcloud compute networks subnets list
// --region` makes.
export const fetchComputeSubnetworks = async (project: string, region: string): Promise<ComputeSubnetwork[]> =>
  (await authorizedJSON<{ items?: ComputeSubnetwork[] }>(`${computeParent(project)}/regions/${region}/subnetworks`))
    .items ?? [];

export const fetchComputeFirewalls = async (project: string): Promise<ComputeFirewall[]> =>
  (await authorizedJSON<{ items?: ComputeFirewall[] }>(`${computeParent(project)}/global/firewalls`)).items ?? [];

// ---------------------------------------------------------------------------
// Cloud Load Balancing (compute.googleapis.com/v1 global load-balancing)
// ---------------------------------------------------------------------------

export interface ComputeBackendService {
  name: string;
  id?: string;
  protocol?: string;
  loadBalancingScheme?: string;
  timeoutSec?: number;
  healthChecks?: string[];
  creationTimestamp?: string;
}

export interface ComputeUrlMap {
  name: string;
  id?: string;
  defaultService?: string;
  creationTimestamp?: string;
}

export interface ComputeForwardingRule {
  name: string;
  id?: string;
  IPAddress?: string;
  IPProtocol?: string;
  portRange?: string;
  target?: string;
  loadBalancingScheme?: string;
  creationTimestamp?: string;
}

export interface ComputeHealthCheck {
  name: string;
  id?: string;
  type?: string;
  checkIntervalSec?: number;
  timeoutSec?: number;
  creationTimestamp?: string;
}

export interface ComputeTargetHttpProxy {
  name: string;
  id?: string;
  urlMap?: string;
  creationTimestamp?: string;
}

export const fetchBackendServices = async (project: string): Promise<ComputeBackendService[]> =>
  (await authorizedJSON<{ items?: ComputeBackendService[] }>(`${computeParent(project)}/global/backendServices`))
    .items ?? [];

export const fetchUrlMaps = async (project: string): Promise<ComputeUrlMap[]> =>
  (await authorizedJSON<{ items?: ComputeUrlMap[] }>(`${computeParent(project)}/global/urlMaps`)).items ?? [];

export const fetchForwardingRules = async (project: string): Promise<ComputeForwardingRule[]> =>
  (await authorizedJSON<{ items?: ComputeForwardingRule[] }>(`${computeParent(project)}/global/forwardingRules`))
    .items ?? [];

export const fetchHealthChecks = async (project: string): Promise<ComputeHealthCheck[]> =>
  (await authorizedJSON<{ items?: ComputeHealthCheck[] }>(`${computeParent(project)}/global/healthChecks`)).items ?? [];

export const fetchTargetHttpProxies = async (project: string): Promise<ComputeTargetHttpProxy[]> =>
  (await authorizedJSON<{ items?: ComputeTargetHttpProxy[] }>(`${computeParent(project)}/global/targetHttpProxies`))
    .items ?? [];

// ---------------------------------------------------------------------------
// Cloud DNS (dns.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface DnsManagedZone {
  name: string;
  id?: string;
  dnsName?: string;
  description?: string;
  visibility?: string;
  nameServers?: string[];
  creationTime?: string;
  labels?: Record<string, string>;
}

export interface DnsResourceRecordSet {
  name: string;
  type: string;
  ttl?: number;
  rrdatas?: string[];
}

const dnsParent = (project: string) => `/dns/v1/projects/${project}`;

export const fetchDnsZones = async (project: string): Promise<DnsManagedZone[]> =>
  (await authorizedJSON<{ managedZones?: DnsManagedZone[] }>(`${dnsParent(project)}/managedZones`)).managedZones ?? [];

export const fetchDnsZone = (project: string, zone: string): Promise<DnsManagedZone> =>
  authorizedJSON<DnsManagedZone>(`${dnsParent(project)}/managedZones/${zone}`);

export const fetchDnsRecordSets = async (project: string, zone: string): Promise<DnsResourceRecordSet[]> =>
  (await authorizedJSON<{ rrsets?: DnsResourceRecordSet[] }>(`${dnsParent(project)}/managedZones/${zone}/rrsets`))
    .rrsets ?? [];

// dns.managedZones.create — synchronous: the real API answers with the created
// ManagedZone, not an operation.
export const createDnsZone = (
  project: string,
  zone: { name: string; dnsName: string; description: string; visibility: string },
): Promise<DnsManagedZone> => authorizedJSONPost<DnsManagedZone>(`${dnsParent(project)}/managedZones`, zone);

export const deleteDnsZone = (project: string, zone: string): Promise<void> =>
  authorizedJSONDelete<void>(`${dnsParent(project)}/managedZones/${zone}`);

// ---------------------------------------------------------------------------
// Serverless VPC Access (vpcaccess.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface VpcAccessConnector {
  name: string;
  network?: string;
  ipCidrRange?: string;
  state?: string;
  machineType?: string;
  minInstances?: number;
  maxInstances?: number;
  minThroughput?: number;
  maxThroughput?: number;
}

export const fetchVpcConnectors = async (project: string): Promise<VpcAccessConnector[]> =>
  (
    await authorizedJSON<{ connectors?: VpcAccessConnector[] }>(
      `/v1/projects/${project}/locations/${CONSOLE_REGION}/connectors`,
    )
  ).connectors ?? [];

// projects.locations.connectors.delete — a long-running operation, driven
// through the same v1 operations.get poll (waitArOperation) the Artifact
// Registry flows use.
export const deleteVpcConnector = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`/v1/projects/${project}/locations/${CONSOLE_REGION}/connectors/${name}`);

// ---------------------------------------------------------------------------
// Cloud SQL (sqladmin.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface SqlInstance {
  name: string;
  project?: string;
  region?: string;
  databaseVersion?: string;
  state?: string;
  instanceType?: string;
  backendType?: string;
  connectionName?: string;
  createTime?: string;
  settings?: { tier?: string };
  ipAddresses?: { ipAddress?: string; type?: string }[];
}

export interface SqlDatabase {
  name: string;
  instance?: string;
  charset?: string;
  collation?: string;
}

export interface SqlUser {
  name: string;
  instance?: string;
  host?: string;
  type?: string;
}

// The Cloud SQL Admin API answers every mutation with its own sql#operation
// resource — `status` runs PENDING → RUNNING → DONE and the failure rides in
// `error.errors`, distinct from the google.longrunning.Operation shape.
export interface SqlOperation {
  name: string;
  status?: string;
  operationType?: string;
  targetId?: string;
  error?: { errors?: { code?: string; message?: string }[] };
}

const sqlParent = (project: string) => `/v1/projects/${project}/instances`;

export const fetchSqlInstances = async (project: string): Promise<SqlInstance[]> =>
  (await authorizedJSON<{ items?: SqlInstance[] }>(sqlParent(project))).items ?? [];

export const fetchSqlInstance = (project: string, name: string): Promise<SqlInstance> =>
  authorizedJSON<SqlInstance>(`${sqlParent(project)}/${name}`);

export const fetchSqlDatabases = async (project: string, instance: string): Promise<SqlDatabase[]> =>
  (await authorizedJSON<{ items?: SqlDatabase[] }>(`${sqlParent(project)}/${instance}/databases`)).items ?? [];

export const fetchSqlUsers = async (project: string, instance: string): Promise<SqlUser[]> =>
  (await authorizedJSON<{ items?: SqlUser[] }>(`${sqlParent(project)}/${instance}/users`)).items ?? [];

export const fetchSqlOperation = (project: string, name: string): Promise<SqlOperation> =>
  authorizedJSON<SqlOperation>(`/v1/projects/${project}/operations/${name}`);

// waitSqlOperation drives a Cloud SQL Admin operation to completion the way
// gcloud sql does: poll operations.get until status is DONE, then surface the
// operation's own error.
export const waitSqlOperation = async (project: string, operation: SqlOperation): Promise<SqlOperation> => {
  let current = operation;
  while (current.status !== "DONE") {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchSqlOperation(project, current.name);
  }
  const failure = current.error?.errors?.[0];
  if (failure) {
    throw new Error(failure.message ?? `operation ${current.name} failed`);
  }
  return current;
};

// sql.instances.insert — the body is the DatabaseInstance itself.
export const createSqlInstance = (
  project: string,
  config: { name: string; databaseVersion: string; tier: string },
): Promise<SqlOperation> =>
  authorizedJSONPost<SqlOperation>(sqlParent(project), {
    name: config.name,
    region: CONSOLE_REGION,
    databaseVersion: config.databaseVersion,
    settings: { tier: config.tier },
  });

export const deleteSqlInstance = (project: string, name: string): Promise<SqlOperation> =>
  authorizedJSONDelete<SqlOperation>(`${sqlParent(project)}/${name}`);

export const createSqlDatabase = (project: string, instance: string, name: string): Promise<SqlOperation> =>
  authorizedJSONPost<SqlOperation>(`${sqlParent(project)}/${instance}/databases`, { name, instance, project });

// ---------------------------------------------------------------------------
// Firestore (firestore.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface FirestoreDatabase {
  name: string;
  uid?: string;
  locationId?: string;
  type?: string;
  concurrencyMode?: string;
  appEngineIntegrationMode?: string;
  createTime?: string;
  updateTime?: string;
}

export const fetchFirestoreDatabases = async (project: string): Promise<FirestoreDatabase[]> =>
  (await authorizedJSON<{ databases?: FirestoreDatabase[] }>(`/v1/projects/${project}/databases`)).databases ?? [];

// projects.databases.create — POST ?databaseId=, a long-running operation on
// the v1 collection.
export const createFirestoreDatabase = (
  project: string,
  databaseId: string,
  config: { type: string; locationId: string },
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(
    `/v1/projects/${project}/databases?databaseId=${encodeURIComponent(databaseId)}`,
    config,
  );

// ---------------------------------------------------------------------------
// Spanner (spanner.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface SpannerInstance {
  name: string;
  config?: string;
  displayName?: string;
  nodeCount?: number;
  processingUnits?: number;
  state?: string;
  labels?: Record<string, string>;
}

export interface SpannerDatabase {
  name: string;
  state?: string;
  createTime?: string;
  versionRetentionPeriod?: string;
  databaseDialect?: string;
}

const spannerParent = (project: string) => `/spanner/v1/projects/${project}/instances`;

export const fetchSpannerInstances = async (project: string): Promise<SpannerInstance[]> =>
  (await authorizedJSON<{ instances?: SpannerInstance[] }>(spannerParent(project))).instances ?? [];

export const fetchSpannerInstance = (project: string, name: string): Promise<SpannerInstance> =>
  authorizedJSON<SpannerInstance>(`${spannerParent(project)}/${name}`);

export const fetchSpannerDatabases = async (project: string, instance: string): Promise<SpannerDatabase[]> =>
  (await authorizedJSON<{ databases?: SpannerDatabase[] }>(`${spannerParent(project)}/${instance}/databases`))
    .databases ?? [];

// projects.instances.create — the body carries instanceId plus the Instance;
// the reply is a google.longrunning.Operation named under the instance.
export const createSpannerInstance = (
  project: string,
  instanceId: string,
  config: { displayName: string; nodeCount: number },
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(spannerParent(project), {
    instanceId,
    instance: {
      config: `projects/${project}/instanceConfigs/regional-${CONSOLE_REGION}`,
      displayName: config.displayName,
      nodeCount: config.nodeCount,
    },
  });

export const deleteSpannerInstance = (project: string, name: string): Promise<unknown> =>
  authorizedJSONDelete(`${spannerParent(project)}/${name}`);

// operations.get for the Spanner collection: the operation name is already a
// full resource path ("projects/p/instances/i/operations/o"), addressed under
// the /spanner/v1 prefix.
export const fetchSpannerOperation = (name: string): Promise<CrmOperation> =>
  authorizedJSON<CrmOperation>(`/spanner/v1/${name}`);

export const waitSpannerOperation = async (operation: CrmOperation): Promise<CrmOperation> => {
  let current = operation;
  while (!current.done) {
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await fetchSpannerOperation(current.name);
  }
  if (current.error) {
    throw new Error(current.error.message ?? `operation ${current.name} failed`);
  }
  return current;
};

// ---------------------------------------------------------------------------
// Bigtable (bigtableadmin.googleapis.com/v2)
// ---------------------------------------------------------------------------

export interface BigtableInstance {
  name: string;
  displayName?: string;
  state?: string;
  type?: string;
  labels?: Record<string, string>;
}

export interface BigtableCluster {
  name: string;
  location?: string;
  state?: string;
  serveNodes?: number;
  defaultStorageType?: string;
}

export interface BigtableTable {
  name: string;
  granularity?: string;
  clusterStates?: Record<string, { replicationState?: string }>;
}

const bigtableParent = (project: string) => `/v2/projects/${project}/instances`;

export const fetchBigtableInstances = async (project: string): Promise<BigtableInstance[]> =>
  (await authorizedJSON<{ instances?: BigtableInstance[] }>(bigtableParent(project))).instances ?? [];

export const fetchBigtableInstance = (project: string, name: string): Promise<BigtableInstance> =>
  authorizedJSON<BigtableInstance>(`${bigtableParent(project)}/${name}`);

export const fetchBigtableClusters = async (project: string, instance: string): Promise<BigtableCluster[]> =>
  (await authorizedJSON<{ clusters?: BigtableCluster[] }>(`${bigtableParent(project)}/${instance}/clusters`))
    .clusters ?? [];

export const fetchBigtableTables = async (project: string, instance: string): Promise<BigtableTable[]> =>
  (await authorizedJSON<{ tables?: BigtableTable[] }>(`${bigtableParent(project)}/${instance}/tables`)).tables ?? [];

// projects.instances.create — instanceId, the Instance, and the initial
// clusters map all ride in the body; the reply is a long-running Operation on
// the top-level /v2/operations collection.
export const createBigtableInstance = (
  project: string,
  instanceId: string,
  config: { displayName: string; zone: string; serveNodes: number },
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(bigtableParent(project), {
    instanceId,
    instance: { displayName: config.displayName, type: "PRODUCTION" },
    clusters: {
      [`${instanceId}-c1`]: {
        location: `projects/${project}/locations/${config.zone}`,
        serveNodes: config.serveNodes,
        defaultStorageType: "SSD",
      },
    },
  });

export const deleteBigtableInstance = (project: string, name: string): Promise<unknown> =>
  authorizedJSONDelete(`${bigtableParent(project)}/${name}`);

// ---------------------------------------------------------------------------
// Memorystore for Redis (redis.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface RedisInstance {
  name: string;
  displayName?: string;
  tier?: string;
  redisVersion?: string;
  memorySizeGb?: number;
  host?: string;
  port?: number;
  state?: string;
  createTime?: string;
  labels?: Record<string, string>;
}

const redisParent = (project: string) => `/v1/projects/${project}/locations/${CONSOLE_REGION}/instances`;

export const fetchRedisInstances = async (project: string): Promise<RedisInstance[]> =>
  (await authorizedJSON<{ instances?: RedisInstance[] }>(redisParent(project))).instances ?? [];

export const fetchRedisInstance = (project: string, name: string): Promise<RedisInstance> =>
  authorizedJSON<RedisInstance>(`${redisParent(project)}/${name}`);

// projects.locations.instances.create — POST ?instanceId=, a long-running
// operation on the v1 locations collection.
export const createRedisInstance = (
  project: string,
  instanceId: string,
  config: { tier: string; memorySizeGb: number },
): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(`${redisParent(project)}?instanceId=${encodeURIComponent(instanceId)}`, config);

export const deleteRedisInstance = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`${redisParent(project)}/${name}`);

// ---------------------------------------------------------------------------
// BigQuery (bigquery.googleapis.com/v2)
// ---------------------------------------------------------------------------

export interface BigQueryDatasetRef {
  projectId?: string;
  datasetId?: string;
}

export interface BigQueryDataset {
  id?: string;
  datasetReference?: BigQueryDatasetRef;
  friendlyName?: string;
  location?: string;
  labels?: Record<string, string> | null;
  creationTime?: string;
  lastModifiedTime?: string;
  description?: string;
}

export interface BigQueryTable {
  id?: string;
  tableReference?: { projectId?: string; datasetId?: string; tableId?: string };
  friendlyName?: string;
  type?: string;
  creationTime?: string;
  numRows?: string;
  numBytes?: string;
}

export interface BigQueryJob {
  id?: string;
  jobReference?: { projectId?: string; jobId?: string; location?: string };
  state?: string;
  status?: { state?: string; errorResult?: { message?: string } };
  configuration?: { jobType?: string };
  statistics?: { creationTime?: string; startTime?: string; endTime?: string };
  user_email?: string;
}

const bigqueryParent = (project: string) => `/bigquery/v2/projects/${project}`;

export const fetchBigQueryDatasets = async (project: string): Promise<BigQueryDataset[]> =>
  (await authorizedJSON<{ datasets?: BigQueryDataset[] }>(`${bigqueryParent(project)}/datasets`)).datasets ?? [];

export const fetchBigQueryDataset = (project: string, dataset: string): Promise<BigQueryDataset> =>
  authorizedJSON<BigQueryDataset>(`${bigqueryParent(project)}/datasets/${dataset}`);

export const fetchBigQueryTables = async (project: string, dataset: string): Promise<BigQueryTable[]> =>
  (await authorizedJSON<{ tables?: BigQueryTable[] }>(`${bigqueryParent(project)}/datasets/${dataset}/tables`))
    .tables ?? [];

export const fetchBigQueryJobs = async (project: string): Promise<BigQueryJob[]> =>
  (await authorizedJSON<{ jobs?: BigQueryJob[] }>(`${bigqueryParent(project)}/jobs?allUsers=true`)).jobs ?? [];

// datasets.insert — synchronous; the real API returns the created Dataset.
export const createBigQueryDataset = (
  project: string,
  datasetId: string,
  location: string,
): Promise<BigQueryDataset> =>
  authorizedJSONPost<BigQueryDataset>(`${bigqueryParent(project)}/datasets`, {
    datasetReference: { projectId: project, datasetId },
    location,
  });

export const deleteBigQueryDataset = (project: string, dataset: string): Promise<void> =>
  authorizedJSONDelete<void>(`${bigqueryParent(project)}/datasets/${dataset}?deleteContents=true`);

// ---------------------------------------------------------------------------
// Pub/Sub (pubsub.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface PubSubTopic {
  name: string;
  labels?: Record<string, string>;
  messageRetentionDuration?: string;
  kmsKeyName?: string;
}

export interface PubSubSubscription {
  name: string;
  topic?: string;
  ackDeadlineSeconds?: number;
  messageRetentionDuration?: string;
  retainAckedMessages?: boolean;
  pushConfig?: { pushEndpoint?: string };
  labels?: Record<string, string>;
}

const pubsubParent = (project: string) => `/v1/projects/${project}`;

export const fetchPubSubTopics = async (project: string): Promise<PubSubTopic[]> =>
  (await authorizedJSON<{ topics?: PubSubTopic[] }>(`${pubsubParent(project)}/topics`)).topics ?? [];

export const fetchPubSubTopic = (project: string, topic: string): Promise<PubSubTopic> =>
  authorizedJSON<PubSubTopic>(`${pubsubParent(project)}/topics/${topic}`);

export const fetchPubSubSubscriptions = async (project: string): Promise<PubSubSubscription[]> =>
  (await authorizedJSON<{ subscriptions?: PubSubSubscription[] }>(`${pubsubParent(project)}/subscriptions`))
    .subscriptions ?? [];

// projects.topics.create is a PUT on the topic's own resource name — the
// method Pub/Sub uses instead of POST-to-collection.
export const createPubSubTopic = (project: string, topic: string): Promise<PubSubTopic> =>
  authorizedJSONPut<PubSubTopic>(`${pubsubParent(project)}/topics/${topic}`, {});

export const deletePubSubTopic = (project: string, topic: string): Promise<unknown> =>
  authorizedJSONDelete(`${pubsubParent(project)}/topics/${topic}`);

// projects.subscriptions.create is likewise a PUT on the subscription's name;
// the body carries the topic it attaches to.
export const createPubSubSubscription = (
  project: string,
  subscription: string,
  topic: string,
  ackDeadlineSeconds: number,
): Promise<PubSubSubscription> =>
  authorizedJSONPut<PubSubSubscription>(`${pubsubParent(project)}/subscriptions/${subscription}`, {
    topic: `projects/${project}/topics/${topic}`,
    ackDeadlineSeconds,
  });

export const deletePubSubSubscription = (project: string, subscription: string): Promise<unknown> =>
  authorizedJSONDelete(`${pubsubParent(project)}/subscriptions/${subscription}`);

// ---------------------------------------------------------------------------
// Dataflow (dataflow.googleapis.com/v1b3)
// ---------------------------------------------------------------------------

export interface DataflowJob {
  id: string;
  name?: string;
  projectId?: string;
  location?: string;
  type?: string;
  currentState?: string;
  currentStateTime?: string;
  createTime?: string;
  startTime?: string;
  labels?: Record<string, string>;
}

export interface DataflowJobMessage {
  id?: string;
  time?: string;
  messageText?: string;
  messageImportance?: string;
}

const dataflowParent = (project: string) => `/v1b3/projects/${project}/locations/${CONSOLE_REGION}/jobs`;

export const fetchDataflowJobs = async (project: string): Promise<DataflowJob[]> =>
  (await authorizedJSON<{ jobs?: DataflowJob[] }>(dataflowParent(project))).jobs ?? [];

export const fetchDataflowJob = (project: string, id: string): Promise<DataflowJob> =>
  authorizedJSON<DataflowJob>(`${dataflowParent(project)}/${id}`);

export const fetchDataflowJobMessages = async (project: string, id: string): Promise<DataflowJobMessage[]> =>
  (await authorizedJSON<{ jobMessages?: DataflowJobMessage[] }>(`${dataflowParent(project)}/${id}/messages`))
    .jobMessages ?? [];

// projects.locations.jobs.update — Dataflow cancels or drains a job by
// PUTting the requested terminal state, the same call `gcloud dataflow jobs
// cancel` makes. Synchronous: the reply is the updated Job.
export const updateDataflowJobState = (project: string, id: string, requestedState: string): Promise<DataflowJob> =>
  authorizedJSONPut<DataflowJob>(`${dataflowParent(project)}/${id}`, { requestedState });

// ---------------------------------------------------------------------------
// Cloud Build (cloudbuild.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface CloudBuildStep {
  name?: string;
  args?: string[];
  id?: string;
  status?: string;
}

export interface CloudBuild {
  id: string;
  name?: string;
  projectId?: string;
  status?: string;
  statusDetail?: string;
  steps?: CloudBuildStep[];
  createTime?: string;
  startTime?: string;
  finishTime?: string;
  logUrl?: string;
  images?: string[];
  substitutions?: Record<string, string>;
  tags?: string[];
}

export interface CloudBuildTrigger {
  id: string;
  name?: string;
  description?: string;
  filename?: string;
  disabled?: boolean;
  createTime?: string;
  triggerTemplate?: { repoName?: string; branchName?: string; tagName?: string };
}

export const fetchCloudBuilds = async (project: string): Promise<CloudBuild[]> =>
  (await authorizedJSON<{ builds?: CloudBuild[] }>(`/v1/projects/${project}/builds`)).builds ?? [];

export const fetchCloudBuild = (project: string, id: string): Promise<CloudBuild> =>
  authorizedJSON<CloudBuild>(`/v1/projects/${project}/builds/${id}`);

export const fetchCloudBuildTriggers = async (project: string): Promise<CloudBuildTrigger[]> =>
  (await authorizedJSON<{ triggers?: CloudBuildTrigger[] }>(`/v1/projects/${project}/triggers`)).triggers ?? [];

// projects.builds.cancel / .retry — both answer with the Build (cancel) or a
// long-running Operation (retry); the console reads the real reply rather than
// assuming either shape.
export const cancelCloudBuild = (project: string, id: string): Promise<CloudBuild> =>
  authorizedJSONPost<CloudBuild>(`/v1/projects/${project}/builds/${id}:cancel`, {});

export const retryCloudBuild = (project: string, id: string): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(`/v1/projects/${project}/builds/${id}:retry`, {});

// ---------------------------------------------------------------------------
// Eventarc (eventarc.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface EventarcTrigger {
  name: string;
  uid?: string;
  createTime?: string;
  updateTime?: string;
  serviceAccount?: string;
  eventFilters?: { attribute?: string; value?: string }[];
  destination?: { cloudRun?: { service?: string; region?: string; path?: string }; cloudFunction?: string };
  transport?: { pubsub?: { topic?: string; subscription?: string } };
  labels?: Record<string, string>;
}

export interface EventarcProvider {
  name: string;
  displayName?: string;
  eventTypes?: { type?: string; description?: string }[];
}

const eventarcParent = (project: string) => `/v1/projects/${project}/locations/${CONSOLE_REGION}`;

export const fetchEventarcTriggers = async (project: string): Promise<EventarcTrigger[]> =>
  (await authorizedJSON<{ triggers?: EventarcTrigger[] }>(`${eventarcParent(project)}/triggers`)).triggers ?? [];

export const fetchEventarcTrigger = (project: string, name: string): Promise<EventarcTrigger> =>
  authorizedJSON<EventarcTrigger>(`${eventarcParent(project)}/triggers/${name}`);

export const fetchEventarcProviders = async (project: string): Promise<EventarcProvider[]> =>
  (await authorizedJSON<{ providers?: EventarcProvider[] }>(`${eventarcParent(project)}/providers`)).providers ?? [];

// projects.locations.triggers.delete — a long-running operation, driven
// through the same v1 operations.get poll the Artifact Registry flows use.
export const deleteEventarcTrigger = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`${eventarcParent(project)}/triggers/${name}`);

// ---------------------------------------------------------------------------
// API Gateway (apigateway.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface ApiGatewayApi {
  name: string;
  displayName?: string;
  managedService?: string;
  state?: string;
  createTime?: string;
  labels?: Record<string, string>;
}

export interface ApiGatewayGateway {
  name: string;
  displayName?: string;
  apiConfig?: string;
  state?: string;
  defaultHostname?: string;
  createTime?: string;
  labels?: Record<string, string>;
}

export const fetchApiGatewayApis = async (project: string): Promise<ApiGatewayApi[]> =>
  (await authorizedJSON<{ apis?: ApiGatewayApi[] }>(`/v1/projects/${project}/locations/global/apis`)).apis ?? [];

export const fetchApiGatewayGateways = async (project: string): Promise<ApiGatewayGateway[]> =>
  (
    await authorizedJSON<{ gateways?: ApiGatewayGateway[] }>(
      `/v1/projects/${project}/locations/${CONSOLE_REGION}/gateways`,
    )
  ).gateways ?? [];

// projects.locations.apis.delete — a long-running operation on the v1
// collection, driven through the same operations.get poll.
export const deleteApiGatewayApi = (project: string, name: string): Promise<CrmOperation> =>
  authorizedJSONDelete<CrmOperation>(`/v1/projects/${project}/locations/global/apis/${name}`);

// ---------------------------------------------------------------------------
// Service Usage (serviceusage.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface EnabledService {
  name: string;
  state?: string;
  parent?: string;
  config?: { name?: string; title?: string };
}

export const fetchEnabledServices = async (project: string): Promise<EnabledService[]> =>
  (await authorizedJSON<{ services?: EnabledService[] }>(`/v1/projects/${project}/services`)).services ?? [];

// services.enable / services.disable — both long-running operations on the v1
// collection, the same calls `gcloud services enable` makes.
export const enableService = (project: string, service: string): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(`/v1/projects/${project}/services/${service}:enable`, {});

export const disableService = (project: string, service: string): Promise<CrmOperation> =>
  authorizedJSONPost<CrmOperation>(`/v1/projects/${project}/services/${service}:disable`, {});

// ---------------------------------------------------------------------------
// Cloud KMS (cloudkms.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface KmsKeyRing {
  name: string;
  createTime?: string;
}

export interface KmsCryptoKeyVersion {
  name: string;
  state?: string;
  protectionLevel?: string;
  algorithm?: string;
  createTime?: string;
  generateTime?: string;
  destroyTime?: string;
}

export interface KmsCryptoKey {
  name: string;
  purpose?: string;
  createTime?: string;
  nextRotationTime?: string;
  rotationPeriod?: string;
  primary?: KmsCryptoKeyVersion;
  versionTemplate?: { protectionLevel?: string; algorithm?: string };
  labels?: Record<string, string>;
}

const kmsParent = (project: string) => `/v1/projects/${project}/locations/${CONSOLE_REGION}/keyRings`;

export const fetchKmsKeyRings = async (project: string): Promise<KmsKeyRing[]> =>
  (await authorizedJSON<{ keyRings?: KmsKeyRing[] | null }>(kmsParent(project))).keyRings ?? [];

export const fetchKmsKeyRing = (project: string, keyRing: string): Promise<KmsKeyRing> =>
  authorizedJSON<KmsKeyRing>(`${kmsParent(project)}/${keyRing}`);

export const fetchKmsCryptoKeys = async (project: string, keyRing: string): Promise<KmsCryptoKey[]> =>
  (await authorizedJSON<{ cryptoKeys?: KmsCryptoKey[] | null }>(`${kmsParent(project)}/${keyRing}/cryptoKeys`))
    .cryptoKeys ?? [];

export const fetchKmsCryptoKeyVersions = async (
  project: string,
  keyRing: string,
  cryptoKey: string,
): Promise<KmsCryptoKeyVersion[]> =>
  (
    await authorizedJSON<{ cryptoKeyVersions?: KmsCryptoKeyVersion[] | null }>(
      `${kmsParent(project)}/${keyRing}/cryptoKeys/${cryptoKey}/cryptoKeyVersions`,
    )
  ).cryptoKeyVersions ?? [];

// projects.locations.keyRings.create — synchronous; the real API returns the
// created KeyRing.
export const createKmsKeyRing = (project: string, keyRingId: string): Promise<KmsKeyRing> =>
  authorizedJSONPost<KmsKeyRing>(`${kmsParent(project)}?keyRingId=${encodeURIComponent(keyRingId)}`, {});

// projects.locations.keyRings.cryptoKeys.create — synchronous; the reply is
// the created CryptoKey with its first (primary) version already generated.
export const createKmsCryptoKey = (
  project: string,
  keyRing: string,
  cryptoKeyId: string,
  purpose: string,
): Promise<KmsCryptoKey> =>
  authorizedJSONPost<KmsCryptoKey>(
    `${kmsParent(project)}/${keyRing}/cryptoKeys?cryptoKeyId=${encodeURIComponent(cryptoKeyId)}`,
    { purpose },
  );

// ---------------------------------------------------------------------------
// Secret Manager (secretmanager.googleapis.com/v1)
// ---------------------------------------------------------------------------

export interface SecretResource {
  name: string;
  createTime?: string;
  labels?: Record<string, string>;
  replication?: { automatic?: Record<string, never>; userManaged?: { replicas?: { location?: string }[] } };
}

export interface SecretVersion {
  name: string;
  state?: string;
  createTime?: string;
  destroyTime?: string;
}

const secretsParent = (project: string) => `/v1/projects/${project}/secrets`;

export const fetchSecrets = async (project: string): Promise<SecretResource[]> =>
  (await authorizedJSON<{ secrets?: SecretResource[] }>(secretsParent(project))).secrets ?? [];

export const fetchSecret = (project: string, secret: string): Promise<SecretResource> =>
  authorizedJSON<SecretResource>(`${secretsParent(project)}/${secret}`);

export const fetchSecretVersions = async (project: string, secret: string): Promise<SecretVersion[]> =>
  (await authorizedJSON<{ versions?: SecretVersion[] }>(`${secretsParent(project)}/${secret}/versions`)).versions ?? [];

// projects.secrets.create — POST ?secretId=, synchronous; the body carries the
// replication policy the real API requires.
export const createSecret = (project: string, secretId: string): Promise<SecretResource> =>
  authorizedJSONPost<SecretResource>(`${secretsParent(project)}?secretId=${encodeURIComponent(secretId)}`, {
    replication: { automatic: {} },
  });

export const deleteSecret = (project: string, secret: string): Promise<unknown> =>
  authorizedJSONDelete(`${secretsParent(project)}/${secret}`);

// projects.secrets.addVersion — the payload is base64 in `data`, the way the
// real API and `gcloud secrets versions add` send it.
export const addSecretVersion = (project: string, secret: string, value: string): Promise<SecretVersion> =>
  authorizedJSONPost<SecretVersion>(`${secretsParent(project)}/${secret}:addVersion`, {
    payload: { data: btoa(value) },
  });

// ---------------------------------------------------------------------------
// Cloud IAM — the project's allow policy (cloudresourcemanager v3)
// ---------------------------------------------------------------------------

export interface IamBinding {
  role: string;
  members?: string[];
  condition?: { title?: string; description?: string; expression?: string };
}

export interface IamPolicy {
  version?: number;
  etag?: string;
  bindings?: IamBinding[];
}

export interface IamRole {
  name: string;
  title?: string;
  description?: string;
  stage?: string;
  includedPermissions?: string[];
}

// projects.getIamPolicy — a POST, the way every Google Cloud IAM read is.
export const fetchProjectIamPolicy = (project: string): Promise<IamPolicy> =>
  authorizedJSONPost<IamPolicy>(`/v3/projects/${project}:getIamPolicy`, {});

// projects.setIamPolicy — the whole policy is replaced, so the caller sends
// back the policy it read with its edit applied (including the etag, which the
// API uses for optimistic concurrency).
export const setProjectIamPolicy = (project: string, policy: IamPolicy): Promise<IamPolicy> =>
  authorizedJSONPost<IamPolicy>(`/v3/projects/${project}:setIamPolicy`, { policy });

// roles.list — the predefined roles a grant can name.
export const fetchIamRoles = async (): Promise<IamRole[]> =>
  (await authorizedJSON<{ roles?: IamRole[] }>("/v1/roles")).roles ?? [];
