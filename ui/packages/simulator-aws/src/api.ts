import {
  awsFetch,
  AwsApiError,
  awsJson,
  awsQuery,
  awsRestJson,
  awsRestJsonDelete,
  awsRestXml,
  awsRestXmlDelete,
  awsRestXmlPut,
} from "./console/federation.js";

// restJson issues an AWS REST-JSON request that carries a JSON body and reads a
// JSON (or empty 204) response — the shape Lambda's CreateFunction,
// UpdateFunctionConfiguration, and TagResource operations speak. It signs
// through the same federated awsFetch every other reader uses; only the request
// shaping lives here. A failure carries the protocol's `__type` error code
// (stripped of any `namespace#` prefix, the way SDKs resolve it) so the operator
// reads the real service error, exactly as awsJson does for the awsjson1.1
// services.
async function restJson<T>(
  service: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const hasBody = body !== undefined && method !== "GET" && method !== "HEAD";
  const response = await awsFetch({
    service,
    method,
    path,
    headers: hasBody ? { "content-type": "application/json" } : undefined,
    body: hasBody ? JSON.stringify(body) : undefined,
  });
  const operation = path;
  if (!response.ok) {
    const text = await response.text();
    let type = "";
    let message = "";
    try {
      const parsed = JSON.parse(text) as { __type?: string; message?: string; Message?: string };
      type = parsed.__type ?? "";
      message = parsed.message ?? parsed.Message ?? "";
    } catch {
      // Not the protocol's JSON error shape — the HTTP status is all there is.
    }
    const code = type.slice(type.lastIndexOf("#") + 1);
    throw new AwsApiError(
      code ? `${operation}: ${code}: ${message}` : `${operation} returned HTTP ${response.status}`,
      code,
    );
  }
  const text = await response.text();
  return (text ? JSON.parse(text) : {}) as T;
}

// The console reads the real AWS APIs — including ECS, Lambda, ECR, S3,
// CloudWatch, IAM, Amazon Data Firehose, AWS Private Certificate Authority,
// and AWS Organizations — over federated, SigV4-signed requests, rendering the
// true resource shapes rather than a console-specific projection.

export type ECSTaskStatus = "PROVISIONING" | "PENDING" | "RUNNING" | "STOPPED" | "DEPROVISIONING";

export interface ECSTask {
  taskArn: string;
  status: ECSTaskStatus;
  clusterArn: string;
  launchType: string;
  cpu: string;
  memory: string;
}

interface DescribeTasksTask {
  taskArn?: string;
  lastStatus?: string;
  clusterArn?: string;
  launchType?: string;
  cpu?: string;
  memory?: string;
}

// ECS tasks live in clusters, and ListTasks is per-cluster, so the console
// enumerates clusters first — the way the real console shows tasks per cluster.
export const fetchECSTasks = async (): Promise<ECSTask[]> => {
  const clusters = await awsJson<{ clusterArns?: string[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.ListClusters",
    {},
  );
  const tasks: ECSTask[] = [];
  for (const cluster of clusters.clusterArns ?? []) {
    const listed = await awsJson<{ taskArns?: string[] }>("ecs", "AmazonEC2ContainerServiceV20141113.ListTasks", {
      cluster,
    });
    const taskArns = listed.taskArns ?? [];
    if (taskArns.length === 0) continue;
    const described = await awsJson<{ tasks?: DescribeTasksTask[] }>(
      "ecs",
      "AmazonEC2ContainerServiceV20141113.DescribeTasks",
      { cluster, tasks: taskArns },
    );
    for (const task of described.tasks ?? []) {
      tasks.push({
        taskArn: task.taskArn ?? "",
        status: (task.lastStatus ?? "PROVISIONING") as ECSTaskStatus,
        clusterArn: task.clusterArn ?? "",
        launchType: task.launchType ?? "",
        cpu: task.cpu ?? "",
        memory: task.memory ?? "",
      });
    }
  }
  return tasks;
};

export interface ECSServiceDeployment {
  id: string;
  status: string;
  taskDefinition: string;
  desiredCount: number;
  runningCount: number;
  pendingCount: number;
  rolloutState: string;
  rolloutStateReason: string;
  createdAt?: number;
  updatedAt?: number;
}

export interface ECSServiceEvent {
  id: string;
  createdAt?: number;
  message: string;
}

export interface ECSService {
  serviceArn: string;
  serviceName: string;
  clusterArn: string;
  taskDefinition: string;
  desiredCount: number;
  runningCount: number;
  pendingCount: number;
  status: string;
  launchType: string;
  platformVersion: string;
  deployments: ECSServiceDeployment[];
  events: ECSServiceEvent[];
  serviceRegistries: { registryArn?: string; containerName?: string; containerPort?: number; port?: number }[];
  deploymentConfiguration?: {
    maximumPercent?: number;
    minimumHealthyPercent?: number;
    deploymentCircuitBreaker?: {
      enable?: boolean;
      rollback?: boolean;
      resetOnHealthyTask?: boolean;
      thresholdConfiguration?: { type?: string; value?: number };
    };
    alarms?: { alarmNames?: string[]; enable?: boolean; rollback?: boolean };
  };
}

interface ECSServiceWire extends Omit<ECSService, "deployments" | "events" | "serviceRegistries"> {
  deployments?: ECSServiceDeployment[];
  events?: ECSServiceEvent[];
  serviceRegistries?: ECSService["serviceRegistries"];
}

function ecsServiceFromWire(service: ECSServiceWire): ECSService {
  return {
    ...service,
    deployments: service.deployments ?? [],
    events: service.events ?? [],
    serviceRegistries: service.serviceRegistries ?? [],
  };
}

export const fetchECSServices = async (): Promise<ECSService[]> => {
  const clusters = await fetchECSClusters();
  const services: ECSService[] = [];
  for (const cluster of clusters) {
    const listed = await awsJson<{ serviceArns?: string[] }>(
      "ecs",
      "AmazonEC2ContainerServiceV20141113.ListServices",
      { cluster },
    );
    if ((listed.serviceArns ?? []).length === 0) continue;
    const described = await awsJson<{ services?: ECSServiceWire[] }>(
      "ecs",
      "AmazonEC2ContainerServiceV20141113.DescribeServices",
      { cluster, services: listed.serviceArns },
    );
    services.push(...(described.services ?? []).map(ecsServiceFromWire));
  }
  return services;
};

export const fetchECSService = async (cluster: string, serviceName: string): Promise<ECSService> => {
  const described = await awsJson<{ services?: ECSServiceWire[]; failures?: { reason?: string }[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.DescribeServices",
    { cluster, services: [serviceName] },
  );
  const service = described.services?.[0];
  if (!service) throw new Error(described.failures?.[0]?.reason ?? `Amazon ECS service ${serviceName} was not found`);
  return ecsServiceFromWire(service);
};

export const updateECSServiceDesiredCount = async (
  cluster: string,
  serviceName: string,
  desiredCount: number,
): Promise<void> => {
  await awsJson("ecs", "AmazonEC2ContainerServiceV20141113.UpdateService", {
    cluster,
    service: serviceName,
    desiredCount,
  });
};

export const deleteECSService = async (cluster: string, serviceName: string): Promise<void> => {
  await awsJson("ecs", "AmazonEC2ContainerServiceV20141113.DeleteService", {
    cluster,
    service: serviceName,
    force: true,
  });
};

// Real ECS never deletes a task record on request — a task is stopped, and
// the service reaps the STOPPED record on its own schedule — so the console's
// task action is StopTask, matching what the real console's "Stop" offers.
export const stopECSTask = async (clusterArn: string, taskArn: string): Promise<void> => {
  await awsJson("ecs", "AmazonEC2ContainerServiceV20141113.StopTask", {
    cluster: clusterArn,
    task: taskArn,
    reason: "Stopped from the AWS console simulator",
  });
};

// ECS task detail — DescribeTasks (the single-task read) and
// DescribeTaskDefinition, the two real ECS operations the task detail page
// reads. A task ARN carries its cluster's short name
// (`arn:aws:ecs:<region>:<account>:task/<cluster>/<task-id>`), which is what
// DescribeTasks accepts for `cluster` — the same short name or full ARN the
// aws CLI and SDKs accept.
function clusterNameFromTaskArn(taskArn: string): string {
  const match = /:task\/([^/]+)\//.exec(taskArn);
  if (!match) throw new Error(`could not read a cluster name from task ARN: ${taskArn}`);
  return match[1];
}

export interface ECSContainerDetail {
  name: string;
  image: string;
  lastStatus: string;
  exitCode?: number;
  privateIpv4Address?: string;
}

export interface ECSAttachmentDetail {
  id: string;
  type: string;
  status: string;
  details: { name: string; value: string }[];
}

export interface ECSTaskDetail {
  taskArn: string;
  taskDefinitionArn: string;
  clusterArn: string;
  status: ECSTaskStatus;
  desiredStatus: string;
  connectivity: string;
  launchType: string;
  cpu: string;
  memory: string;
  group: string;
  createdAt?: number;
  startedAt?: number;
  stoppedAt?: number;
  stopCode: string;
  stoppedReason: string;
  containers: ECSContainerDetail[];
  attachments: ECSAttachmentDetail[];
  networkConfiguration?: { subnets: string[]; securityGroups: string[]; assignPublicIp: string };
}

interface DescribeTasksTaskFull extends DescribeTasksTask {
  taskDefinitionArn?: string;
  desiredStatus?: string;
  connectivity?: string;
  group?: string;
  createdAt?: number;
  startedAt?: number;
  stoppedAt?: number;
  stopCode?: string;
  stoppedReason?: string;
  containers?: {
    name?: string;
    lastStatus?: string;
    exitCode?: number;
    networkInterfaces?: { privateIpv4Address?: string }[];
  }[];
  attachments?: {
    id?: string;
    type?: string;
    status?: string;
    details?: { name?: string; value?: string }[];
  }[];
  networkConfiguration?: {
    awsvpcConfiguration?: { subnets?: string[]; securityGroups?: string[]; assignPublicIp?: string };
  };
}

export const fetchECSTaskDetail = async (taskArn: string): Promise<ECSTaskDetail> => {
  const cluster = clusterNameFromTaskArn(taskArn);
  const described = await awsJson<{ tasks?: DescribeTasksTaskFull[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.DescribeTasks",
    { cluster, tasks: [taskArn] },
  );
  const task = described.tasks?.[0];
  if (!task) throw new Error(`DescribeTasks returned no task for ${taskArn}`);
  const vpc = task.networkConfiguration?.awsvpcConfiguration;
  return {
    taskArn: task.taskArn ?? taskArn,
    taskDefinitionArn: task.taskDefinitionArn ?? "",
    clusterArn: task.clusterArn ?? "",
    status: (task.lastStatus ?? "PROVISIONING") as ECSTaskStatus,
    desiredStatus: task.desiredStatus ?? "",
    connectivity: task.connectivity ?? "",
    launchType: task.launchType ?? "",
    cpu: task.cpu ?? "",
    memory: task.memory ?? "",
    group: task.group ?? "",
    createdAt: task.createdAt,
    startedAt: task.startedAt,
    stoppedAt: task.stoppedAt,
    stopCode: task.stopCode ?? "",
    stoppedReason: task.stoppedReason ?? "",
    containers: (task.containers ?? []).map((container) => ({
      name: container.name ?? "",
      // DescribeTasks doesn't echo the image — it lives on the task
      // definition's container definitions, joined in by the page.
      image: "",
      lastStatus: container.lastStatus ?? "",
      exitCode: container.exitCode,
      privateIpv4Address: container.networkInterfaces?.[0]?.privateIpv4Address,
    })),
    attachments: (task.attachments ?? []).map((attachment) => ({
      id: attachment.id ?? "",
      type: attachment.type ?? "",
      status: attachment.status ?? "",
      details: (attachment.details ?? []).map((detail) => ({ name: detail.name ?? "", value: detail.value ?? "" })),
    })),
    networkConfiguration: vpc
      ? { subnets: vpc.subnets ?? [], securityGroups: vpc.securityGroups ?? [], assignPublicIp: vpc.assignPublicIp ?? "" }
      : undefined,
  };
};

export interface ECSContainerDefinitionDetail {
  name: string;
  image: string;
  cpu?: number;
  memory?: number;
  memoryReservation?: number;
  essential: boolean;
  environment: { name: string; value: string }[];
  portMappings: { containerPort: number; hostPort?: number; protocol?: string }[];
  entryPoint: string[];
  command: string[];
  logDriver?: string;
}

export interface ECSTaskDefinitionDetail {
  taskDefinitionArn: string;
  family: string;
  revision: number;
  cpu: string;
  memory: string;
  networkMode: string;
  requiresCompatibilities: string[];
  executionRoleArn: string;
  taskRoleArn: string;
  containerDefinitions: ECSContainerDefinitionDetail[];
}

interface TaskDefinitionWire {
  taskDefinitionArn?: string;
  family?: string;
  revision?: number;
  cpu?: string;
  memory?: string;
  networkMode?: string;
  requiresCompatibilities?: string[];
  executionRoleArn?: string;
  taskRoleArn?: string;
  containerDefinitions?: {
    name?: string;
    image?: string;
    cpu?: number;
    memory?: number;
    memoryReservation?: number;
    essential?: boolean;
    environment?: { name?: string; value?: string }[];
    portMappings?: { containerPort?: number; hostPort?: number; protocol?: string }[];
    entryPoint?: string[];
    command?: string[];
    logConfiguration?: { logDriver?: string };
  }[];
}

export const fetchECSTaskDefinition = async (taskDefinitionArn: string): Promise<ECSTaskDefinitionDetail> => {
  const described = await awsJson<{ taskDefinition?: TaskDefinitionWire }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition",
    { taskDefinition: taskDefinitionArn },
  );
  const td = described.taskDefinition;
  if (!td) throw new Error(`DescribeTaskDefinition returned no taskDefinition for ${taskDefinitionArn}`);
  return {
    taskDefinitionArn: td.taskDefinitionArn ?? taskDefinitionArn,
    family: td.family ?? "",
    revision: td.revision ?? 0,
    cpu: td.cpu ?? "",
    memory: td.memory ?? "",
    networkMode: td.networkMode ?? "",
    requiresCompatibilities: td.requiresCompatibilities ?? [],
    executionRoleArn: td.executionRoleArn ?? "",
    taskRoleArn: td.taskRoleArn ?? "",
    containerDefinitions: (td.containerDefinitions ?? []).map((container) => ({
      name: container.name ?? "",
      image: container.image ?? "",
      cpu: container.cpu,
      memory: container.memory,
      memoryReservation: container.memoryReservation,
      essential: container.essential ?? true,
      environment: (container.environment ?? []).map((entry) => ({ name: entry.name ?? "", value: entry.value ?? "" })),
      portMappings: (container.portMappings ?? []).map((mapping) => ({
        containerPort: mapping.containerPort ?? 0,
        hostPort: mapping.hostPort,
        protocol: mapping.protocol,
      })),
      entryPoint: container.entryPoint ?? [],
      command: container.command ?? [],
      logDriver: container.logConfiguration?.logDriver,
    })),
  };
};

// ECS clusters and task-definition families — ListClusters and
// ListTaskDefinitionFamilies feed the "Run task" form's cluster and task
// definition pickers, the same reads the real console's Run task wizard makes
// to populate its dropdowns.
export const fetchECSClusters = async (): Promise<string[]> => {
  const listed = await awsJson<{ clusterArns?: string[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.ListClusters",
    {},
  );
  return listed.clusterArns ?? [];
};

export const fetchECSTaskDefinitionFamilies = async (): Promise<string[]> => {
  const listed = await awsJson<{ families?: string[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.ListTaskDefinitionFamilies",
    { status: "ACTIVE" },
  );
  return listed.families ?? [];
};

// RegisterTaskDefinition — the console registers a minimal single-container
// task definition inline when the operator chooses to define one in the Run
// task form, the same operation the real console's inline task-definition
// authoring drives. Returns the family:revision reference RunTask accepts.
export interface RegisterTaskDefinitionInput {
  family: string;
  image: string;
  cpu: string;
  memory: string;
  launchType: "EC2" | "FARGATE";
}

export const registerECSTaskDefinition = async (input: RegisterTaskDefinitionInput): Promise<string> => {
  const requiresCompatibilities = input.launchType === "FARGATE" ? ["FARGATE"] : ["EC2"];
  const body: Record<string, unknown> = {
    family: input.family,
    requiresCompatibilities,
    networkMode: input.launchType === "FARGATE" ? "awsvpc" : "bridge",
    containerDefinitions: [
      {
        name: input.family,
        image: input.image,
        essential: true,
        ...(input.cpu ? { cpu: Number(input.cpu) } : {}),
        ...(input.memory ? { memory: Number(input.memory) } : {}),
      },
    ],
  };
  // Fargate requires task-level cpu and memory; EC2 does not, so the console
  // sends them only when the operator supplied them.
  if (input.cpu) body.cpu = input.cpu;
  if (input.memory) body.memory = input.memory;
  const registered = await awsJson<{ taskDefinition?: { family?: string; revision?: number } }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition",
    body,
  );
  const td = registered.taskDefinition;
  if (!td?.family || td.revision === undefined) throw new Error("RegisterTaskDefinition returned no task definition");
  return `${td.family}:${td.revision}`;
};

// RunTask — launches the given task definition on the chosen cluster, the same
// operation the real console's "Run task" wizard drives.
export interface RunTaskInput {
  cluster: string;
  taskDefinition: string;
  launchType: "EC2" | "FARGATE";
  count: number;
}

export const runECSTask = async (input: RunTaskInput): Promise<string[]> => {
  const run = await awsJson<{ tasks?: { taskArn?: string }[] }>(
    "ecs",
    "AmazonEC2ContainerServiceV20141113.RunTask",
    {
      cluster: input.cluster,
      taskDefinition: input.taskDefinition,
      launchType: input.launchType,
      count: input.count,
    },
  );
  return (run.tasks ?? []).map((task) => task.taskArn ?? "");
};

export type LambdaState = "Pending" | "Active" | "Inactive" | "Failed";

export interface LambdaFunction {
  name: string;
  runtime: string;
  state: LambdaState;
  memorySize: number;
  timeout: number;
  lastModified: string;
}

interface LambdaListEntry {
  FunctionName?: string;
  Runtime?: string;
  State?: string;
  MemorySize?: number;
  Timeout?: number;
  LastModified?: string;
}

export const fetchLambdaFunctions = async (): Promise<LambdaFunction[]> => {
  const listed = await awsRestJson<{ Functions?: LambdaListEntry[] }>("lambda", "/2015-03-31/functions");
  return (listed.Functions ?? []).map((fn) => ({
    name: fn.FunctionName ?? "",
    runtime: fn.Runtime ?? "",
    state: (fn.State ?? "Active") as LambdaState,
    memorySize: fn.MemorySize ?? 0,
    timeout: fn.Timeout ?? 0,
    lastModified: fn.LastModified ?? "",
  }));
};

export const deleteLambdaFunction = async (functionName: string): Promise<void> => {
  await awsRestJsonDelete("lambda", `/2015-03-31/functions/${encodeURIComponent(functionName)}`);
};

// Lambda function detail — GetFunction, which answers Configuration, Code,
// and Tags in one read (there is no separate GetFunctionConfiguration call
// this page needs: GetFunction already carries every configuration field).

export interface LambdaFunctionDetail {
  name: string;
  arn: string;
  runtime: string;
  role: string;
  handler: string;
  codeSha256: string;
  codeSize: number;
  description: string;
  memorySize: number;
  timeout: number;
  environment: { name: string; value: string }[];
  state: LambdaState;
  lastUpdateStatus: string;
  lastModified: string;
  revisionId: string;
  version: string;
  packageType: string;
  architectures: string[];
  vpcConfig?: { subnetIds: string[]; securityGroupIds: string[]; vpcId: string };
  layers: { arn: string; codeSize: number }[];
  imageConfig?: { entryPoint: string[]; command: string[]; workingDirectory: string };
}

export interface LambdaFunctionCode {
  location: string;
  repositoryType: string;
  imageUri?: string;
  resolvedImageUri?: string;
}

interface LambdaGetFunctionResponse {
  Configuration?: {
    FunctionName?: string;
    FunctionArn?: string;
    Runtime?: string;
    Role?: string;
    Handler?: string;
    CodeSha256?: string;
    CodeSize?: number;
    Description?: string;
    MemorySize?: number;
    Timeout?: number;
    Environment?: { Variables?: Record<string, string> };
    State?: string;
    LastUpdateStatus?: string;
    LastModified?: string;
    RevisionId?: string;
    Version?: string;
    PackageType?: string;
    Architectures?: string[];
    VpcConfig?: { SubnetIds?: string[]; SecurityGroupIds?: string[]; VpcId?: string };
    Layers?: { Arn?: string; CodeSize?: number }[];
    ImageConfigResponse?: {
      ImageConfig?: { EntryPoint?: string[]; Command?: string[]; WorkingDirectory?: string };
    };
  };
  Code?: { Location?: string; RepositoryType?: string; ImageUri?: string; ResolvedImageUri?: string };
  Tags?: Record<string, string>;
}

export const fetchLambdaFunctionDetail = async (
  functionName: string,
): Promise<{ configuration: LambdaFunctionDetail; code: LambdaFunctionCode; tags: Record<string, string> }> => {
  const response = await awsRestJson<LambdaGetFunctionResponse>(
    "lambda",
    `/2015-03-31/functions/${encodeURIComponent(functionName)}`,
  );
  const cfg = response.Configuration ?? {};
  const variables = cfg.Environment?.Variables ?? {};
  return {
    configuration: {
      name: cfg.FunctionName ?? functionName,
      arn: cfg.FunctionArn ?? "",
      runtime: cfg.Runtime ?? "",
      role: cfg.Role ?? "",
      handler: cfg.Handler ?? "",
      codeSha256: cfg.CodeSha256 ?? "",
      codeSize: cfg.CodeSize ?? 0,
      description: cfg.Description ?? "",
      memorySize: cfg.MemorySize ?? 0,
      timeout: cfg.Timeout ?? 0,
      environment: Object.entries(variables).map(([name, value]) => ({ name, value })),
      state: (cfg.State ?? "Active") as LambdaState,
      lastUpdateStatus: cfg.LastUpdateStatus ?? "",
      lastModified: cfg.LastModified ?? "",
      revisionId: cfg.RevisionId ?? "",
      version: cfg.Version ?? "",
      packageType: cfg.PackageType ?? "",
      architectures: cfg.Architectures ?? [],
      layers: (cfg.Layers ?? []).map((layer) => ({ arn: layer.Arn ?? "", codeSize: layer.CodeSize ?? 0 })),
      imageConfig: cfg.ImageConfigResponse?.ImageConfig
        ? {
            entryPoint: cfg.ImageConfigResponse.ImageConfig.EntryPoint ?? [],
            command: cfg.ImageConfigResponse.ImageConfig.Command ?? [],
            workingDirectory: cfg.ImageConfigResponse.ImageConfig.WorkingDirectory ?? "",
          }
        : undefined,
      vpcConfig: cfg.VpcConfig
        ? {
            subnetIds: cfg.VpcConfig.SubnetIds ?? [],
            securityGroupIds: cfg.VpcConfig.SecurityGroupIds ?? [],
            vpcId: cfg.VpcConfig.VpcId ?? "",
          }
        : undefined,
    },
    code: {
      location: response.Code?.Location ?? "",
      repositoryType: response.Code?.RepositoryType ?? "",
      imageUri: response.Code?.ImageUri,
      resolvedImageUri: response.Code?.ResolvedImageUri,
    },
    tags: response.Tags ?? {},
  };
};

// UpdateFunctionConfiguration — PUT /2015-03-31/functions/{name}/configuration,
// the real Lambda operation the console's "Edit configuration" flow drives.
// The console sends only the fields the operator edited (memory, timeout,
// description, environment); real Lambda leaves any omitted field unchanged,
// so the caller passes exactly what changed.
export interface LambdaConfigurationUpdate {
  memorySize?: number;
  timeout?: number;
  description?: string;
  environment?: { name: string; value: string }[];
  runtime?: string;
  handler?: string;
  role?: string;
  layers?: string[];
}

export const updateLambdaFunctionConfiguration = async (
  functionName: string,
  update: LambdaConfigurationUpdate,
): Promise<void> => {
  const body: Record<string, unknown> = {};
  if (update.memorySize !== undefined) body.MemorySize = update.memorySize;
  if (update.timeout !== undefined) body.Timeout = update.timeout;
  if (update.description !== undefined) body.Description = update.description;
  if (update.environment !== undefined) {
    body.Environment = {
      Variables: Object.fromEntries(update.environment.map((entry) => [entry.name, entry.value])),
    };
  }
  if (update.runtime !== undefined) body.Runtime = update.runtime;
  if (update.handler !== undefined) body.Handler = update.handler;
  if (update.role !== undefined) body.Role = update.role;
  if (update.layers !== undefined) body.Layers = update.layers;
  await restJson(
    "lambda",
    "PUT",
    `/2015-03-31/functions/${encodeURIComponent(functionName)}/configuration`,
    body,
  );
};

// CreateFunction — POST /2015-03-31/functions. The console offers the two
// package types real Lambda accepts: a container image (Code.ImageUri, the
// simplest faithful path) or a Zip archive staged in Amazon S3 (Code.S3Bucket
// / Code.S3Key with a runtime and handler), matching what the real console's
// "Create function" wizard collects.
export interface CreateLambdaFunctionInput {
  functionName: string;
  role: string;
  memorySize: number;
  timeout: number;
  description?: string;
  packageType: "Image" | "Zip";
  imageUri?: string;
  runtime?: string;
  handler?: string;
  s3Bucket?: string;
  s3Key?: string;
}

export const createLambdaFunction = async (input: CreateLambdaFunctionInput): Promise<void> => {
  const body: Record<string, unknown> = {
    FunctionName: input.functionName,
    Role: input.role,
    MemorySize: input.memorySize,
    Timeout: input.timeout,
    PackageType: input.packageType,
  };
  if (input.description) body.Description = input.description;
  if (input.packageType === "Image") {
    body.Code = { ImageUri: input.imageUri };
  } else {
    body.Runtime = input.runtime;
    body.Handler = input.handler;
    body.Code = { S3Bucket: input.s3Bucket, S3Key: input.s3Key };
  }
  await restJson("lambda", "POST", "/2015-03-31/functions", body);
};

export interface LambdaInvokeResult {
  payload: string;
  functionError: string;
}

// Invoke — POST /2015-03-31/functions/{name}/invocations. A RequestResponse
// invocation returns the function's raw response payload; real Lambda reports a
// handler error out-of-band in the X-Amz-Function-Error response header (the
// payload then carries the error document), which the console surfaces exactly
// as the real console's "Test" tab does.
export const invokeLambdaFunction = async (
  functionName: string,
  payload: string,
): Promise<LambdaInvokeResult> => {
  const response = await awsFetch({
    service: "lambda",
    method: "POST",
    path: `/2015-03-31/functions/${encodeURIComponent(functionName)}/invocations`,
    headers: { "content-type": "application/json" },
    body: payload,
  });
  const text = await response.text();
  if (!response.ok) {
    let type = "";
    let message = "";
    try {
      const parsed = JSON.parse(text) as { __type?: string; message?: string; Message?: string };
      type = parsed.__type ?? "";
      message = parsed.message ?? parsed.Message ?? "";
    } catch {
      // Not the protocol's JSON error shape — the HTTP status is all there is.
    }
    const code = type.slice(type.lastIndexOf("#") + 1);
    throw new AwsApiError(
      code ? `Invoke: ${code}: ${message}` : `Invoke returned HTTP ${response.status}`,
      code,
    );
  }
  return { payload: text, functionError: response.headers.get("X-Amz-Function-Error") ?? "" };
};

export interface LambdaVersion {
  version: string;
  arn: string;
  description: string;
  runtime: string;
  lastModified: string;
  codeSize: number;
}

export const fetchLambdaVersions = async (functionName: string): Promise<LambdaVersion[]> => {
  const response = await awsRestJson<{
    Versions?: {
      Version?: string;
      FunctionArn?: string;
      Description?: string;
      Runtime?: string;
      LastModified?: string;
      CodeSize?: number;
    }[];
  }>("lambda", `/2015-03-31/functions/${encodeURIComponent(functionName)}/versions`);
  return (response.Versions ?? []).map((version) => ({
    version: version.Version ?? "",
    arn: version.FunctionArn ?? "",
    description: version.Description ?? "",
    runtime: version.Runtime ?? "",
    lastModified: version.LastModified ?? "",
    codeSize: version.CodeSize ?? 0,
  }));
};

export interface LambdaAlias {
  name: string;
  arn: string;
  functionVersion: string;
  description: string;
  revisionId: string;
}

export const fetchLambdaAliases = async (functionName: string): Promise<LambdaAlias[]> => {
  const response = await awsRestJson<{
    Aliases?: {
      Name?: string;
      AliasArn?: string;
      FunctionVersion?: string;
      Description?: string;
      RevisionId?: string;
    }[];
  }>("lambda", `/2015-03-31/functions/${encodeURIComponent(functionName)}/aliases`);
  return (response.Aliases ?? []).map((alias) => ({
    name: alias.Name ?? "",
    arn: alias.AliasArn ?? "",
    functionVersion: alias.FunctionVersion ?? "",
    description: alias.Description ?? "",
    revisionId: alias.RevisionId ?? "",
  }));
};

export const publishLambdaVersion = async (functionName: string, description: string): Promise<LambdaVersion> => {
  const version = await restJson<{
    Version?: string;
    FunctionArn?: string;
    Description?: string;
    Runtime?: string;
    LastModified?: string;
    CodeSize?: number;
  }>("lambda", "POST", `/2015-03-31/functions/${encodeURIComponent(functionName)}/versions`, {
    Description: description,
  });
  return {
    version: version.Version ?? "",
    arn: version.FunctionArn ?? "",
    description: version.Description ?? "",
    runtime: version.Runtime ?? "",
    lastModified: version.LastModified ?? "",
    codeSize: version.CodeSize ?? 0,
  };
};

export const createLambdaAlias = async (
  functionName: string,
  name: string,
  functionVersion: string,
  description: string,
): Promise<void> => {
  await restJson("lambda", "POST", `/2015-03-31/functions/${encodeURIComponent(functionName)}/aliases`, {
    Name: name,
    FunctionVersion: functionVersion,
    Description: description,
  });
};

export const updateLambdaAlias = async (
  functionName: string,
  aliasName: string,
  functionVersion: string,
  description: string,
): Promise<void> => {
  await restJson(
    "lambda",
    "PUT",
    `/2015-03-31/functions/${encodeURIComponent(functionName)}/aliases/${encodeURIComponent(aliasName)}`,
    { FunctionVersion: functionVersion, Description: description },
  );
};

export const deleteLambdaAlias = async (functionName: string, aliasName: string): Promise<void> => {
  await restJson(
    "lambda",
    "DELETE",
    `/2015-03-31/functions/${encodeURIComponent(functionName)}/aliases/${encodeURIComponent(aliasName)}`,
  );
};

export interface LambdaCodeUpdate {
  imageUri?: string;
  s3Bucket?: string;
  s3Key?: string;
  publish?: boolean;
}

export const updateLambdaFunctionCode = async (
  functionName: string,
  update: LambdaCodeUpdate,
): Promise<void> => {
  await restJson("lambda", "PUT", `/2015-03-31/functions/${encodeURIComponent(functionName)}/code`, {
    ...(update.imageUri ? { ImageUri: update.imageUri } : { S3Bucket: update.s3Bucket, S3Key: update.s3Key }),
    Publish: update.publish ?? false,
  });
};

export interface LambdaEventSourceMapping {
  uuid: string;
  eventSourceArn: string;
  functionArn: string;
  state: string;
  stateTransitionReason: string;
  batchSize: number;
  lastModified: number;
}

export interface LambdaEventSourceMappingUpdate {
  eventSourceArn?: string;
  enabled: boolean;
  batchSize: number;
  startingPosition?: "LATEST" | "TRIM_HORIZON";
}

export const fetchLambdaEventSourceMappings = async (
  functionName: string,
): Promise<LambdaEventSourceMapping[]> => {
  const query = new URLSearchParams({ FunctionName: functionName });
  const response = await awsRestJson<{
    EventSourceMappings?: {
      UUID?: string;
      EventSourceArn?: string;
      FunctionArn?: string;
      State?: string;
      StateTransitionReason?: string;
      BatchSize?: number;
      LastModified?: number;
    }[];
  }>("lambda", `/2015-03-31/event-source-mappings?${query}`);
  return (response.EventSourceMappings ?? []).map((mapping) => ({
    uuid: mapping.UUID ?? "",
    eventSourceArn: mapping.EventSourceArn ?? "",
    functionArn: mapping.FunctionArn ?? "",
    state: mapping.State ?? "",
    stateTransitionReason: mapping.StateTransitionReason ?? "",
    batchSize: mapping.BatchSize ?? 0,
    lastModified: mapping.LastModified ?? 0,
  }));
};

export const createLambdaEventSourceMapping = async (
  functionName: string,
  update: LambdaEventSourceMappingUpdate,
): Promise<void> => {
  await restJson("lambda", "POST", "/2015-03-31/event-source-mappings", {
    FunctionName: functionName,
    EventSourceArn: update.eventSourceArn,
    Enabled: update.enabled,
    BatchSize: update.batchSize,
    ...(update.startingPosition ? { StartingPosition: update.startingPosition } : {}),
  });
};

export const updateLambdaEventSourceMapping = async (
  uuid: string,
  update: LambdaEventSourceMappingUpdate,
): Promise<void> => {
  await restJson("lambda", "PUT", `/2015-03-31/event-source-mappings/${encodeURIComponent(uuid)}`, {
    Enabled: update.enabled,
    BatchSize: update.batchSize,
  });
};

export const deleteLambdaEventSourceMapping = async (uuid: string): Promise<void> => {
  await restJson("lambda", "DELETE", `/2015-03-31/event-source-mappings/${encodeURIComponent(uuid)}`);
};

export interface LambdaConcurrency {
  reservedConcurrentExecutions?: number;
}

export const fetchLambdaConcurrency = async (functionName: string): Promise<LambdaConcurrency> =>
  restJson<LambdaConcurrencyWire>(
    "lambda",
    "GET",
    `/2017-10-31/functions/${encodeURIComponent(functionName)}/concurrency`,
  ).then((response) => ({ reservedConcurrentExecutions: response.ReservedConcurrentExecutions }));

export const putLambdaConcurrency = async (
  functionName: string,
  reservedConcurrentExecutions: number,
): Promise<void> => {
  await restJson("lambda", "PUT", `/2017-10-31/functions/${encodeURIComponent(functionName)}/concurrency`, {
    ReservedConcurrentExecutions: reservedConcurrentExecutions,
  });
};

export const deleteLambdaConcurrency = async (functionName: string): Promise<void> => {
  await restJson("lambda", "DELETE", `/2017-10-31/functions/${encodeURIComponent(functionName)}/concurrency`);
};

export interface LambdaProvisionedConcurrency {
  functionArn: string;
  qualifier: string;
  requested: number;
  available: number;
  allocated: number;
  status: string;
  lastModified: string;
}

export const fetchLambdaProvisionedConcurrency = async (
  functionName: string,
): Promise<LambdaProvisionedConcurrency[]> => {
  const response = await restJson<{
    ProvisionedConcurrencyConfigs?: {
      FunctionArn?: string;
      RequestedProvisionedConcurrentExecutions?: number;
      AvailableProvisionedConcurrentExecutions?: number;
      AllocatedProvisionedConcurrentExecutions?: number;
      Status?: string;
      LastModified?: string;
    }[];
  }>("lambda", "GET", `/2019-09-30/functions/${encodeURIComponent(functionName)}/provisioned-concurrency?List=ALL`);
  return (response.ProvisionedConcurrencyConfigs ?? []).map((config) => {
    const functionArn = config.FunctionArn ?? "";
    return {
      functionArn,
      qualifier: functionArn.slice(functionArn.lastIndexOf(":") + 1),
      requested: config.RequestedProvisionedConcurrentExecutions ?? 0,
      available: config.AvailableProvisionedConcurrentExecutions ?? 0,
      allocated: config.AllocatedProvisionedConcurrentExecutions ?? 0,
      status: config.Status ?? "",
      lastModified: config.LastModified ?? "",
    };
  });
};

export const putLambdaProvisionedConcurrency = async (
  functionName: string,
  qualifier: string,
  executions: number,
): Promise<void> => {
  await restJson(
    "lambda",
    "PUT",
    `/2019-09-30/functions/${encodeURIComponent(functionName)}/provisioned-concurrency?Qualifier=${encodeURIComponent(qualifier)}`,
    { ProvisionedConcurrentExecutions: executions },
  );
};

export const deleteLambdaProvisionedConcurrency = async (
  functionName: string,
  qualifier: string,
): Promise<void> => {
  await restJson(
    "lambda",
    "DELETE",
    `/2019-09-30/functions/${encodeURIComponent(functionName)}/provisioned-concurrency?Qualifier=${encodeURIComponent(qualifier)}`,
  );
};

interface LambdaConcurrencyWire {
  ReservedConcurrentExecutions?: number;
}

export interface LambdaFunctionUrl {
  functionUrl: string;
  functionArn: string;
  authType: string;
  creationTime: string;
  lastModifiedTime: string;
  invokeMode: string;
}

export const fetchLambdaFunctionUrls = async (functionName: string): Promise<LambdaFunctionUrl[]> => {
  const response = await restJson<{
    FunctionUrlConfigs?: {
      FunctionUrl?: string;
      FunctionArn?: string;
      AuthType?: string;
      CreationTime?: string;
      LastModifiedTime?: string;
      InvokeMode?: string;
    }[];
  }>("lambda", "GET", `/2021-10-31/functions/${encodeURIComponent(functionName)}/urls`);
  return (response.FunctionUrlConfigs ?? []).map((config) => ({
    functionUrl: config.FunctionUrl ?? "",
    functionArn: config.FunctionArn ?? "",
    authType: config.AuthType ?? "",
    creationTime: config.CreationTime ?? "",
    lastModifiedTime: config.LastModifiedTime ?? "",
    invokeMode: config.InvokeMode ?? "",
  }));
};

export const createLambdaFunctionUrl = async (
  functionName: string,
  authType: "AWS_IAM" | "NONE",
  invokeMode: "BUFFERED" | "RESPONSE_STREAM",
): Promise<void> => {
  await restJson("lambda", "POST", `/2021-10-31/functions/${encodeURIComponent(functionName)}/url`, {
    AuthType: authType,
    InvokeMode: invokeMode,
  });
};

export const updateLambdaFunctionUrl = async (
  functionName: string,
  authType: "AWS_IAM" | "NONE",
  invokeMode: "BUFFERED" | "RESPONSE_STREAM",
): Promise<void> => {
  await restJson("lambda", "PUT", `/2021-10-31/functions/${encodeURIComponent(functionName)}/url`, {
    AuthType: authType,
    InvokeMode: invokeMode,
  });
};

export const deleteLambdaFunctionUrl = async (functionName: string): Promise<void> => {
  await restJson("lambda", "DELETE", `/2021-10-31/functions/${encodeURIComponent(functionName)}/url`);
};

export interface LambdaEventInvokeConfig {
  functionArn: string;
  qualifier: string;
  maximumRetryAttempts?: number;
  maximumEventAgeInSeconds?: number;
  onSuccessDestination?: string;
  onFailureDestination?: string;
  lastModified: number;
}

export const fetchLambdaEventInvokeConfigs = async (functionName: string): Promise<LambdaEventInvokeConfig[]> => {
  const response = await restJson<{
    FunctionEventInvokeConfigs?: {
      FunctionArn?: string;
      MaximumRetryAttempts?: number;
      MaximumEventAgeInSeconds?: number;
      LastModified?: number;
      DestinationConfig?: {
        OnSuccess?: { Destination?: string };
        OnFailure?: { Destination?: string };
      };
    }[];
  }>("lambda", "GET", `/2019-09-25/functions/${encodeURIComponent(functionName)}/event-invoke-config/list`);
  return (response.FunctionEventInvokeConfigs ?? []).map((config) => {
    const functionArn = config.FunctionArn ?? "";
    const suffix = functionArn.slice(functionArn.lastIndexOf(":") + 1);
    return {
      functionArn,
      qualifier: suffix === functionName ? "$LATEST" : suffix,
      maximumRetryAttempts: config.MaximumRetryAttempts,
      maximumEventAgeInSeconds: config.MaximumEventAgeInSeconds,
      onSuccessDestination: config.DestinationConfig?.OnSuccess?.Destination,
      onFailureDestination: config.DestinationConfig?.OnFailure?.Destination,
      lastModified: config.LastModified ?? 0,
    };
  });
};

export const putLambdaEventInvokeConfig = async (
  functionName: string,
  qualifier: string,
  config: {
    maximumRetryAttempts: number;
    maximumEventAgeInSeconds: number;
    onSuccessDestination?: string;
    onFailureDestination?: string;
  },
): Promise<void> => {
  const destinationConfig: Record<string, { Destination: string }> = {};
  if (config.onSuccessDestination) destinationConfig.OnSuccess = { Destination: config.onSuccessDestination };
  if (config.onFailureDestination) destinationConfig.OnFailure = { Destination: config.onFailureDestination };
  const query = qualifier === "$LATEST" ? "" : `?Qualifier=${encodeURIComponent(qualifier)}`;
  await restJson("lambda", "PUT", `/2019-09-25/functions/${encodeURIComponent(functionName)}/event-invoke-config${query}`, {
    MaximumRetryAttempts: config.maximumRetryAttempts,
    MaximumEventAgeInSeconds: config.maximumEventAgeInSeconds,
    DestinationConfig: destinationConfig,
  });
};

export const deleteLambdaEventInvokeConfig = async (
  functionName: string,
  qualifier: string,
): Promise<void> => {
  const query = qualifier === "$LATEST" ? "" : `?Qualifier=${encodeURIComponent(qualifier)}`;
  await restJson(
    "lambda",
    "DELETE",
    `/2019-09-25/functions/${encodeURIComponent(functionName)}/event-invoke-config${query}`,
  );
};

// Lambda resource tagging — TagResource (POST /2017-03-31/tags/{arn}) sets or
// updates the given tags; UntagResource (DELETE …?tagKeys=…) removes keys. The
// current tags are read from GetFunction (fetchLambdaFunctionDetail), so the
// console diffs the operator's edits against them to make exactly these two
// calls, the way the real console's Tags editor does.
export const tagLambdaResource = async (arn: string, tags: Record<string, string>): Promise<void> => {
  await restJson("lambda", "POST", `/2017-03-31/tags/${encodeURIComponent(arn)}`, { Tags: tags });
};

export const untagLambdaResource = async (arn: string, tagKeys: string[]): Promise<void> => {
  const query = tagKeys.map((key) => `tagKeys=${encodeURIComponent(key)}`).join("&");
  await restJson("lambda", "DELETE", `/2017-03-31/tags/${encodeURIComponent(arn)}?${query}`);
};

export interface ECRRepo {
  name: string;
  arn: string;
  uri: string;
  createdAt: number;
  imageTagMutability: string;
  scanOnPush: boolean;
}

interface ECRRepository {
  repositoryName?: string;
  repositoryArn?: string;
  repositoryUri?: string;
  createdAt?: number;
  imageTagMutability?: string;
  imageScanningConfiguration?: { scanOnPush?: boolean };
}

const ecrRepoFromWire = (repo: ECRRepository, fallbackName: string): ECRRepo => ({
  name: repo.repositoryName ?? fallbackName,
  arn: repo.repositoryArn ?? "",
  uri: repo.repositoryUri ?? "",
  createdAt: repo.createdAt ?? 0,
  imageTagMutability: repo.imageTagMutability ?? "MUTABLE",
  scanOnPush: repo.imageScanningConfiguration?.scanOnPush ?? false,
});

export const fetchECRRepos = async (): Promise<ECRRepo[]> => {
  const described = await awsJson<{ repositories?: ECRRepository[] }>(
    "ecr",
    "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
    {},
  );
  return (described.repositories ?? []).map((repo) => ecrRepoFromWire(repo, ""));
};

// CreateRepository — the real console's "Create repository" dialog collects
// just the name (image tag mutability, scanning, and encryption all keep
// ECR's own defaults, the same as a bare `aws ecr create-repository`).
export const createECRRepository = async (repositoryName: string): Promise<ECRRepo> => {
  const created = await awsJson<{ repository?: ECRRepository }>(
    "ecr",
    "AmazonEC2ContainerRegistry_V20150921.CreateRepository",
    { repositoryName },
  );
  const repo = created.repository;
  if (!repo) throw new Error("CreateRepository returned no repository");
  return ecrRepoFromWire(repo, repositoryName);
};

// DeleteRepository with force: true — the real console's delete dialog warns
// that a non-empty repository's images are deleted along with it and asks the
// operator to confirm by typing the repository name, then sends force so the
// non-empty case succeeds rather than answering RepositoryNotEmptyException.
export const deleteECRRepo = async (repositoryName: string): Promise<void> => {
  await awsJson("ecr", "AmazonEC2ContainerRegistry_V20150921.DeleteRepository", {
    repositoryName,
    force: true,
  });
};

// ECR repository detail — DescribeRepositories filtered to one name (there is
// no singular DescribeRepository operation), and DescribeImages for the image
// list, the same two calls the real console's repository page reads.
export const fetchECRRepo = async (repositoryName: string): Promise<ECRRepo> => {
  const described = await awsJson<{ repositories?: ECRRepository[] }>(
    "ecr",
    "AmazonEC2ContainerRegistry_V20150921.DescribeRepositories",
    { repositoryNames: [repositoryName] },
  );
  const repo = described.repositories?.[0];
  if (!repo) throw new Error(`DescribeRepositories returned no repository for ${repositoryName}`);
  return ecrRepoFromWire(repo, repositoryName);
};

// PutImageTagMutability and PutImageScanningConfiguration — the two real ECR
// operations the console's "Edit" flow drives for a repository's registry
// settings, matching what the real console's repository settings page changes.
export const putECRImageTagMutability = async (
  repositoryName: string,
  imageTagMutability: "MUTABLE" | "IMMUTABLE",
): Promise<void> => {
  await awsJson("ecr", "AmazonEC2ContainerRegistry_V20150921.PutImageTagMutability", {
    repositoryName,
    imageTagMutability,
  });
};

export const putECRImageScanningConfiguration = async (
  repositoryName: string,
  scanOnPush: boolean,
): Promise<void> => {
  await awsJson("ecr", "AmazonEC2ContainerRegistry_V20150921.PutImageScanningConfiguration", {
    repositoryName,
    imageScanningConfiguration: { scanOnPush },
  });
};

// ECR resource tagging — ListTagsForResource reads the current tags,
// TagResource sets/updates them, and UntagResource removes keys, all keyed by
// the repository ARN, the same three operations the real console's Tags tab
// drives.
export const fetchECRTags = async (resourceArn: string): Promise<Record<string, string>> => {
  const listed = await awsJson<{ tags?: { Key?: string; Value?: string }[] }>(
    "ecr",
    "AmazonEC2ContainerRegistry_V20150921.ListTagsForResource",
    { resourceArn },
  );
  return Object.fromEntries((listed.tags ?? []).map((tag) => [tag.Key ?? "", tag.Value ?? ""]));
};

export const tagECRResource = async (resourceArn: string, tags: Record<string, string>): Promise<void> => {
  await awsJson("ecr", "AmazonEC2ContainerRegistry_V20150921.TagResource", {
    resourceArn,
    tags: Object.entries(tags).map(([Key, Value]) => ({ Key, Value })),
  });
};

export const untagECRResource = async (resourceArn: string, tagKeys: string[]): Promise<void> => {
  await awsJson("ecr", "AmazonEC2ContainerRegistry_V20150921.UntagResource", { resourceArn, tagKeys });
};

export interface ECRImage {
  digest: string;
  tags: string[];
  sizeBytes: number;
  pushedAt: number;
}

interface ECRImageDetailEntry {
  imageDigest?: string;
  imageTags?: string[];
  imageSizeInBytes?: number;
  imagePushedAt?: number;
}

export const fetchECRImages = async (repositoryName: string): Promise<ECRImage[]> => {
  const described = await awsJson<{ imageDetails?: ECRImageDetailEntry[] }>(
    "ecr",
    "AmazonEC2ContainerRegistry_V20150921.DescribeImages",
    { repositoryName },
  );
  return (described.imageDetails ?? []).map((image) => ({
    digest: image.imageDigest ?? "",
    tags: image.imageTags ?? [],
    sizeBytes: image.imageSizeInBytes ?? 0,
    pushedAt: image.imagePushedAt ?? 0,
  }));
};

export interface S3Bucket {
  name: string;
  creationDate: string;
}

export const fetchS3Buckets = async (): Promise<S3Bucket[]> => {
  const xml = await awsRestXml("s3", "/");
  return Array.from(xml.getElementsByTagName("Bucket")).map((bucket) => ({
    name: bucket.getElementsByTagName("Name")[0]?.textContent ?? "",
    creationDate: bucket.getElementsByTagName("CreationDate")[0]?.textContent ?? "",
  }));
};

// CreateBucket — PUT /{bucket} with an empty body, the same request the real
// console's "Create bucket" issues for a bucket in this console's own Region
// (us-east-1 is the one Region CreateBucket rejects a LocationConstraint
// for, so the console sends none rather than special-casing it).
export const createS3Bucket = async (bucketName: string): Promise<void> => {
  await awsRestXmlPut("s3", `/${encodeURIComponent(bucketName)}`);
};

// DeleteBucket only succeeds on an empty bucket — the same constraint the
// real console enforces, surfacing S3's BucketNotEmpty error rather than
// emptying the bucket on the operator's behalf.
export const deleteS3Bucket = async (bucketName: string): Promise<void> => {
  await awsRestXmlDelete("s3", `/${encodeURIComponent(bucketName)}`);
};

// S3 bucket detail — GetBucketLocation for the Region, and ListObjectsV2
// (`?list-type=2`) for the object listing, the same sub-resource GET
// requests the real console's bucket page issues. S3 has no per-bucket
// "properties" read for the creation date; the real console reads it from
// the same ListBuckets response the buckets page already fetches, so the
// detail page's caller does the same rather than inventing a value.
export const fetchS3BucketLocation = async (bucketName: string): Promise<string> => {
  const xml = await awsRestXml("s3", `/${encodeURIComponent(bucketName)}?location`);
  const constraint = xml.documentElement?.textContent?.trim();
  // An empty LocationConstraint means us-east-1 — the one real S3 Region that
  // reports as empty rather than naming itself.
  return constraint || "us-east-1";
};

export interface S3Object {
  key: string;
  size: number;
  lastModified: string;
  etag: string;
}

export const fetchS3Objects = async (bucketName: string): Promise<S3Object[]> => {
  const xml = await awsRestXml("s3", `/${encodeURIComponent(bucketName)}?list-type=2`);
  return Array.from(xml.getElementsByTagName("Contents")).map((entry) => ({
    key: entry.getElementsByTagName("Key")[0]?.textContent ?? "",
    size: Number(entry.getElementsByTagName("Size")[0]?.textContent ?? "0"),
    lastModified: entry.getElementsByTagName("LastModified")[0]?.textContent ?? "",
    etag: (entry.getElementsByTagName("ETag")[0]?.textContent ?? "").replace(/^"|"$/g, ""),
  }));
};

const S3_XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/";

// GetBucketVersioning — GET /{bucket}?versioning. An unversioned bucket answers
// an empty `<VersioningConfiguration/>` (no Status element), the same shape the
// real console reads before it shows the versioning toggle as off.
export type S3VersioningStatus = "Enabled" | "Suspended" | "Disabled";

export const fetchS3BucketVersioning = async (bucketName: string): Promise<S3VersioningStatus> => {
  const xml = await awsRestXml("s3", `/${encodeURIComponent(bucketName)}?versioning`);
  const status = xml.getElementsByTagName("Status")[0]?.textContent?.trim();
  return status === "Enabled" || status === "Suspended" ? status : "Disabled";
};

// PutBucketVersioning — PUT /{bucket}?versioning. Real S3 never removes
// versioning once enabled: it is Enabled or Suspended, so the console's toggle
// sends exactly those two, matching the real console.
export const putS3BucketVersioning = async (
  bucketName: string,
  status: "Enabled" | "Suspended",
): Promise<void> => {
  const body = `<VersioningConfiguration xmlns="${S3_XMLNS}"><Status>${status}</Status></VersioningConfiguration>`;
  await awsRestXmlPut("s3", `/${encodeURIComponent(bucketName)}?versioning`, body);
};

// GetBucketTagging — GET /{bucket}?tagging. A bucket with no tag set answers
// S3's NoSuchTagSet error rather than an empty document; the real console reads
// that as "no tags", so the console does the same rather than surfacing it as a
// load failure.
export const fetchS3BucketTagging = async (bucketName: string): Promise<Record<string, string>> => {
  const response = await awsFetch({ service: "s3", method: "GET", path: `/${encodeURIComponent(bucketName)}?tagging` });
  const text = await response.text();
  const xml = new DOMParser().parseFromString(text, "text/xml");
  if (!response.ok) {
    const code = xml.getElementsByTagName("Code")[0]?.textContent ?? "";
    if (code === "NoSuchTagSet") return {};
    const message = xml.getElementsByTagName("Message")[0]?.textContent ?? "";
    throw new Error(code ? `${code}: ${message}` : `GET /${bucketName}?tagging returned HTTP ${response.status}`);
  }
  const tags: Record<string, string> = {};
  for (const tag of Array.from(xml.getElementsByTagName("Tag"))) {
    const key = tag.getElementsByTagName("Key")[0]?.textContent ?? "";
    if (key) tags[key] = tag.getElementsByTagName("Value")[0]?.textContent ?? "";
  }
  return tags;
};

// PutBucketTagging replaces the whole tag set (S3 tagging has no per-key add);
// clearing every tag is DeleteBucketTagging, the same two operations the real
// console's bucket Tags editor drives.
export const putS3BucketTagging = async (bucketName: string, tags: Record<string, string>): Promise<void> => {
  const entries = Object.entries(tags);
  if (entries.length === 0) {
    await awsRestXmlDelete("s3", `/${encodeURIComponent(bucketName)}?tagging`);
    return;
  }
  const tagXml = entries
    .map(([key, value]) => `<Tag><Key>${escapeXml(key)}</Key><Value>${escapeXml(value)}</Value></Tag>`)
    .join("");
  const body = `<Tagging xmlns="${S3_XMLNS}"><TagSet>${tagXml}</TagSet></Tagging>`;
  await awsRestXmlPut("s3", `/${encodeURIComponent(bucketName)}?tagging`, body);
};

function escapeXml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&apos;");
}

export interface CWLogGroup {
  name: string;
  creationTime: number;
  retentionInDays: number;
  storedBytes: number;
}

interface CWLogGroupEntry {
  logGroupName?: string;
  creationTime?: number;
  retentionInDays?: number;
  storedBytes?: number;
}

export type CWStorageTier = "STANDARD" | "INTELLIGENT_TIERING";

export interface CWStorageTierPolicy {
  storageTier: CWStorageTier;
  lastUpdatedTime: number;
}

export interface CWSyslogConfiguration {
  logGroupArn: string;
  sourceType: string;
  vpcEndpointId: string;
  createdAt: number;
}

// AWS Identity and Access Management (IAM) — the query protocol (Action +
// Version=2010-05-08, form-encoded, XML responses), the same wire the aws CLI
// signs. The console drives it with the operator's federated credentials, so
// minting a user and an access key here is the same call an administrator's
// CLI would make.

const IAM_VERSION = "2010-05-08";

const elementText = (parent: Element | Document, tag: string): string =>
  parent.getElementsByTagName(tag)[0]?.textContent ?? "";

export interface IAMUserSummary {
  userName: string;
  userId: string;
  arn: string;
  path: string;
  createDate: string;
}

const iamUserFromElement = (element: Element): IAMUserSummary => ({
  userName: elementText(element, "UserName"),
  userId: elementText(element, "UserId"),
  arn: elementText(element, "Arn"),
  path: elementText(element, "Path"),
  createDate: elementText(element, "CreateDate"),
});

export const fetchIAMUsers = async (): Promise<IAMUserSummary[]> => {
  const xml = await awsQuery("iam", IAM_VERSION, "ListUsers");
  return Array.from(xml.getElementsByTagName("member")).map(iamUserFromElement);
};

export const fetchIAMUser = async (userName: string): Promise<IAMUserSummary> => {
  const xml = await awsQuery("iam", IAM_VERSION, "GetUser", { UserName: userName });
  const user = xml.getElementsByTagName("User")[0];
  if (!user) throw new Error(`GetUser returned no User element for ${userName}`);
  return iamUserFromElement(user);
};

export const createIAMUser = async (userName: string): Promise<void> => {
  await awsQuery("iam", IAM_VERSION, "CreateUser", { UserName: userName });
};

export const deleteIAMUser = async (userName: string): Promise<void> => {
  await awsQuery("iam", IAM_VERSION, "DeleteUser", { UserName: userName });
};

export interface IAMAccessKeyMetadata {
  accessKeyId: string;
  status: string;
  createDate: string;
}

// ListAccessKeys returns metadata only — never the secret. The secret exists
// exactly once, in the CreateAccessKey response.
export const fetchIAMAccessKeys = async (userName: string): Promise<IAMAccessKeyMetadata[]> => {
  const xml = await awsQuery("iam", IAM_VERSION, "ListAccessKeys", { UserName: userName });
  return Array.from(xml.getElementsByTagName("member")).map((member) => ({
    accessKeyId: elementText(member, "AccessKeyId"),
    status: elementText(member, "Status"),
    createDate: elementText(member, "CreateDate"),
  }));
};

export interface IAMCreatedAccessKey {
  accessKeyId: string;
  secretAccessKey: string;
}

export const createIAMAccessKey = async (userName: string): Promise<IAMCreatedAccessKey> => {
  const xml = await awsQuery("iam", IAM_VERSION, "CreateAccessKey", { UserName: userName });
  const created = {
    accessKeyId: elementText(xml, "AccessKeyId"),
    secretAccessKey: elementText(xml, "SecretAccessKey"),
  };
  if (!created.accessKeyId || !created.secretAccessKey) {
    throw new Error("CreateAccessKey returned no credential material");
  }
  return created;
};

export const deleteIAMAccessKey = async (userName: string, accessKeyId: string): Promise<void> => {
  await awsQuery("iam", IAM_VERSION, "DeleteAccessKey", { UserName: userName, AccessKeyId: accessKeyId });
};

export const updateIAMAccessKeyStatus = async (
  userName: string,
  accessKeyId: string,
  status: "Active" | "Inactive",
): Promise<void> => {
  await awsQuery("iam", IAM_VERSION, "UpdateAccessKey", {
    UserName: userName,
    AccessKeyId: accessKeyId,
    Status: status,
  });
};

// AWS Organizations — awsjson1.1, X-Amz-Target AWSOrganizationsV20161128.<Op>,
// the wire the aws CLI and SDKs sign for `aws organizations …`. The console's
// AWS accounts page is a plain Organizations client: ListAccounts for the
// table, the async CreateAccount → DescribeCreateAccountStatus request flow,
// and RemoveAccountFromOrganization / CloseAccount for the account actions the
// real console offers.

const ORG_TARGET_PREFIX = "AWSOrganizationsV20161128.";

const orgJson = <T>(operation: string, input: Record<string, unknown> = {}): Promise<T> =>
  awsJson<T>("organizations", `${ORG_TARGET_PREFIX}${operation}`, input);

export interface OrgAccount {
  id: string;
  arn: string;
  name: string;
  email: string;
  status: string;
  joinedMethod: string;
  joinedTimestamp: number;
}

interface OrgAccountEntry {
  Id?: string;
  Arn?: string;
  Name?: string;
  Email?: string;
  Status?: string;
  JoinedMethod?: string;
  JoinedTimestamp?: number;
}

const orgAccountFromEntry = (entry: OrgAccountEntry): OrgAccount => ({
  id: entry.Id ?? "",
  arn: entry.Arn ?? "",
  name: entry.Name ?? "",
  email: entry.Email ?? "",
  status: entry.Status ?? "",
  joinedMethod: entry.JoinedMethod ?? "",
  joinedTimestamp: entry.JoinedTimestamp ?? 0,
});

export const fetchOrgAccounts = async (): Promise<OrgAccount[]> => {
  const listed = await orgJson<{ Accounts?: OrgAccountEntry[] }>("ListAccounts");
  return (listed.Accounts ?? []).map(orgAccountFromEntry);
};

export const fetchOrgAccount = async (accountId: string): Promise<OrgAccount> => {
  const described = await orgJson<{ Account?: OrgAccountEntry }>("DescribeAccount", { AccountId: accountId });
  if (!described.Account) throw new Error(`DescribeAccount returned no Account for ${accountId}`);
  return orgAccountFromEntry(described.Account);
};

export type OrgCreateAccountState = "IN_PROGRESS" | "SUCCEEDED" | "FAILED";

export interface OrgCreateAccountStatus {
  id: string;
  accountName: string;
  state: OrgCreateAccountState;
  requestedTimestamp: number;
  completedTimestamp: number;
  accountId: string;
  failureReason: string;
}

interface OrgCreateAccountStatusEntry {
  Id?: string;
  AccountName?: string;
  State?: string;
  RequestedTimestamp?: number;
  CompletedTimestamp?: number;
  AccountId?: string;
  FailureReason?: string;
}

const orgCreateStatusFromEntry = (entry: OrgCreateAccountStatusEntry): OrgCreateAccountStatus => ({
  id: entry.Id ?? "",
  accountName: entry.AccountName ?? "",
  state: (entry.State ?? "IN_PROGRESS") as OrgCreateAccountState,
  requestedTimestamp: entry.RequestedTimestamp ?? 0,
  completedTimestamp: entry.CompletedTimestamp ?? 0,
  accountId: entry.AccountId ?? "",
  failureReason: entry.FailureReason ?? "",
});

export const createOrgAccount = async (accountName: string, email: string): Promise<OrgCreateAccountStatus> => {
  const created = await orgJson<{ CreateAccountStatus?: OrgCreateAccountStatusEntry }>("CreateAccount", {
    AccountName: accountName,
    Email: email,
  });
  if (!created.CreateAccountStatus) throw new Error("CreateAccount returned no CreateAccountStatus");
  return orgCreateStatusFromEntry(created.CreateAccountStatus);
};

export const fetchOrgCreateAccountStatus = async (requestId: string): Promise<OrgCreateAccountStatus> => {
  const described = await orgJson<{ CreateAccountStatus?: OrgCreateAccountStatusEntry }>(
    "DescribeCreateAccountStatus",
    { CreateAccountRequestId: requestId },
  );
  if (!described.CreateAccountStatus) {
    throw new Error(`DescribeCreateAccountStatus returned no CreateAccountStatus for ${requestId}`);
  }
  return orgCreateStatusFromEntry(described.CreateAccountStatus);
};

export const fetchOrgCreateAccountStatuses = async (): Promise<OrgCreateAccountStatus[]> => {
  const listed = await orgJson<{ CreateAccountStatuses?: OrgCreateAccountStatusEntry[] }>("ListCreateAccountStatus");
  return (listed.CreateAccountStatuses ?? []).map(orgCreateStatusFromEntry);
};

export const createOrganization = async (): Promise<void> => {
  await orgJson("CreateOrganization", { FeatureSet: "ALL" });
};

export const removeOrgAccount = async (accountId: string): Promise<void> => {
  await orgJson("RemoveAccountFromOrganization", { AccountId: accountId });
};

export const closeOrgAccount = async (accountId: string): Promise<void> => {
  await orgJson("CloseAccount", { AccountId: accountId });
};

export const fetchCWLogGroups = async (): Promise<CWLogGroup[]> => {
  const described = await awsJson<{ logGroups?: CWLogGroupEntry[] }>("logs", "Logs_20140328.DescribeLogGroups", {});
  return (described.logGroups ?? []).map((group) => ({
    name: group.logGroupName ?? "",
    creationTime: group.creationTime ?? 0,
    retentionInDays: group.retentionInDays ?? 0,
    storedBytes: group.storedBytes ?? 0,
  }));
};

// CreateLogGroup — the real console's "Create log group" dialog collects
// just the name; retention and KMS key both keep CloudWatch Logs' own
// defaults (never expire, service-managed encryption).
export const createCWLogGroup = async (logGroupName: string): Promise<void> => {
  await awsJson("logs", "Logs_20140328.CreateLogGroup", { logGroupName });
};

export const deleteCWLogGroup = async (logGroupName: string): Promise<void> => {
  await awsJson("logs", "Logs_20140328.DeleteLogGroup", { logGroupName });
};

export const fetchCWStorageTierPolicy = async (): Promise<CWStorageTierPolicy> =>
  awsJson<CWStorageTierPolicy>("logs", "Logs_20140328.GetStorageTierPolicy", {});

export const putCWStorageTierPolicy = async (storageTier: CWStorageTier): Promise<CWStorageTierPolicy> =>
  awsJson<CWStorageTierPolicy>("logs", "Logs_20140328.PutStorageTierPolicy", { storageTier });

export const fetchCWSyslogConfigurations = async (
  logGroupIdentifier?: string,
): Promise<CWSyslogConfiguration[]> => {
  const listed = await awsJson<{ syslogConfigurations?: CWSyslogConfiguration[] }>(
    "logs",
    "Logs_20140328.ListSyslogConfigurations",
    logGroupIdentifier ? { logGroupIdentifier } : {},
  );
  return listed.syslogConfigurations ?? [];
};

export const putCWSyslogConfiguration = async (
  logGroupIdentifier: string,
  vpcEndpointId: string,
): Promise<void> => {
  await awsJson("logs", "Logs_20140328.PutSyslogConfiguration", {
    logGroupIdentifier,
    vpcEndpointId,
  });
};

export const deleteCWSyslogConfiguration = async (
  logGroupIdentifier: string,
  vpcEndpointId: string,
): Promise<void> => {
  await awsJson("logs", "Logs_20140328.DeleteSyslogConfiguration", {
    logGroupIdentifier,
    vpcEndpointId,
  });
};

// Retention — PutRetentionPolicy sets a fixed retention in days;
// DeleteRetentionPolicy clears it so events never expire. CloudWatch has no
// "0 days" retention, so "Never expire" is the delete, matching what the real
// console's retention editor does.
export const putCWLogGroupRetention = async (
  logGroupName: string,
  retentionInDays: number,
): Promise<void> => {
  if (retentionInDays <= 0) {
    await awsJson("logs", "Logs_20140328.DeleteRetentionPolicy", { logGroupName });
    return;
  }
  await awsJson("logs", "Logs_20140328.PutRetentionPolicy", { logGroupName, retentionInDays });
};

// CloudWatch Logs log group detail — DescribeLogGroups has no singular
// "GetLogGroup" counterpart, so the detail page reads it the way the real
// console does: `logGroupNamePrefix` narrowed to an exact match.
export const fetchCWLogGroup = async (logGroupName: string): Promise<CWLogGroup> => {
  const described = await awsJson<{ logGroups?: CWLogGroupEntry[] }>("logs", "Logs_20140328.DescribeLogGroups", {
    logGroupNamePrefix: logGroupName,
  });
  const group = (described.logGroups ?? []).find((entry) => entry.logGroupName === logGroupName);
  if (!group) throw new Error(`DescribeLogGroups returned no log group named ${logGroupName}`);
  return {
    name: group.logGroupName ?? logGroupName,
    creationTime: group.creationTime ?? 0,
    retentionInDays: group.retentionInDays ?? 0,
    storedBytes: group.storedBytes ?? 0,
  };
};

export interface CWLogStream {
  name: string;
  creationTime: number;
  firstEventTimestamp: number;
  lastEventTimestamp: number;
  lastIngestionTime: number;
}

interface CWLogStreamEntry {
  logStreamName?: string;
  creationTime?: number;
  firstEventTimestamp?: number;
  lastEventTimestamp?: number;
  lastIngestionTime?: number;
}

export const fetchCWLogStreams = async (logGroupName: string): Promise<CWLogStream[]> => {
  const described = await awsJson<{ logStreams?: CWLogStreamEntry[] }>("logs", "Logs_20140328.DescribeLogStreams", {
    logGroupName,
    orderBy: "LastEventTime",
    descending: true,
  });
  return (described.logStreams ?? []).map((stream) => ({
    name: stream.logStreamName ?? "",
    creationTime: stream.creationTime ?? 0,
    firstEventTimestamp: stream.firstEventTimestamp ?? 0,
    lastEventTimestamp: stream.lastEventTimestamp ?? 0,
    lastIngestionTime: stream.lastIngestionTime ?? 0,
  }));
};

export interface CWLogEvent {
  timestamp: number;
  message: string;
}

// GetLogEvents with startFromHead: false (the documented default) returns the
// tail of the stream — the most recent events, the same window the real
// console's log-stream viewer opens on.
export const fetchCWLogEvents = async (logGroupName: string, logStreamName: string): Promise<CWLogEvent[]> => {
  const described = await awsJson<{ events?: { timestamp?: number; message?: string }[] }>(
    "logs",
    "Logs_20140328.GetLogEvents",
    { logGroupName, logStreamName, limit: 100, startFromHead: false },
  );
  return (described.events ?? []).map((event) => ({ timestamp: event.timestamp ?? 0, message: event.message ?? "" }));
};

// ---------------------------------------------------------------------------
// The rest of the AWS services this simulator implements.
//
// Every reader below drives the same real AWS API its service's own console
// page drives — the operation names, wire protocols, request shapes and
// response shapes are AWS's, reached through the same federated, SigV4-signed
// `awsFetch` the readers above use. Nothing here is a simulator-specific
// endpoint or a sockerless-invented summary.
// ---------------------------------------------------------------------------

// awsJson10 is the awsjson1.0 variant of `awsJson` — the protocol Amazon
// DynamoDB, Amazon SQS, and AWS Step Functions speak. The wire differs from
// awsjson1.1 only in the Content-Type the client declares; the target header,
// the JSON body, and the `{"__type", "message"}` error shape are identical.
async function awsJson10<T>(service: string, target: string, input: Record<string, unknown> = {}): Promise<T> {
  const response = await awsFetch({
    service,
    method: "POST",
    path: "/",
    headers: { "content-type": "application/x-amz-json-1.0", "x-amz-target": target },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    const text = await response.text();
    let type = "";
    let message = "";
    try {
      const parsed = JSON.parse(text) as { __type?: string; message?: string; Message?: string };
      type = parsed.__type ?? "";
      message = parsed.message ?? parsed.Message ?? "";
    } catch {
      // Not the protocol's JSON error shape — the HTTP status is all there is.
    }
    const operation = target.slice(target.lastIndexOf(".") + 1);
    const code = type.slice(type.lastIndexOf("#") + 1);
    throw new AwsApiError(
      code ? `${operation}: ${code}: ${message}` : `${operation} returned HTTP ${response.status}`,
      code,
    );
  }
  return (await response.json()) as T;
}

/** The direct element children of the first element named `container`, which is
 * how every AWS Query- and REST-XML-protocol list is shaped: a wrapper element
 * (`<Topics>`, `<vpcSet>`, `<DBInstances>`) holding one element per entry. */
function xmlList(root: Document | Element, container: string): Element[] {
  const found = root.getElementsByTagName(container)[0];
  return found ? Array.from(found.children) : [];
}

/**
 * The text of a *direct child* element, which is the only safe way to read a
 * field off an AWS XML entry. A descendant search finds the first element with
 * that name anywhere beneath it, and AWS's own shapes nest the same names at
 * several depths — a CloudFront DistributionSummary carries its own `Enabled`
 * flag *after* a `DefaultCacheBehavior` whose `TrustedSigners` has an `Enabled`
 * of its own, so a descendant search reads the wrong one.
 */
function childText(parent: Element, tag: string): string {
  for (const child of Array.from(parent.children)) {
    if (child.tagName === tag) return child.textContent ?? "";
  }
  return "";
}

/** The first direct child element named `tag`, for a nested structure a caller
 * then reads fields out of (`<Endpoint><Address>…`). */
function childElement(parent: Element, tag: string): Element | undefined {
  for (const child of Array.from(parent.children)) {
    if (child.tagName === tag) return child;
  }
  return undefined;
}

/** Query-protocol form parameters for a list argument, which AWS flattens as
 * `<Name>.1`, `<Name>.2`, … (`InstanceId.1=i-abc`). */
function xmlIndexedParams(name: string, values: string[]): Record<string, string> {
  return Object.fromEntries(values.map((value, index) => [`${name}.${index + 1}`, value]));
}

/** An AWS resource tag set, as every EC2-family XML response carries it. */
function xmlTags(element: Element): Record<string, string> {
  const tags: Record<string, string> = {};
  for (const item of xmlList(element, "tagSet")) {
    const key = childText(item, "key");
    if (key) tags[key] = childText(item, "value");
  }
  return tags;
}

// ---------------------------------------------------------------------------
// Amazon Elastic Compute Cloud (EC2) — the Query protocol
// (Action + Version=2016-11-15, form-encoded, XML responses).
// ---------------------------------------------------------------------------

const EC2_VERSION = "2016-11-15";

export interface EC2Instance {
  instanceId: string;
  name: string;
  instanceType: string;
  state: string;
  imageId: string;
  privateIpAddress: string;
  publicIpAddress: string;
  launchTime: string;
  availabilityZone: string;
  vpcId: string;
  subnetId: string;
  keyName: string;
  architecture: string;
  securityGroups: { groupId: string; groupName: string }[];
}

function ec2InstanceFromElement(item: Element): EC2Instance {
  const tags = xmlTags(item);
  const instanceState = childElement(item, "instanceState");
  const placement = childElement(item, "placement");
  return {
    instanceId: childText(item, "instanceId"),
    name: tags.Name ?? "",
    instanceType: childText(item, "instanceType"),
    state: instanceState ? childText(instanceState, "name") : "",
    imageId: childText(item, "imageId"),
    privateIpAddress: childText(item, "privateIpAddress"),
    publicIpAddress: childText(item, "ipAddress"),
    launchTime: childText(item, "launchTime"),
    availabilityZone: placement ? childText(placement, "availabilityZone") : "",
    vpcId: childText(item, "vpcId"),
    subnetId: childText(item, "subnetId"),
    keyName: childText(item, "keyName"),
    architecture: childText(item, "architecture"),
    securityGroups: xmlList(item, "groupSet").map((group) => ({
      groupId: childText(group, "groupId"),
      groupName: childText(group, "groupName"),
    })),
  };
}

// DescribeInstances answers reservations, each holding the instances launched
// by one RunInstances call; the real console's Instances table flattens them,
// so this reader does the same.
export const fetchEC2Instances = async (): Promise<EC2Instance[]> => {
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeInstances");
  const instances: EC2Instance[] = [];
  for (const reservation of xmlList(xml, "reservationSet")) {
    for (const item of xmlList(reservation, "instancesSet")) {
      instances.push(ec2InstanceFromElement(item));
    }
  }
  return instances;
};

export const fetchEC2Instance = async (instanceId: string): Promise<EC2Instance> => {
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeInstances", { "InstanceId.1": instanceId });
  for (const reservation of xmlList(xml, "reservationSet")) {
    for (const item of xmlList(reservation, "instancesSet")) {
      return ec2InstanceFromElement(item);
    }
  }
  throw new Error(`DescribeInstances returned no instance for ${instanceId}`);
};

// The three instance lifecycle actions the real console's Instance state menu
// offers. Each is the real EC2 operation, taking the same InstanceId list.
export const startEC2Instances = async (instanceIds: string[]): Promise<void> => {
  await awsQuery("ec2", EC2_VERSION, "StartInstances", xmlIndexedParams("InstanceId", instanceIds));
};

export const stopEC2Instances = async (instanceIds: string[]): Promise<void> => {
  await awsQuery("ec2", EC2_VERSION, "StopInstances", xmlIndexedParams("InstanceId", instanceIds));
};

export const rebootEC2Instances = async (instanceIds: string[]): Promise<void> => {
  await awsQuery("ec2", EC2_VERSION, "RebootInstances", xmlIndexedParams("InstanceId", instanceIds));
};

export const terminateEC2Instances = async (instanceIds: string[]): Promise<void> => {
  await awsQuery("ec2", EC2_VERSION, "TerminateInstances", xmlIndexedParams("InstanceId", instanceIds));
};

export interface EC2Vpc {
  vpcId: string;
  name: string;
  cidrBlock: string;
  state: string;
  isDefault: boolean;
  dhcpOptionsId: string;
  instanceTenancy: string;
}

export const fetchEC2Vpcs = async (): Promise<EC2Vpc[]> => {
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeVpcs");
  return xmlList(xml, "vpcSet").map((item) => ({
    vpcId: childText(item, "vpcId"),
    name: xmlTags(item).Name ?? "",
    cidrBlock: childText(item, "cidrBlock"),
    state: childText(item, "state"),
    isDefault: childText(item, "isDefault") === "true",
    dhcpOptionsId: childText(item, "dhcpOptionsId"),
    instanceTenancy: childText(item, "instanceTenancy"),
  }));
};

export type EC2AccountVpcEncryptionMode = "unmanaged" | "attempt-monitor" | "attempt-enforce";
export type EC2AccountVpcEncryptionExclusion =
  | "InternetGateway"
  | "EgressOnlyInternetGateway"
  | "NatGateway"
  | "VirtualPrivateGateway"
  | "VpcPeering"
  | "Lambda"
  | "VpcLattice"
  | "ElasticFileSystem";

export interface EC2AccountVpcEncryptionControl {
  state: string;
  mode: EC2AccountVpcEncryptionMode;
  managedBy: string;
  lastUpdateTimestamp: string;
  exclusions: Record<EC2AccountVpcEncryptionExclusion, boolean>;
}

const accountVpcEncryptionExclusionFields: Record<EC2AccountVpcEncryptionExclusion, string> = {
  InternetGateway: "internetGateway",
  EgressOnlyInternetGateway: "egressOnlyInternetGateway",
  NatGateway: "natGateway",
  VirtualPrivateGateway: "virtualPrivateGateway",
  VpcPeering: "vpcPeering",
  Lambda: "lambda",
  VpcLattice: "vpcLattice",
  ElasticFileSystem: "elasticFileSystem",
};

function accountVpcEncryptionControlFromXML(xml: Document): EC2AccountVpcEncryptionControl {
  const control = xml.getElementsByTagName("accountVpcEncryptionControl")[0];
  if (!control) throw new Error("Amazon EC2 returned no account VPC encryption control");
  const exclusions = childElement(control, "exclusions");
  return {
    state: childText(control, "state"),
    mode: childText(control, "mode") as EC2AccountVpcEncryptionMode,
    managedBy: childText(control, "managedBy"),
    lastUpdateTimestamp: childText(control, "lastUpdateTimestamp"),
    exclusions: Object.fromEntries(
      Object.entries(accountVpcEncryptionExclusionFields).map(([field, xmlName]) => [
        field,
        exclusions ? childText(exclusions, xmlName) === "enabled" : false,
      ]),
    ) as Record<EC2AccountVpcEncryptionExclusion, boolean>,
  };
}

export const fetchEC2AccountVpcEncryptionControl = async (): Promise<EC2AccountVpcEncryptionControl> =>
  accountVpcEncryptionControlFromXML(await awsQuery("ec2", EC2_VERSION, "DescribeAccountVpcEncryptionControl"));

export const modifyEC2AccountVpcEncryptionControl = async (
  mode: EC2AccountVpcEncryptionMode,
  exclusions: Record<EC2AccountVpcEncryptionExclusion, boolean>,
): Promise<EC2AccountVpcEncryptionControl> => {
  const parameters: Record<string, string> = { Mode: mode };
  for (const field of Object.keys(accountVpcEncryptionExclusionFields) as EC2AccountVpcEncryptionExclusion[]) {
    parameters[field] = exclusions[field] ? "enable" : "disable";
  }
  return accountVpcEncryptionControlFromXML(
    await awsQuery("ec2", EC2_VERSION, "ModifyAccountVpcEncryptionControl", parameters),
  );
};

export interface EC2Subnet {
  subnetId: string;
  name: string;
  vpcId: string;
  cidrBlock: string;
  availabilityZone: string;
  state: string;
  availableIpAddressCount: number;
  mapPublicIpOnLaunch: boolean;
}

// DescribeSubnets narrowed by VPC uses EC2's own `Filter.N` form — the same
// filter the real console's VPC detail page applies.
export const fetchEC2Subnets = async (vpcId?: string): Promise<EC2Subnet[]> => {
  const params: Record<string, string> = vpcId ? { "Filter.1.Name": "vpc-id", "Filter.1.Value.1": vpcId } : {};
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeSubnets", params);
  return xmlList(xml, "subnetSet").map((item) => ({
    subnetId: childText(item, "subnetId"),
    name: xmlTags(item).Name ?? "",
    vpcId: childText(item, "vpcId"),
    cidrBlock: childText(item, "cidrBlock"),
    availabilityZone: childText(item, "availabilityZone"),
    state: childText(item, "state"),
    availableIpAddressCount: Number(childText(item, "availableIpAddressCount") || "0"),
    mapPublicIpOnLaunch: childText(item, "mapPublicIpOnLaunch") === "true",
  }));
};

export interface EC2SecurityGroup {
  groupId: string;
  groupName: string;
  description: string;
  vpcId: string;
}

export const fetchEC2SecurityGroups = async (vpcId?: string): Promise<EC2SecurityGroup[]> => {
  const params: Record<string, string> = vpcId ? { "Filter.1.Name": "vpc-id", "Filter.1.Value.1": vpcId } : {};
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeSecurityGroups", params);
  return xmlList(xml, "securityGroupInfo").map((item) => ({
    groupId: childText(item, "groupId"),
    groupName: childText(item, "groupName"),
    description: childText(item, "groupDescription"),
    vpcId: childText(item, "vpcId"),
  }));
};

export interface EC2Volume {
  volumeId: string;
  size: number;
  state: string;
  volumeType: string;
  availabilityZone: string;
  createTime: string;
  encrypted: boolean;
}

export const fetchEC2Volumes = async (): Promise<EC2Volume[]> => {
  const xml = await awsQuery("ec2", EC2_VERSION, "DescribeVolumes");
  return xmlList(xml, "volumeSet").map((item) => ({
    volumeId: childText(item, "volumeId"),
    size: Number(childText(item, "size") || "0"),
    state: childText(item, "status"),
    volumeType: childText(item, "volumeType"),
    availabilityZone: childText(item, "availabilityZone"),
    createTime: childText(item, "createTime"),
    encrypted: childText(item, "encrypted") === "true",
  }));
};

// ---------------------------------------------------------------------------
// Amazon EC2 Auto Scaling — the Query protocol (Version=2011-01-01).
// ---------------------------------------------------------------------------

export interface AutoScalingGroup {
  name: string;
  arn: string;
  minSize: number;
  maxSize: number;
  desiredCapacity: number;
  healthCheckType: string;
  instanceCount: number;
  availabilityZones: string[];
  createdTime: string;
}

export const fetchAutoScalingGroups = async (): Promise<AutoScalingGroup[]> => {
  const xml = await awsQuery("autoscaling", "2011-01-01", "DescribeAutoScalingGroups");
  return xmlList(xml, "AutoScalingGroups").map((member) => ({
    name: childText(member, "AutoScalingGroupName"),
    arn: childText(member, "AutoScalingGroupARN"),
    minSize: Number(childText(member, "MinSize") || "0"),
    maxSize: Number(childText(member, "MaxSize") || "0"),
    desiredCapacity: Number(childText(member, "DesiredCapacity") || "0"),
    healthCheckType: childText(member, "HealthCheckType"),
    instanceCount: xmlList(member, "Instances").length,
    availabilityZones: xmlList(member, "AvailabilityZones").map((zone) => zone.textContent ?? ""),
    createdTime: childText(member, "CreatedTime"),
  }));
};

// ---------------------------------------------------------------------------
// AWS Batch — the REST-JSON protocol (POST /v1/<operation>).
// ---------------------------------------------------------------------------

export interface BatchJobQueue {
  jobQueueName: string;
  jobQueueArn: string;
  state: string;
  status: string;
  priority: number;
  computeEnvironments: string[];
}

export const fetchBatchJobQueues = async (): Promise<BatchJobQueue[]> => {
  const described = await restJson<{
    jobQueues?: {
      jobQueueName?: string;
      jobQueueArn?: string;
      state?: string;
      status?: string;
      priority?: number;
      computeEnvironmentOrder?: { computeEnvironment?: string }[];
    }[];
  }>("batch", "POST", "/v1/describejobqueues", {});
  return (described.jobQueues ?? []).map((queue) => ({
    jobQueueName: queue.jobQueueName ?? "",
    jobQueueArn: queue.jobQueueArn ?? "",
    state: queue.state ?? "",
    status: queue.status ?? "",
    priority: queue.priority ?? 0,
    computeEnvironments: (queue.computeEnvironmentOrder ?? []).map((entry) => entry.computeEnvironment ?? ""),
  }));
};

export interface BatchComputeEnvironment {
  computeEnvironmentName: string;
  computeEnvironmentArn: string;
  type: string;
  state: string;
  status: string;
  serviceRole: string;
}

export const fetchBatchComputeEnvironments = async (): Promise<BatchComputeEnvironment[]> => {
  const described = await restJson<{
    computeEnvironments?: {
      computeEnvironmentName?: string;
      computeEnvironmentArn?: string;
      type?: string;
      state?: string;
      status?: string;
      serviceRole?: string;
    }[];
  }>("batch", "POST", "/v1/describecomputeenvironments", {});
  return (described.computeEnvironments ?? []).map((env) => ({
    computeEnvironmentName: env.computeEnvironmentName ?? "",
    computeEnvironmentArn: env.computeEnvironmentArn ?? "",
    type: env.type ?? "",
    state: env.state ?? "",
    status: env.status ?? "",
    serviceRole: env.serviceRole ?? "",
  }));
};

export interface BatchJob {
  jobId: string;
  jobArn: string;
  jobName: string;
  jobQueue: string;
  jobDefinition: string;
  status: string;
  statusReason: string;
  createdAt: number;
  startedAt: number;
  stoppedAt: number;
  exitCode?: number;
}

const BATCH_JOB_STATUSES = [
  "SUBMITTED",
  "PENDING",
  "RUNNABLE",
  "STARTING",
  "RUNNING",
  "SUCCEEDED",
  "FAILED",
] as const;

export const fetchBatchJobs = async (): Promise<BatchJob[]> => {
  const queues = await fetchBatchJobQueues();
  const jobIds = new Set<string>();
  for (const queue of queues) {
    for (const jobStatus of BATCH_JOB_STATUSES) {
      let nextToken: string | undefined;
      do {
        const listed = await restJson<{
          jobSummaryList?: { jobId?: string }[];
          nextToken?: string;
        }>("batch", "POST", "/v1/listjobs", {
          jobQueue: queue.jobQueueName,
          jobStatus,
          ...(nextToken ? { nextToken } : {}),
        });
        for (const job of listed.jobSummaryList ?? []) {
          if (job.jobId) jobIds.add(job.jobId);
        }
        nextToken = listed.nextToken;
      } while (nextToken);
    }
  }
  const ids = [...jobIds];
  if (ids.length === 0) return [];
  const jobs: BatchJob[] = [];
  for (let offset = 0; offset < ids.length; offset += 100) {
    const described = await restJson<{
      jobs?: {
        jobId?: string;
        jobArn?: string;
        jobName?: string;
        jobQueue?: string;
        jobDefinition?: string;
        status?: string;
        statusReason?: string;
        createdAt?: number;
        startedAt?: number;
        stoppedAt?: number;
        container?: { exitCode?: number };
      }[];
    }>("batch", "POST", "/v1/describejobs", { jobs: ids.slice(offset, offset + 100) });
    jobs.push(...(described.jobs ?? []).map((job) => ({
      jobId: job.jobId ?? "",
      jobArn: job.jobArn ?? "",
      jobName: job.jobName ?? "",
      jobQueue: job.jobQueue ?? "",
      jobDefinition: job.jobDefinition ?? "",
      status: job.status ?? "",
      statusReason: job.statusReason ?? "",
      createdAt: job.createdAt ?? 0,
      startedAt: job.startedAt ?? 0,
      stoppedAt: job.stoppedAt ?? 0,
      exitCode: job.container?.exitCode,
    })));
  }
  return jobs;
};

export const terminateBatchJob = async (jobId: string, reason: string): Promise<void> => {
  await restJson("batch", "POST", "/v1/terminatejob", { jobId, reason });
};

export interface BatchJobDefinition {
  jobDefinitionName: string;
  jobDefinitionArn: string;
  revision: number;
  status: string;
  type: string;
  image: string;
  vcpus: number;
  memory: number;
}

export const fetchBatchJobDefinitions = async (): Promise<BatchJobDefinition[]> => {
  const definitions: BatchJobDefinition[] = [];
  let nextToken: string | undefined;
  do {
    const described = await restJson<{
      jobDefinitions?: {
        jobDefinitionName?: string;
        jobDefinitionArn?: string;
        revision?: number;
        status?: string;
        type?: string;
        containerProperties?: { image?: string; vcpus?: number; memory?: number };
      }[];
      nextToken?: string;
    }>("batch", "POST", "/v1/describejobdefinitions", {
      ...(nextToken ? { nextToken } : {}),
    });
    definitions.push(...(described.jobDefinitions ?? []).map((definition) => ({
      jobDefinitionName: definition.jobDefinitionName ?? "",
      jobDefinitionArn: definition.jobDefinitionArn ?? "",
      revision: definition.revision ?? 0,
      status: definition.status ?? "",
      type: definition.type ?? "",
      image: definition.containerProperties?.image ?? "",
      vcpus: definition.containerProperties?.vcpus ?? 0,
      memory: definition.containerProperties?.memory ?? 0,
    })));
    nextToken = described.nextToken;
  } while (nextToken);
  return definitions;
};

// ---------------------------------------------------------------------------
// Amazon Elastic File System (EFS) — the REST-JSON protocol.
// ---------------------------------------------------------------------------

export interface EFSFileSystem {
  fileSystemId: string;
  name: string;
  lifeCycleState: string;
  performanceMode: string;
  throughputMode: string;
  sizeInBytes: number;
  numberOfMountTargets: number;
  creationTime: number;
  encrypted: boolean;
}

interface EFSFileSystemWire {
  FileSystemId?: string;
  Name?: string;
  LifeCycleState?: string;
  PerformanceMode?: string;
  ThroughputMode?: string;
  SizeInBytes?: { Value?: number };
  NumberOfMountTargets?: number;
  CreationTime?: number;
  Encrypted?: boolean;
}

const efsFileSystemFromWire = (fs: EFSFileSystemWire): EFSFileSystem => ({
  fileSystemId: fs.FileSystemId ?? "",
  name: fs.Name ?? "",
  lifeCycleState: fs.LifeCycleState ?? "",
  performanceMode: fs.PerformanceMode ?? "",
  throughputMode: fs.ThroughputMode ?? "",
  sizeInBytes: fs.SizeInBytes?.Value ?? 0,
  numberOfMountTargets: fs.NumberOfMountTargets ?? 0,
  creationTime: fs.CreationTime ?? 0,
  encrypted: fs.Encrypted ?? false,
});

export const fetchEFSFileSystems = async (): Promise<EFSFileSystem[]> => {
  const listed = await awsRestJson<{ FileSystems?: EFSFileSystemWire[] }>("elasticfilesystem", "/2015-02-01/file-systems");
  return (listed.FileSystems ?? []).map(efsFileSystemFromWire);
};

// CreateFileSystem takes a caller-supplied idempotency token; the real console
// generates one per create, which is what `crypto.randomUUID` does here.
export const createEFSFileSystem = async (name: string): Promise<void> => {
  await restJson("elasticfilesystem", "POST", "/2015-02-01/file-systems", {
    CreationToken: crypto.randomUUID(),
    Tags: [{ Key: "Name", Value: name }],
  });
};

export const deleteEFSFileSystem = async (fileSystemId: string): Promise<void> => {
  await restJson("elasticfilesystem", "DELETE", `/2015-02-01/file-systems/${encodeURIComponent(fileSystemId)}`);
};

export interface EFSMountTarget {
  mountTargetId: string;
  fileSystemId: string;
  subnetId: string;
  lifeCycleState: string;
  ipAddress: string;
  availabilityZoneName: string;
}

export const fetchEFSMountTargets = async (fileSystemId: string): Promise<EFSMountTarget[]> => {
  const listed = await awsRestJson<{
    MountTargets?: {
      MountTargetId?: string;
      FileSystemId?: string;
      SubnetId?: string;
      LifeCycleState?: string;
      IpAddress?: string;
      AvailabilityZoneName?: string;
    }[];
  }>("elasticfilesystem", `/2015-02-01/mount-targets?FileSystemId=${encodeURIComponent(fileSystemId)}`);
  return (listed.MountTargets ?? []).map((target) => ({
    mountTargetId: target.MountTargetId ?? "",
    fileSystemId: target.FileSystemId ?? "",
    subnetId: target.SubnetId ?? "",
    lifeCycleState: target.LifeCycleState ?? "",
    ipAddress: target.IpAddress ?? "",
    availabilityZoneName: target.AvailabilityZoneName ?? "",
  }));
};

// ---------------------------------------------------------------------------
// Amazon Relational Database Service (RDS) — the Query protocol
// (Version=2014-10-31).
// ---------------------------------------------------------------------------

const RDS_VERSION = "2014-10-31";

export interface RDSInstance {
  dbInstanceIdentifier: string;
  engine: string;
  engineVersion: string;
  status: string;
  dbInstanceClass: string;
  endpointAddress: string;
  endpointPort: number;
  availabilityZone: string;
  allocatedStorage: number;
  multiAZ: boolean;
  masterUsername: string;
  dbName: string;
  iamDatabaseAuthenticationEnabled: boolean;
  arn: string;
}

export const fetchRDSInstances = async (): Promise<RDSInstance[]> => {
  const xml = await awsQuery("rds", RDS_VERSION, "DescribeDBInstances");
  return xmlList(xml, "DBInstances").map((item) => {
    const endpoint = childElement(item, "Endpoint");
    return {
      dbInstanceIdentifier: childText(item, "DBInstanceIdentifier"),
      engine: childText(item, "Engine"),
      engineVersion: childText(item, "EngineVersion"),
      status: childText(item, "DBInstanceStatus"),
      dbInstanceClass: childText(item, "DBInstanceClass"),
      endpointAddress: endpoint ? childText(endpoint, "Address") : "",
      endpointPort: endpoint ? Number(childText(endpoint, "Port") || "0") : 0,
      availabilityZone: childText(item, "AvailabilityZone"),
      allocatedStorage: Number(childText(item, "AllocatedStorage") || "0"),
      multiAZ: childText(item, "MultiAZ") === "true",
      masterUsername: childText(item, "MasterUsername"),
      dbName: childText(item, "DBName"),
      iamDatabaseAuthenticationEnabled: childText(item, "IAMDatabaseAuthenticationEnabled") === "true",
      arn: childText(item, "DBInstanceArn"),
    };
  });
};

export const createRDSInstance = async (input: {
  dbInstanceIdentifier: string;
  engine: "postgres" | "mysql";
  dbInstanceClass: string;
  allocatedStorage: number;
  masterUsername: string;
  masterUserPassword: string;
  dbName: string;
  enableIAMDatabaseAuthentication: boolean;
}): Promise<void> => {
  await awsQuery("rds", RDS_VERSION, "CreateDBInstance", {
    DBInstanceIdentifier: input.dbInstanceIdentifier,
    Engine: input.engine,
    DBInstanceClass: input.dbInstanceClass,
    AllocatedStorage: String(input.allocatedStorage),
    MasterUsername: input.masterUsername,
    MasterUserPassword: input.masterUserPassword,
    DBName: input.dbName,
    EnableIAMDatabaseAuthentication: String(input.enableIAMDatabaseAuthentication),
  });
};

export const modifyRDSInstanceAuthentication = async (input: {
  dbInstanceIdentifier: string;
  enableIAMDatabaseAuthentication: boolean;
  masterUserPassword?: string;
}): Promise<void> => {
  const parameters: Record<string, string> = {
    DBInstanceIdentifier: input.dbInstanceIdentifier,
    EnableIAMDatabaseAuthentication: String(input.enableIAMDatabaseAuthentication),
    ApplyImmediately: "true",
  };
  if (input.masterUserPassword) parameters.MasterUserPassword = input.masterUserPassword;
  await awsQuery("rds", RDS_VERSION, "ModifyDBInstance", parameters);
};

export interface RDSCluster {
  dbClusterIdentifier: string;
  engine: string;
  engineVersion: string;
  status: string;
  endpoint: string;
  arn: string;
}

export const fetchRDSClusters = async (): Promise<RDSCluster[]> => {
  const xml = await awsQuery("rds", RDS_VERSION, "DescribeDBClusters");
  return xmlList(xml, "DBClusters").map((item) => ({
    dbClusterIdentifier: childText(item, "DBClusterIdentifier"),
    engine: childText(item, "Engine"),
    engineVersion: childText(item, "EngineVersion"),
    status: childText(item, "Status"),
    endpoint: childText(item, "Endpoint"),
    arn: childText(item, "DBClusterArn"),
  }));
};

// DeleteDBInstance without a final snapshot is what the real console's delete
// dialog sends when the operator clears "Create final snapshot"; the console
// states that plainly before it asks for confirmation.
export const deleteRDSInstance = async (dbInstanceIdentifier: string): Promise<void> => {
  await awsQuery("rds", RDS_VERSION, "DeleteDBInstance", {
    DBInstanceIdentifier: dbInstanceIdentifier,
    SkipFinalSnapshot: "true",
  });
};

// ---------------------------------------------------------------------------
// Amazon DynamoDB — awsjson1.0, X-Amz-Target DynamoDB_20120810.<Op>.
// ---------------------------------------------------------------------------

const DDB_TARGET_PREFIX = "DynamoDB_20120810.";

const ddbJson = <T>(operation: string, input: Record<string, unknown> = {}): Promise<T> =>
  awsJson10<T>("dynamodb", `${DDB_TARGET_PREFIX}${operation}`, input);

export interface DynamoDBTable {
  tableName: string;
  tableStatus: string;
  tableArn: string;
  itemCount: number;
  tableSizeBytes: number;
  partitionKey: string;
  sortKey: string;
  billingMode: string;
  creationDateTime: number;
  globalSecondaryIndexes: string[];
}

interface DDBTableDescriptionWire {
  TableName?: string;
  TableStatus?: string;
  TableArn?: string;
  ItemCount?: number;
  TableSizeBytes?: number;
  CreationDateTime?: number;
  KeySchema?: { AttributeName?: string; KeyType?: string }[];
  BillingModeSummary?: { BillingMode?: string };
  GlobalSecondaryIndexes?: { IndexName?: string }[];
}

const ddbTableFromWire = (table: DDBTableDescriptionWire, fallbackName: string): DynamoDBTable => {
  const schema = table.KeySchema ?? [];
  return {
    tableName: table.TableName ?? fallbackName,
    tableStatus: table.TableStatus ?? "",
    tableArn: table.TableArn ?? "",
    itemCount: table.ItemCount ?? 0,
    tableSizeBytes: table.TableSizeBytes ?? 0,
    partitionKey: schema.find((key) => key.KeyType === "HASH")?.AttributeName ?? "",
    sortKey: schema.find((key) => key.KeyType === "RANGE")?.AttributeName ?? "",
    billingMode: table.BillingModeSummary?.BillingMode ?? "PROVISIONED",
    creationDateTime: table.CreationDateTime ?? 0,
    globalSecondaryIndexes: (table.GlobalSecondaryIndexes ?? []).map((index) => index.IndexName ?? ""),
  };
};

// DynamoDB has no operation that answers a table's properties for every table
// at once: ListTables answers names, and DescribeTable answers one table's
// description — the same two calls the real console's Tables page makes.
export const fetchDynamoDBTables = async (): Promise<DynamoDBTable[]> => {
  const listed = await ddbJson<{ TableNames?: string[] }>("ListTables");
  const tables: DynamoDBTable[] = [];
  for (const name of listed.TableNames ?? []) {
    const described = await ddbJson<{ Table?: DDBTableDescriptionWire }>("DescribeTable", { TableName: name });
    tables.push(ddbTableFromWire(described.Table ?? {}, name));
  }
  return tables;
};

export const fetchDynamoDBTable = async (tableName: string): Promise<DynamoDBTable> => {
  const described = await ddbJson<{ Table?: DDBTableDescriptionWire }>("DescribeTable", { TableName: tableName });
  if (!described.Table) throw new Error(`DescribeTable returned no Table for ${tableName}`);
  return ddbTableFromWire(described.Table, tableName);
};

export interface CreateDynamoDBTableInput {
  tableName: string;
  partitionKey: string;
  partitionKeyType: "S" | "N" | "B";
  sortKey?: string;
  sortKeyType?: "S" | "N" | "B";
}

// CreateTable in the real console's default "on-demand" mode: PAY_PER_REQUEST
// billing, a partition key, and an optional sort key.
export const createDynamoDBTable = async (input: CreateDynamoDBTableInput): Promise<void> => {
  const attributeDefinitions = [{ AttributeName: input.partitionKey, AttributeType: input.partitionKeyType }];
  const keySchema = [{ AttributeName: input.partitionKey, KeyType: "HASH" }];
  if (input.sortKey) {
    attributeDefinitions.push({ AttributeName: input.sortKey, AttributeType: input.sortKeyType ?? "S" });
    keySchema.push({ AttributeName: input.sortKey, KeyType: "RANGE" });
  }
  await ddbJson("CreateTable", {
    TableName: input.tableName,
    AttributeDefinitions: attributeDefinitions,
    KeySchema: keySchema,
    BillingMode: "PAY_PER_REQUEST",
  });
};

export const deleteDynamoDBTable = async (tableName: string): Promise<void> => {
  await ddbJson("DeleteTable", { TableName: tableName });
};

// ---------------------------------------------------------------------------
// Amazon ElastiCache — the Query protocol (Version=2015-02-02).
// ---------------------------------------------------------------------------

export interface ElastiCacheCluster {
  cacheClusterId: string;
  engine: string;
  engineVersion: string;
  status: string;
  cacheNodeType: string;
  numCacheNodes: number;
  preferredAvailabilityZone: string;
  arn: string;
}

export const fetchElastiCacheClusters = async (): Promise<ElastiCacheCluster[]> => {
  const xml = await awsQuery("elasticache", "2015-02-02", "DescribeCacheClusters");
  return xmlList(xml, "CacheClusters").map((item) => ({
    cacheClusterId: childText(item, "CacheClusterId"),
    engine: childText(item, "Engine"),
    engineVersion: childText(item, "EngineVersion"),
    status: childText(item, "CacheClusterStatus"),
    cacheNodeType: childText(item, "CacheNodeType"),
    numCacheNodes: Number(childText(item, "NumCacheNodes") || "0"),
    preferredAvailabilityZone: childText(item, "PreferredAvailabilityZone"),
    arn: childText(item, "ARN"),
  }));
};

// ---------------------------------------------------------------------------
// Amazon CloudFront — the REST-XML protocol (GET /2020-05-31/distribution).
// ---------------------------------------------------------------------------

export interface CloudFrontDistribution {
  id: string;
  arn: string;
  status: string;
  domainName: string;
  comment: string;
  enabled: boolean;
  lastModifiedTime: string;
  aliases: string[];
  origins: string[];
  priceClass: string;
  httpVersion: string;
}

export const fetchCloudFrontDistributions = async (): Promise<CloudFrontDistribution[]> => {
  const xml = await awsRestXml("cloudfront", "/2020-05-31/distribution");
  return xmlList(xml, "Items").map((summary) => ({
    id: childText(summary, "Id"),
    arn: childText(summary, "ARN"),
    status: childText(summary, "Status"),
    domainName: childText(summary, "DomainName"),
    comment: childText(summary, "Comment"),
    enabled: childText(summary, "Enabled") === "true",
    lastModifiedTime: childText(summary, "LastModifiedTime"),
    aliases: Array.from(childElement(summary, "Aliases")?.getElementsByTagName("CNAME") ?? []).map(
      (alias) => alias.textContent ?? "",
    ),
    origins: Array.from(childElement(summary, "Origins")?.getElementsByTagName("DomainName") ?? []).map(
      (origin) => origin.textContent ?? "",
    ),
    priceClass: childText(summary, "PriceClass"),
    httpVersion: childText(summary, "HttpVersion"),
  }));
};

// ---------------------------------------------------------------------------
// Amazon Route 53 — the REST-XML protocol (GET /2013-04-01/hostedzone).
// ---------------------------------------------------------------------------

export interface Route53HostedZone {
  id: string;
  name: string;
  comment: string;
  privateZone: boolean;
  resourceRecordSetCount: number;
}

// A hosted zone's Id arrives as the resource path `/hostedzone/Z123`; every
// Route 53 operation that takes one accepts the bare id, which is also what
// the real console shows.
function route53ZoneId(path: string): string {
  return path.replace(/^\/hostedzone\//, "");
}

export const fetchRoute53HostedZones = async (): Promise<Route53HostedZone[]> => {
  const xml = await awsRestXml("route53", "/2013-04-01/hostedzone");
  return xmlList(xml, "HostedZones").map((zone) => {
    const config = childElement(zone, "Config");
    return {
      id: route53ZoneId(childText(zone, "Id")),
      name: childText(zone, "Name"),
      comment: config ? childText(config, "Comment") : "",
      privateZone: config ? childText(config, "PrivateZone") === "true" : false,
      resourceRecordSetCount: Number(childText(zone, "ResourceRecordSetCount") || "0"),
    };
  });
};

export interface Route53RecordSet {
  name: string;
  type: string;
  ttl: number;
  values: string[];
}

export const fetchRoute53RecordSets = async (hostedZoneId: string): Promise<Route53RecordSet[]> => {
  const xml = await awsRestXml("route53", `/2013-04-01/hostedzone/${encodeURIComponent(hostedZoneId)}/rrset`);
  return xmlList(xml, "ResourceRecordSets").map((record) => ({
    name: childText(record, "Name"),
    type: childText(record, "Type"),
    ttl: Number(childText(record, "TTL") || "0"),
    values: Array.from(record.getElementsByTagName("Value")).map((value) => value.textContent ?? ""),
  }));
};

// ---------------------------------------------------------------------------
// Amazon API Gateway — REST-JSON. The v1 (REST API) and v2 (HTTP and WebSocket
// API) surfaces are separate AWS APIs on separate paths, which is why the
// console reads both.
// ---------------------------------------------------------------------------

export interface APIGatewayRestApi {
  id: string;
  name: string;
  description: string;
  createdDate: number;
  apiKeySource: string;
  endpointTypes: string[];
}

export const fetchAPIGatewayRestApis = async (): Promise<APIGatewayRestApi[]> => {
  const listed = await awsRestJson<{
    item?: {
      id?: string;
      name?: string;
      description?: string;
      createdDate?: number;
      apiKeySource?: string;
      endpointConfiguration?: { types?: string[] };
    }[];
  }>("apigateway", "/restapis");
  return (listed.item ?? []).map((api) => ({
    id: api.id ?? "",
    name: api.name ?? "",
    description: api.description ?? "",
    createdDate: api.createdDate ?? 0,
    apiKeySource: api.apiKeySource ?? "",
    endpointTypes: api.endpointConfiguration?.types ?? [],
  }));
};

export interface APIGatewayV2Api {
  apiId: string;
  name: string;
  protocolType: string;
  apiEndpoint: string;
  routeSelectionExpression: string;
  createdDate: string;
}

export const fetchAPIGatewayV2Apis = async (): Promise<APIGatewayV2Api[]> => {
  const listed = await awsRestJson<{
    Items?: {
      ApiId?: string;
      Name?: string;
      ProtocolType?: string;
      ApiEndpoint?: string;
      RouteSelectionExpression?: string;
      CreatedDate?: string;
    }[];
  }>("apigateway", "/v2/apis");
  return (listed.Items ?? []).map((api) => ({
    apiId: api.ApiId ?? "",
    name: api.Name ?? "",
    protocolType: api.ProtocolType ?? "",
    apiEndpoint: api.ApiEndpoint ?? "",
    routeSelectionExpression: api.RouteSelectionExpression ?? "",
    createdDate: api.CreatedDate ?? "",
  }));
};

// ---------------------------------------------------------------------------
// Elastic Load Balancing — the Query protocol (Version=2015-12-01), the API
// behind Application, Network, and Gateway Load Balancers.
// ---------------------------------------------------------------------------

const ELBV2_VERSION = "2015-12-01";

export interface LoadBalancer {
  loadBalancerArn: string;
  loadBalancerName: string;
  dnsName: string;
  type: string;
  scheme: string;
  state: string;
  vpcId: string;
  availabilityZones: string[];
}

export const fetchLoadBalancers = async (): Promise<LoadBalancer[]> => {
  const xml = await awsQuery("elasticloadbalancing", ELBV2_VERSION, "DescribeLoadBalancers");
  return xmlList(xml, "LoadBalancers").map((member) => {
    const state = childElement(member, "State");
    return {
      loadBalancerArn: childText(member, "LoadBalancerArn"),
      loadBalancerName: childText(member, "LoadBalancerName"),
      dnsName: childText(member, "DNSName"),
      type: childText(member, "Type"),
      scheme: childText(member, "Scheme"),
      state: state ? childText(state, "Code") : "",
      vpcId: childText(member, "VpcId"),
      availabilityZones: xmlList(member, "AvailabilityZones").map((zone) => childText(zone, "ZoneName")),
    };
  });
};

export interface TargetGroup {
  targetGroupArn: string;
  targetGroupName: string;
  protocol: string;
  port: number;
  targetType: string;
  vpcId: string;
  healthCheckPath: string;
}

export const fetchTargetGroups = async (): Promise<TargetGroup[]> => {
  const xml = await awsQuery("elasticloadbalancing", ELBV2_VERSION, "DescribeTargetGroups");
  return xmlList(xml, "TargetGroups").map((member) => ({
    targetGroupArn: childText(member, "TargetGroupArn"),
    targetGroupName: childText(member, "TargetGroupName"),
    protocol: childText(member, "Protocol"),
    port: Number(childText(member, "Port") || "0"),
    targetType: childText(member, "TargetType"),
    vpcId: childText(member, "VpcId"),
    healthCheckPath: childText(member, "HealthCheckPath"),
  }));
};

// ---------------------------------------------------------------------------
// AWS Cloud Map — awsjson1.1, X-Amz-Target Route53AutoNaming_v20170314.<Op>.
// ---------------------------------------------------------------------------

const CLOUDMAP_TARGET_PREFIX = "Route53AutoNaming_v20170314.";

export interface CloudMapNamespace {
  id: string;
  arn: string;
  name: string;
  type: string;
  description: string;
  serviceCount: number;
  createDate: number;
}

export const fetchCloudMapNamespaces = async (): Promise<CloudMapNamespace[]> => {
  const listed = await awsJson<{
    Namespaces?: {
      Id?: string;
      Arn?: string;
      Name?: string;
      Type?: string;
      Description?: string;
      ServiceCount?: number;
      CreateDate?: number;
    }[];
  }>("servicediscovery", `${CLOUDMAP_TARGET_PREFIX}ListNamespaces`, {});
  return (listed.Namespaces ?? []).map((namespace) => ({
    id: namespace.Id ?? "",
    arn: namespace.Arn ?? "",
    name: namespace.Name ?? "",
    type: namespace.Type ?? "",
    description: namespace.Description ?? "",
    serviceCount: namespace.ServiceCount ?? 0,
    createDate: namespace.CreateDate ?? 0,
  }));
};

export interface CloudMapService {
  id: string;
  arn: string;
  name: string;
  description: string;
  instanceCount: number;
  createDate: number;
}

export const fetchCloudMapServices = async (): Promise<CloudMapService[]> => {
  const listed = await awsJson<{
    Services?: { Id?: string; Arn?: string; Name?: string; Description?: string; InstanceCount?: number; CreateDate?: number }[];
  }>("servicediscovery", `${CLOUDMAP_TARGET_PREFIX}ListServices`, {});
  return (listed.Services ?? []).map((service) => ({
    id: service.Id ?? "",
    arn: service.Arn ?? "",
    name: service.Name ?? "",
    description: service.Description ?? "",
    instanceCount: service.InstanceCount ?? 0,
    createDate: service.CreateDate ?? 0,
  }));
};

// ---------------------------------------------------------------------------
// AWS CodeBuild — awsjson1.1, X-Amz-Target CodeBuild_20161006.<Op>.
// ---------------------------------------------------------------------------

export interface CodeBuildProject {
  name: string;
  arn: string;
  sourceType: string;
  environmentImage: string;
  environmentType: string;
  serviceRole: string;
  created: number;
  lastModified: number;
}

// ListProjects answers names; BatchGetProjects answers the descriptions — the
// same two calls the real console's Build projects page makes.
export const fetchCodeBuildProjects = async (): Promise<CodeBuildProject[]> => {
  const listed = await awsJson<{ projects?: string[] }>("codebuild", "CodeBuild_20161006.ListProjects", {});
  const names = listed.projects ?? [];
  if (names.length === 0) return [];
  const described = await awsJson<{
    projects?: {
      name?: string;
      arn?: string;
      source?: { type?: string };
      environment?: { image?: string; type?: string };
      serviceRole?: string;
      created?: number;
      lastModified?: number;
    }[];
  }>("codebuild", "CodeBuild_20161006.BatchGetProjects", { names });
  return (described.projects ?? []).map((project) => ({
    name: project.name ?? "",
    arn: project.arn ?? "",
    sourceType: project.source?.type ?? "",
    environmentImage: project.environment?.image ?? "",
    environmentType: project.environment?.type ?? "",
    serviceRole: project.serviceRole ?? "",
    created: project.created ?? 0,
    lastModified: project.lastModified ?? 0,
  }));
};

export const createCodeBuildProject = async (input: {
  name: string;
  image: string;
  buildspec: string;
  serviceRole: string;
}): Promise<void> => {
  await awsJson("codebuild", "CodeBuild_20161006.CreateProject", {
    name: input.name,
    source: { type: "NO_SOURCE", buildspec: input.buildspec },
    artifacts: { type: "NO_ARTIFACTS" },
    environment: {
      type: "LINUX_CONTAINER",
      image: input.image,
      computeType: "BUILD_GENERAL1_SMALL",
    },
    serviceRole: input.serviceRole,
  });
};

export const deleteCodeBuildProject = async (name: string): Promise<void> => {
  await awsJson("codebuild", "CodeBuild_20161006.DeleteProject", { name });
};

export const startCodeBuild = async (projectName: string): Promise<void> => {
  await awsJson("codebuild", "CodeBuild_20161006.StartBuild", { projectName });
};

export const stopCodeBuild = async (id: string): Promise<void> => {
  await awsJson("codebuild", "CodeBuild_20161006.StopBuild", { id });
};

export interface CodeBuildBuild {
  id: string;
  projectName: string;
  status: string;
  startTime: number;
  endTime: number;
  environmentImage: string;
}

export const fetchCodeBuildBuilds = async (): Promise<CodeBuildBuild[]> => {
  const listed = await awsJson<{ ids?: string[] }>("codebuild", "CodeBuild_20161006.ListBuilds", {
    sortOrder: "DESCENDING",
  });
  const ids = listed.ids ?? [];
  if (ids.length === 0) return [];
  const described = await awsJson<{
    builds?: {
      id?: string;
      projectName?: string;
      buildStatus?: string;
      startTime?: number;
      endTime?: number;
      environment?: { image?: string };
    }[];
  }>("codebuild", "CodeBuild_20161006.BatchGetBuilds", { ids });
  return (described.builds ?? []).map((build) => ({
    id: build.id ?? "",
    projectName: build.projectName ?? "",
    status: build.buildStatus ?? "",
    startTime: build.startTime ?? 0,
    endTime: build.endTime ?? 0,
    environmentImage: build.environment?.image ?? "",
  }));
};

// ---------------------------------------------------------------------------
// AWS Amplify — the REST-JSON protocol (GET /apps).
// ---------------------------------------------------------------------------

export interface AmplifyApp {
  appId: string;
  appArn: string;
  name: string;
  platform: string;
  defaultDomain: string;
  repository: string;
  repositoryCloneMethod: string;
  createTime: number;
}

export const fetchAmplifyApps = async (): Promise<AmplifyApp[]> => {
  const listed = await awsRestJson<{
    apps?: {
      appId?: string;
      appArn?: string;
      name?: string;
      platform?: string;
      defaultDomain?: string;
      repository?: string;
      repositoryCloneMethod?: string;
      createTime?: number;
    }[];
  }>("amplify", "/apps");
  return (listed.apps ?? []).map((app) => ({
    appId: app.appId ?? "",
    appArn: app.appArn ?? "",
    name: app.name ?? "",
    platform: app.platform ?? "",
    defaultDomain: app.defaultDomain ?? "",
    repository: app.repository ?? "",
    repositoryCloneMethod: app.repositoryCloneMethod ?? "",
    createTime: app.createTime ?? 0,
  }));
};

export const createAmplifyApp = async (input: {
  name: string;
  repository: string;
  accessToken: string;
  platform: "WEB" | "WEB_COMPUTE";
  buildSpec: string;
}): Promise<void> => {
  await restJson("amplify", "POST", "/apps", {
    name: input.name,
    repository: input.repository || undefined,
    accessToken: input.accessToken || undefined,
    platform: input.platform,
    buildSpec: input.buildSpec || undefined,
  });
};

export const deleteAmplifyApp = async (appId: string): Promise<void> => {
  await restJson("amplify", "DELETE", `/apps/${encodeURIComponent(appId)}`);
};

export interface AmplifyBranch {
  branchName: string;
  stage: string;
  framework: string;
  activeJobId: string;
  enableAutoBuild: boolean;
  updateTime: number;
}

export const fetchAmplifyBranches = async (appId: string): Promise<AmplifyBranch[]> => {
  const result = await awsRestJson<{
    branches?: {
      branchName?: string;
      stage?: string;
      framework?: string;
      activeJobId?: string;
      enableAutoBuild?: boolean;
      updateTime?: number;
    }[];
  }>("amplify", `/apps/${encodeURIComponent(appId)}/branches`);
  return (result.branches ?? []).map((branch) => ({
    branchName: branch.branchName ?? "",
    stage: branch.stage ?? "",
    framework: branch.framework ?? "",
    activeJobId: branch.activeJobId ?? "",
    enableAutoBuild: branch.enableAutoBuild ?? false,
    updateTime: branch.updateTime ?? 0,
  }));
};

export const createAmplifyBranch = async (appId: string, branchName: string): Promise<void> => {
  await restJson("amplify", "POST", `/apps/${encodeURIComponent(appId)}/branches`, {
    branchName,
    enableAutoBuild: true,
    stage: "PRODUCTION",
  });
};

export const startAmplifyJob = async (appId: string, branchName: string): Promise<void> => {
  await restJson(
    "amplify",
    "POST",
    `/apps/${encodeURIComponent(appId)}/branches/${encodeURIComponent(branchName)}/jobs`,
    { jobType: "RELEASE" },
  );
};

export interface AmplifyJob {
  jobId: string;
  status: string;
  jobType: string;
  commitId: string;
  commitMessage: string;
  startTime: number;
  endTime: number;
}

export const fetchAmplifyJobs = async (appId: string, branchName: string): Promise<AmplifyJob[]> => {
  const result = await awsRestJson<{
    jobSummaries?: {
      jobId?: string;
      status?: string;
      jobType?: string;
      commitId?: string;
      commitMessage?: string;
      startTime?: number;
      endTime?: number;
    }[];
  }>(
    "amplify",
    `/apps/${encodeURIComponent(appId)}/branches/${encodeURIComponent(branchName)}/jobs?maxResults=50`,
  );
  return (result.jobSummaries ?? []).map((job) => ({
    jobId: job.jobId ?? "",
    status: job.status ?? "",
    jobType: job.jobType ?? "",
    commitId: job.commitId ?? "",
    commitMessage: job.commitMessage ?? "",
    startTime: job.startTime ?? 0,
    endTime: job.endTime ?? 0,
  }));
};

// ---------------------------------------------------------------------------
// Amazon Kinesis Data Streams — awsjson1.1, X-Amz-Target Kinesis_20131202.<Op>.
// ---------------------------------------------------------------------------

export interface KinesisStream {
  streamName: string;
  streamArn: string;
  status: string;
  streamMode: string;
  openShardCount: number;
  retentionPeriodHours: number;
  creationTimestamp: number;
}

// ListStreams answers names; DescribeStreamSummary answers each stream's
// properties without enumerating its shards, which is what the real console's
// Data streams table shows.
export const fetchKinesisStreams = async (): Promise<KinesisStream[]> => {
  const listed = await awsJson<{ StreamNames?: string[] }>("kinesis", "Kinesis_20131202.ListStreams", {});
  const streams: KinesisStream[] = [];
  for (const name of listed.StreamNames ?? []) {
    const described = await awsJson<{
      StreamDescriptionSummary?: {
        StreamName?: string;
        StreamARN?: string;
        StreamStatus?: string;
        StreamModeDetails?: { StreamMode?: string };
        OpenShardCount?: number;
        RetentionPeriodHours?: number;
        StreamCreationTimestamp?: number;
      };
    }>("kinesis", "Kinesis_20131202.DescribeStreamSummary", { StreamName: name });
    const summary = described.StreamDescriptionSummary ?? {};
    streams.push({
      streamName: summary.StreamName ?? name,
      streamArn: summary.StreamARN ?? "",
      status: summary.StreamStatus ?? "",
      streamMode: summary.StreamModeDetails?.StreamMode ?? "",
      openShardCount: summary.OpenShardCount ?? 0,
      retentionPeriodHours: summary.RetentionPeriodHours ?? 0,
      creationTimestamp: summary.StreamCreationTimestamp ?? 0,
    });
  }
  return streams;
};

// ---------------------------------------------------------------------------
// AWS Glue — awsjson1.1, X-Amz-Target AWSGlue.<Op>.
// ---------------------------------------------------------------------------

export interface GlueDatabase {
  name: string;
  description: string;
  locationUri: string;
  createTime: number;
}

export const fetchGlueDatabases = async (): Promise<GlueDatabase[]> => {
  const listed = await awsJson<{
    DatabaseList?: { Name?: string; Description?: string; LocationUri?: string; CreateTime?: number }[];
  }>("glue", "AWSGlue.GetDatabases", {});
  return (listed.DatabaseList ?? []).map((database) => ({
    name: database.Name ?? "",
    description: database.Description ?? "",
    locationUri: database.LocationUri ?? "",
    createTime: database.CreateTime ?? 0,
  }));
};

export interface GlueJob {
  name: string;
  role: string;
  glueVersion: string;
  workerType: string;
  scriptLocation: string;
  createdOn: number;
}

export const fetchGlueJobs = async (): Promise<GlueJob[]> => {
  const listed = await awsJson<{
    Jobs?: {
      Name?: string;
      Role?: string;
      GlueVersion?: string;
      WorkerType?: string;
      Command?: { ScriptLocation?: string };
      CreatedOn?: number;
    }[];
  }>("glue", "AWSGlue.GetJobs", {});
  return (listed.Jobs ?? []).map((job) => ({
    name: job.Name ?? "",
    role: job.Role ?? "",
    glueVersion: job.GlueVersion ?? "",
    workerType: job.WorkerType ?? "",
    scriptLocation: job.Command?.ScriptLocation ?? "",
    createdOn: job.CreatedOn ?? 0,
  }));
};

export interface GlueGlossary {
  id: string;
  name: string;
  description: string;
}

export const fetchGlueGlossaries = async (): Promise<GlueGlossary[]> => {
  const items: GlueGlossary[] = [];
  let nextToken: string | undefined;
  do {
    const listed = await awsJson<{
      Items?: { Id?: string; Name?: string; Description?: string }[];
      NextToken?: string;
    }>("glue", "AWSGlue.ListGlossaries", nextToken ? { NextToken: nextToken } : {});
    items.push(
      ...(listed.Items ?? []).map((glossary) => ({
        id: glossary.Id ?? "",
        name: glossary.Name ?? "",
        description: glossary.Description ?? "",
      })),
    );
    nextToken = listed.NextToken;
  } while (nextToken);
  return items;
};

export const createGlueGlossary = async (name: string, description: string): Promise<GlueGlossary> => {
  const created = await awsJson<{ Id?: string; Name?: string; Description?: string }>(
    "glue",
    "AWSGlue.CreateGlossary",
    { Name: name, ...(description ? { Description: description } : {}) },
  );
  return {
    id: created.Id ?? "",
    name: created.Name ?? name,
    description: created.Description ?? description,
  };
};

export const deleteGlueGlossary = async (identifier: string): Promise<void> => {
  await awsJson("glue", "AWSGlue.DeleteGlossary", { Identifier: identifier });
};

export interface GlueAssetType {
  id: string;
  name: string;
}

export const fetchGlueAssetTypes = async (): Promise<GlueAssetType[]> => {
  const items: GlueAssetType[] = [];
  let nextToken: string | undefined;
  do {
    const listed = await awsJson<{ Items?: { Id?: string; Name?: string }[]; NextToken?: string }>(
      "glue",
      "AWSGlue.ListAssetTypes",
      nextToken ? { NextToken: nextToken } : {},
    );
    items.push(...(listed.Items ?? []).map((assetType) => ({ id: assetType.Id ?? "", name: assetType.Name ?? "" })));
    nextToken = listed.NextToken;
  } while (nextToken);
  return items;
};

// ---------------------------------------------------------------------------
// Amazon Simple Notification Service (SNS) — the Query protocol
// (Version=2010-03-31).
// ---------------------------------------------------------------------------

const SNS_VERSION = "2010-03-31";

export interface SNSTopic {
  arn: string;
  name: string;
}

/** An SNS topic's name is the last segment of its ARN — SNS has no separate
 * name field on ListTopics, and the real console displays the same segment. */
function snsTopicName(arn: string): string {
  return arn.slice(arn.lastIndexOf(":") + 1);
}

export const fetchSNSTopics = async (): Promise<SNSTopic[]> => {
  const xml = await awsQuery("sns", SNS_VERSION, "ListTopics");
  return xmlList(xml, "Topics").map((member) => {
    const arn = childText(member, "TopicArn");
    return { arn, name: snsTopicName(arn) };
  });
};

export const createSNSTopic = async (name: string): Promise<void> => {
  await awsQuery("sns", SNS_VERSION, "CreateTopic", { Name: name });
};

export const deleteSNSTopic = async (topicArn: string): Promise<void> => {
  await awsQuery("sns", SNS_VERSION, "DeleteTopic", { TopicArn: topicArn });
};

export const publishSNSMessage = async (topicArn: string, message: string, subject = ""): Promise<string> => {
  const xml = await awsQuery("sns", SNS_VERSION, "Publish", { TopicArn: topicArn, Message: message, Subject: subject });
  return childText(xml.documentElement, "MessageId");
};

export const subscribeSNSEndpoint = async (topicArn: string, protocol: string, endpoint: string): Promise<string> => {
  const xml = await awsQuery("sns", SNS_VERSION, "Subscribe", {
    TopicArn: topicArn,
    Protocol: protocol,
    Endpoint: endpoint,
    ReturnSubscriptionArn: "true",
  });
  return childText(xml.documentElement, "SubscriptionArn");
};

export const unsubscribeSNSEndpoint = async (subscriptionArn: string): Promise<void> => {
  await awsQuery("sns", SNS_VERSION, "Unsubscribe", { SubscriptionArn: subscriptionArn });
};

export const fetchSNSTopicAttributes = async (topicArn: string): Promise<Record<string, string>> => {
  const xml = await awsQuery("sns", SNS_VERSION, "GetTopicAttributes", { TopicArn: topicArn });
  return Object.fromEntries(
    xmlList(xml, "Attributes").map((entry) => [childText(entry, "key"), childText(entry, "value")]),
  );
};

export const setSNSTopicAttribute = async (topicArn: string, name: string, value: string): Promise<void> => {
  await awsQuery("sns", SNS_VERSION, "SetTopicAttributes", {
    TopicArn: topicArn,
    AttributeName: name,
    AttributeValue: value,
  });
};

export interface SNSSubscription {
  subscriptionArn: string;
  topicArn: string;
  protocol: string;
  endpoint: string;
  owner: string;
}

export const fetchSNSSubscriptions = async (): Promise<SNSSubscription[]> => {
  const xml = await awsQuery("sns", SNS_VERSION, "ListSubscriptions");
  return xmlList(xml, "Subscriptions").map((member) => ({
    subscriptionArn: childText(member, "SubscriptionArn"),
    topicArn: childText(member, "TopicArn"),
    protocol: childText(member, "Protocol"),
    endpoint: childText(member, "Endpoint"),
    owner: childText(member, "Owner"),
  }));
};

// ---------------------------------------------------------------------------
// Amazon Simple Queue Service (SQS) — awsjson1.0, X-Amz-Target AmazonSQS.<Op>
// (SQS moved off the Query protocol in 2023).
// ---------------------------------------------------------------------------

export interface SQSQueue {
  queueUrl: string;
  name: string;
  arn: string;
  approximateNumberOfMessages: number;
  approximateNumberOfMessagesNotVisible: number;
  createdTimestamp: number;
  visibilityTimeout: number;
  policy: string;
}

/** A queue's name is the last path segment of its URL, which is how every SQS
 * client (and the real console) derives it. */
function sqsQueueName(queueUrl: string): string {
  return queueUrl.slice(queueUrl.lastIndexOf("/") + 1);
}

// ListQueues answers URLs only; the real console's Queues table shows the
// message counts and creation time, which come from GetQueueAttributes.
export const fetchSQSQueues = async (): Promise<SQSQueue[]> => {
  const listed = await awsJson10<{ QueueUrls?: string[] }>("sqs", "AmazonSQS.ListQueues", {});
  const queues: SQSQueue[] = [];
  for (const queueUrl of listed.QueueUrls ?? []) {
    const attributes = await awsJson10<{ Attributes?: Record<string, string> }>("sqs", "AmazonSQS.GetQueueAttributes", {
      QueueUrl: queueUrl,
      AttributeNames: ["All"],
    });
    const values = attributes.Attributes ?? {};
    queues.push({
      queueUrl,
      name: sqsQueueName(queueUrl),
      arn: values.QueueArn ?? "",
      approximateNumberOfMessages: Number(values.ApproximateNumberOfMessages ?? "0"),
      approximateNumberOfMessagesNotVisible: Number(values.ApproximateNumberOfMessagesNotVisible ?? "0"),
      createdTimestamp: Number(values.CreatedTimestamp ?? "0"),
      visibilityTimeout: Number(values.VisibilityTimeout ?? "0"),
      policy: values.Policy ?? "",
    });
  }
  return queues;
};

export const createSQSQueue = async (queueName: string): Promise<void> => {
  await awsJson10("sqs", "AmazonSQS.CreateQueue", { QueueName: queueName });
};

export const deleteSQSQueue = async (queueUrl: string): Promise<void> => {
  await awsJson10("sqs", "AmazonSQS.DeleteQueue", { QueueUrl: queueUrl });
};

export interface SQSMessage {
  messageId: string;
  receiptHandle: string;
  body: string;
  attributes: Record<string, string>;
}

export const sendSQSMessage = async (queueUrl: string, messageBody: string): Promise<string> => {
  const response = await awsJson10<{ MessageId?: string }>("sqs", "AmazonSQS.SendMessage", {
    QueueUrl: queueUrl,
    MessageBody: messageBody,
  });
  return response.MessageId ?? "";
};

export const receiveSQSMessages = async (queueUrl: string): Promise<SQSMessage[]> => {
  const response = await awsJson10<{
    Messages?: { MessageId?: string; ReceiptHandle?: string; Body?: string; Attributes?: Record<string, string> }[];
  }>("sqs", "AmazonSQS.ReceiveMessage", {
    QueueUrl: queueUrl,
    MaxNumberOfMessages: 10,
    WaitTimeSeconds: 0,
    AttributeNames: ["All"],
    MessageAttributeNames: ["All"],
  });
  return (response.Messages ?? []).map((message) => ({
    messageId: message.MessageId ?? "",
    receiptHandle: message.ReceiptHandle ?? "",
    body: message.Body ?? "",
    attributes: message.Attributes ?? {},
  }));
};

export const deleteSQSMessage = async (queueUrl: string, receiptHandle: string): Promise<void> => {
  await awsJson10("sqs", "AmazonSQS.DeleteMessage", { QueueUrl: queueUrl, ReceiptHandle: receiptHandle });
};

export const purgeSQSQueue = async (queueUrl: string): Promise<void> => {
  await awsJson10("sqs", "AmazonSQS.PurgeQueue", { QueueUrl: queueUrl });
};

export const setSQSQueueAttributes = async (
  queueUrl: string,
  attributes: Record<string, string>,
): Promise<void> => {
  await awsJson10("sqs", "AmazonSQS.SetQueueAttributes", { QueueUrl: queueUrl, Attributes: attributes });
};

// ---------------------------------------------------------------------------
// Amazon EventBridge — awsjson1.1, X-Amz-Target AWSEvents.<Op>.
// ---------------------------------------------------------------------------

export interface EventBus {
  name: string;
  arn: string;
  description: string;
}

export const fetchEventBuses = async (): Promise<EventBus[]> => {
  const listed = await awsJson<{ EventBuses?: { Name?: string; Arn?: string; Description?: string }[] }>(
    "events",
    "AWSEvents.ListEventBuses",
    {},
  );
  return (listed.EventBuses ?? []).map((bus) => ({
    name: bus.Name ?? "",
    arn: bus.Arn ?? "",
    description: bus.Description ?? "",
  }));
};

export interface EventBridgeRule {
  name: string;
  arn: string;
  eventBusName: string;
  state: string;
  scheduleExpression: string;
  description: string;
}

export const fetchEventBridgeRules = async (): Promise<EventBridgeRule[]> => {
  const buses = await fetchEventBuses();
  const lists = await Promise.all(buses.map(async (bus) => {
    const listed = await awsJson<{
      Rules?: {
        Name?: string;
        Arn?: string;
        EventBusName?: string;
        State?: string;
        ScheduleExpression?: string;
        Description?: string;
      }[];
    }>("events", "AWSEvents.ListRules", { EventBusName: bus.name });
    return (listed.Rules ?? []).map((rule) => ({
      name: rule.Name ?? "",
      arn: rule.Arn ?? "",
      eventBusName: rule.EventBusName ?? bus.name,
      state: rule.State ?? "",
      scheduleExpression: rule.ScheduleExpression ?? "",
      description: rule.Description ?? "",
    }));
  }));
  return lists.flat();
};

// EnableRule and DisableRule are the two lifecycle actions the real console's
// Rules table offers alongside Delete.
export const setEventBridgeRuleState = async (name: string, enabled: boolean, eventBusName = "default"): Promise<void> => {
  await awsJson("events", `AWSEvents.${enabled ? "EnableRule" : "DisableRule"}`, { Name: name, EventBusName: eventBusName });
};

export const deleteEventBridgeRule = async (name: string, eventBusName = "default"): Promise<void> => {
  await awsJson("events", "AWSEvents.DeleteRule", { Name: name, EventBusName: eventBusName });
};

export const putEventBridgeRule = async (input: {
  name: string;
  eventBusName?: string;
  description?: string;
  eventPattern?: string;
  scheduleExpression?: string;
  state?: string;
}): Promise<string> => {
  const response = await awsJson<{ RuleArn?: string }>("events", "AWSEvents.PutRule", {
    Name: input.name,
    EventBusName: input.eventBusName || "default",
    Description: input.description,
    EventPattern: input.eventPattern || undefined,
    ScheduleExpression: input.scheduleExpression || undefined,
    State: input.state || "ENABLED",
  });
  return response.RuleArn ?? "";
};

export interface EventBridgeTarget {
  id: string;
  arn: string;
  roleArn: string;
  input: string;
  inputPath: string;
}

export const fetchEventBridgeTargets = async (rule: string, eventBusName = "default"): Promise<EventBridgeTarget[]> => {
  const response = await awsJson<{
    Targets?: { Id?: string; Arn?: string; RoleArn?: string; Input?: string; InputPath?: string }[];
  }>("events", "AWSEvents.ListTargetsByRule", { Rule: rule, EventBusName: eventBusName });
  return (response.Targets ?? []).map((target) => ({
    id: target.Id ?? "",
    arn: target.Arn ?? "",
    roleArn: target.RoleArn ?? "",
    input: target.Input ?? "",
    inputPath: target.InputPath ?? "",
  }));
};

export const putEventBridgeTarget = async (
  rule: string,
  target: EventBridgeTarget,
  eventBusName = "default",
): Promise<void> => {
  await awsJson("events", "AWSEvents.PutTargets", {
    Rule: rule,
    EventBusName: eventBusName,
    Targets: [{ Id: target.id, Arn: target.arn, RoleArn: target.roleArn || undefined, Input: target.input || undefined }],
  });
};

export const removeEventBridgeTarget = async (
  rule: string,
  targetId: string,
  eventBusName = "default",
): Promise<void> => {
  await awsJson("events", "AWSEvents.RemoveTargets", { Rule: rule, EventBusName: eventBusName, Ids: [targetId] });
};

export const putEventBridgeEvent = async (input: {
  source: string;
  detailType: string;
  detail: string;
  eventBusName?: string;
}): Promise<string> => {
  const response = await awsJson<{ FailedEntryCount?: number; Entries?: { EventId?: string; ErrorMessage?: string }[] }>(
    "events",
    "AWSEvents.PutEvents",
    {
      Entries: [{
        Source: input.source,
        DetailType: input.detailType,
        Detail: input.detail,
        EventBusName: input.eventBusName || "default",
      }],
    },
  );
  const entry = response.Entries?.[0];
  if (response.FailedEntryCount || entry?.ErrorMessage) throw new Error(entry?.ErrorMessage || "EventBridge rejected the event");
  return entry?.EventId ?? "";
};

export const createEventBus = async (name: string, description = ""): Promise<void> => {
  await awsJson("events", "AWSEvents.CreateEventBus", { Name: name, Description: description || undefined });
};

export const deleteEventBus = async (name: string): Promise<void> => {
  await awsJson("events", "AWSEvents.DeleteEventBus", { Name: name });
};

// ---------------------------------------------------------------------------
// Amazon EventBridge Scheduler — the REST-JSON protocol (GET /schedules).
// ---------------------------------------------------------------------------

export interface Schedule {
  name: string;
  arn: string;
  groupName: string;
  state: string;
  targetArn: string;
  creationDate: string;
  lastModificationDate: string;
}

export const fetchSchedules = async (): Promise<Schedule[]> => {
  const listed = await awsRestJson<{
    Schedules?: {
      Name?: string;
      Arn?: string;
      GroupName?: string;
      State?: string;
      Target?: { Arn?: string };
      CreationDate?: string;
      LastModificationDate?: string;
    }[];
  }>("scheduler", "/schedules");
  return (listed.Schedules ?? []).map((schedule) => ({
    name: schedule.Name ?? "",
    arn: schedule.Arn ?? "",
    groupName: schedule.GroupName ?? "",
    state: schedule.State ?? "",
    targetArn: schedule.Target?.Arn ?? "",
    creationDate: schedule.CreationDate ?? "",
    lastModificationDate: schedule.LastModificationDate ?? "",
  }));
};

export interface ScheduleGroup {
  name: string;
  arn: string;
  state: string;
  creationDate: string;
}

export const fetchScheduleGroups = async (): Promise<ScheduleGroup[]> => {
  const listed = await awsRestJson<{
    ScheduleGroups?: { Name?: string; Arn?: string; State?: string; CreationDate?: string }[];
  }>("scheduler", "/schedule-groups");
  return (listed.ScheduleGroups ?? []).map((group) => ({
    name: group.Name ?? "",
    arn: group.Arn ?? "",
    state: group.State ?? "",
    creationDate: group.CreationDate ?? "",
  }));
};

export const createSchedule = async (input: {
  name: string;
  groupName: string;
  expression: string;
  targetArn: string;
  roleArn: string;
  targetInput: string;
}): Promise<void> => {
  await restJson("scheduler", "POST", `/schedules/${encodeURIComponent(input.name)}?groupName=${encodeURIComponent(input.groupName || "default")}`, {
    ScheduleExpression: input.expression,
    State: "ENABLED",
    FlexibleTimeWindow: { Mode: "OFF" },
    Target: { Arn: input.targetArn, RoleArn: input.roleArn, Input: input.targetInput || "{}" },
  });
};

export const updateScheduleState = async (schedule: Schedule, state: "ENABLED" | "DISABLED"): Promise<void> => {
  const current = await awsRestJson<Record<string, unknown>>(
    "scheduler",
    `/schedules/${encodeURIComponent(schedule.name)}?groupName=${encodeURIComponent(schedule.groupName || "default")}`,
  );
  const { Arn: _arn, CreationDate: _created, LastModificationDate: _modified, Name: _name, GroupName: _group, ...mutable } = current;
  await restJson(
    "scheduler",
    "PUT",
    `/schedules/${encodeURIComponent(schedule.name)}?groupName=${encodeURIComponent(schedule.groupName || "default")}`,
    { ...mutable, State: state },
  );
};

export const deleteSchedule = async (schedule: Schedule): Promise<void> => {
  await restJson(
    "scheduler",
    "DELETE",
    `/schedules/${encodeURIComponent(schedule.name)}?groupName=${encodeURIComponent(schedule.groupName || "default")}`,
  );
};

export const createScheduleGroup = async (name: string): Promise<void> => {
  await restJson("scheduler", "POST", `/schedule-groups/${encodeURIComponent(name)}`, {});
};

export const deleteScheduleGroup = async (name: string): Promise<void> => {
  await restJson("scheduler", "DELETE", `/schedule-groups/${encodeURIComponent(name)}`);
};

// ---------------------------------------------------------------------------
// AWS Step Functions — awsjson1.0, X-Amz-Target AWSStepFunctions.<Op>.
// ---------------------------------------------------------------------------

export interface StateMachine {
  stateMachineArn: string;
  name: string;
  type: string;
  creationDate: number;
}

export const fetchStateMachines = async (): Promise<StateMachine[]> => {
  const listed = await awsJson10<{
    stateMachines?: { stateMachineArn?: string; name?: string; type?: string; creationDate?: number }[];
  }>("states", "AWSStepFunctions.ListStateMachines", {});
  return (listed.stateMachines ?? []).map((machine) => ({
    stateMachineArn: machine.stateMachineArn ?? "",
    name: machine.name ?? "",
    type: machine.type ?? "",
    creationDate: machine.creationDate ?? 0,
  }));
};

export interface CreateStateMachineInput {
  name: string;
  definition: string;
  roleArn: string;
  type: "STANDARD" | "EXPRESS";
  tags?: { key: string; value: string }[];
}

export const createStateMachine = async (input: CreateStateMachineInput): Promise<string> => {
  const created = await awsJson10<{ stateMachineArn?: string }>(
    "states",
    "AWSStepFunctions.CreateStateMachine",
    {
      name: input.name,
      definition: input.definition,
      roleArn: input.roleArn,
      type: input.type,
      tags: input.tags ?? [],
    },
  );
  return created.stateMachineArn ?? "";
};

export interface StateMachineDetail extends StateMachine {
  status: string;
  roleArn: string;
  definition: string;
}

export const fetchStateMachine = async (stateMachineArn: string): Promise<StateMachineDetail> => {
  const described = await awsJson10<{
    stateMachineArn?: string;
    name?: string;
    type?: string;
    status?: string;
    roleArn?: string;
    definition?: string;
    creationDate?: number;
  }>("states", "AWSStepFunctions.DescribeStateMachine", { stateMachineArn });
  return {
    stateMachineArn: described.stateMachineArn ?? stateMachineArn,
    name: described.name ?? "",
    type: described.type ?? "",
    status: described.status ?? "",
    roleArn: described.roleArn ?? "",
    definition: described.definition ?? "",
    creationDate: described.creationDate ?? 0,
  };
};

export interface StateMachineExecution {
  executionArn: string;
  name: string;
  status: string;
  startDate: number;
  stopDate: number;
}

export const fetchStateMachineExecutions = async (stateMachineArn: string): Promise<StateMachineExecution[]> => {
  const listed = await awsJson10<{
    executions?: { executionArn?: string; name?: string; status?: string; startDate?: number; stopDate?: number }[];
  }>("states", "AWSStepFunctions.ListExecutions", { stateMachineArn });
  return (listed.executions ?? []).map((execution) => ({
    executionArn: execution.executionArn ?? "",
    name: execution.name ?? "",
    status: execution.status ?? "",
    startDate: execution.startDate ?? 0,
    stopDate: execution.stopDate ?? 0,
  }));
};

// StartExecution with no input is what the real console's "Start execution"
// dialog sends when the operator leaves the input at its `{}` default.
export const startStateMachineExecution = async (
  stateMachineArn: string,
  input: string,
  name?: string,
): Promise<string> => {
  const started = await awsJson10<{ executionArn?: string }>(
    "states",
    "AWSStepFunctions.StartExecution",
    { stateMachineArn, input, ...(name ? { name } : {}) },
  );
  return started.executionArn ?? "";
};

export interface StateMachineExecutionDetail extends StateMachineExecution {
  stateMachineArn: string;
  input: string;
  output: string;
  error: string;
  cause: string;
}

export interface StateMachineHistoryEvent {
  id: number;
  previousEventId: number;
  timestamp: number;
  type: string;
  details: Record<string, unknown>;
}

export const fetchStateMachineExecution = async (executionArn: string): Promise<StateMachineExecutionDetail> => {
  const execution = await awsJson10<{
    executionArn?: string;
    stateMachineArn?: string;
    name?: string;
    status?: string;
    startDate?: number;
    stopDate?: number;
    input?: string;
    output?: string;
    error?: string;
    cause?: string;
  }>("states", "AWSStepFunctions.DescribeExecution", { executionArn });
  return {
    executionArn: execution.executionArn ?? executionArn,
    stateMachineArn: execution.stateMachineArn ?? "",
    name: execution.name ?? "",
    status: execution.status ?? "",
    startDate: execution.startDate ?? 0,
    stopDate: execution.stopDate ?? 0,
    input: execution.input ?? "",
    output: execution.output ?? "",
    error: execution.error ?? "",
    cause: execution.cause ?? "",
  };
};

export const fetchStateMachineExecutionHistory = async (
  executionArn: string,
): Promise<StateMachineHistoryEvent[]> => {
  const response = await awsJson10<{ events?: Record<string, unknown>[] }>(
    "states",
    "AWSStepFunctions.GetExecutionHistory",
    { executionArn, includeExecutionData: true },
  );
  return (response.events ?? []).map((event) => {
    const detailsEntry = Object.entries(event).find(([key]) => key.endsWith("EventDetails"));
    return {
      id: Number(event.id ?? 0),
      previousEventId: Number(event.previousEventId ?? 0),
      timestamp: Number(event.timestamp ?? 0),
      type: String(event.type ?? ""),
      details: (detailsEntry?.[1] as Record<string, unknown> | undefined) ?? {},
    };
  });
};

export const stopStateMachineExecution = async (executionArn: string): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.StopExecution", {
    executionArn,
    error: "OperatorStopped",
    cause: "Stopped from the AWS Step Functions console.",
  });
};

export const redriveStateMachineExecution = async (executionArn: string): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.RedriveExecution", { executionArn });
};

export interface StateMachineVersion {
  stateMachineVersionArn: string;
  version: number;
  creationDate: number;
}

export const fetchStateMachineVersions = async (stateMachineArn: string): Promise<StateMachineVersion[]> => {
  const response = await awsJson10<{
    stateMachineVersions?: { stateMachineVersionArn?: string; creationDate?: number }[];
  }>("states", "AWSStepFunctions.ListStateMachineVersions", { stateMachineArn });
  return (response.stateMachineVersions ?? []).map((version) => {
    const stateMachineVersionArn = version.stateMachineVersionArn ?? "";
    const parsedVersion = Number(stateMachineVersionArn.slice(stateMachineVersionArn.lastIndexOf(":") + 1));
    return {
      stateMachineVersionArn,
      version: Number.isFinite(parsedVersion) ? parsedVersion : 0,
      creationDate: version.creationDate ?? 0,
    };
  });
};

export interface StateMachineAlias {
  stateMachineAliasArn: string;
  name: string;
  creationDate: number;
  updateDate: number;
  description: string;
  routingConfiguration: { stateMachineVersionArn: string; weight: number }[];
}

export const fetchStateMachineAliases = async (stateMachineArn: string): Promise<StateMachineAlias[]> => {
  const response = await awsJson10<{
    stateMachineAliases?: {
      stateMachineAliasArn?: string;
      creationDate?: number;
    }[];
  }>("states", "AWSStepFunctions.ListStateMachineAliases", { stateMachineArn });
  return Promise.all((response.stateMachineAliases ?? []).map(async (alias) => {
    const stateMachineAliasArn = alias.stateMachineAliasArn ?? "";
    const described = await awsJson10<{
      name?: string;
      updateDate?: number;
      description?: string;
      routingConfiguration?: { stateMachineVersionArn?: string; weight?: number }[];
    }>(
      "states",
      "AWSStepFunctions.DescribeStateMachineAlias",
      { stateMachineAliasArn },
    );
    return {
      stateMachineAliasArn,
      name: described.name ?? stateMachineAliasArn.slice(stateMachineAliasArn.lastIndexOf(":") + 1),
      creationDate: alias.creationDate ?? 0,
      updateDate: described.updateDate ?? 0,
      description: described.description ?? "",
      routingConfiguration: (described.routingConfiguration ?? []).map((route) => ({
        stateMachineVersionArn: route.stateMachineVersionArn ?? "",
        weight: route.weight ?? 0,
      })),
    };
  }));
};

export const fetchStateMachineTags = async (
  resourceArn: string,
): Promise<Record<string, string>> => {
  const response = await awsJson10<{ tags?: { key?: string; value?: string }[] }>(
    "states",
    "AWSStepFunctions.ListTagsForResource",
    { resourceArn },
  );
  return Object.fromEntries((response.tags ?? []).map((tag) => [tag.key ?? "", tag.value ?? ""]));
};

export const updateStateMachine = async (
  stateMachineArn: string,
  definition: string,
  roleArn: string,
): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.UpdateStateMachine", {
    stateMachineArn,
    definition,
    roleArn,
  });
};

export const publishStateMachineVersion = async (
  stateMachineArn: string,
  description: string,
): Promise<string> => {
  const response = await awsJson10<{ stateMachineVersionArn?: string }>(
    "states",
    "AWSStepFunctions.PublishStateMachineVersion",
    { stateMachineArn, description },
  );
  return response.stateMachineVersionArn ?? "";
};

export const createStateMachineAlias = async (
  name: string,
  versionArn: string,
  description: string,
): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.CreateStateMachineAlias", {
    name,
    description,
    routingConfiguration: [{ stateMachineVersionArn: versionArn, weight: 100 }],
  });
};

export const updateStateMachineAlias = async (
  stateMachineAliasArn: string,
  description: string,
  routingConfiguration: { stateMachineVersionArn: string; weight: number }[],
): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.UpdateStateMachineAlias", {
    stateMachineAliasArn,
    description,
    routingConfiguration,
  });
};

export const deleteStateMachineAlias = async (stateMachineAliasArn: string): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.DeleteStateMachineAlias", { stateMachineAliasArn });
};

export const deleteStateMachineVersion = async (stateMachineVersionArn: string): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.DeleteStateMachineVersion", { stateMachineVersionArn });
};

export interface StateMachineDefinitionValidation {
  result: "OK" | "FAIL";
  diagnostics: { message: string }[];
}

export const validateStateMachineDefinition = async (
  definition: string,
): Promise<StateMachineDefinitionValidation> =>
  awsJson10<StateMachineDefinitionValidation>(
    "states",
    "AWSStepFunctions.ValidateStateMachineDefinition",
    { definition },
  );

export interface StateMachineTestResult {
  status: string;
  output: string;
  error: string;
  cause: string;
  nextState: string;
}

export const testStateMachineState = async (
  definition: string,
  input: string,
  stateName: string,
): Promise<StateMachineTestResult> => {
  const response = await awsJson10<Partial<StateMachineTestResult>>(
    "states",
    "AWSStepFunctions.TestState",
    { definition, input, ...(stateName ? { stateName } : {}) },
  );
  return {
    status: response.status ?? "",
    output: response.output ?? "",
    error: response.error ?? "",
    cause: response.cause ?? "",
    nextState: response.nextState ?? "",
  };
};

export const tagStateMachineResource = async (
  resourceArn: string,
  tags: Record<string, string>,
): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.TagResource", {
    resourceArn,
    tags: Object.entries(tags).map(([key, value]) => ({ key, value })),
  });
};

export const untagStateMachineResource = async (resourceArn: string, tagKeys: string[]): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.UntagResource", { resourceArn, tagKeys });
};

export const deleteStateMachine = async (stateMachineArn: string): Promise<void> => {
  await awsJson10("states", "AWSStepFunctions.DeleteStateMachine", { stateMachineArn });
};

// ---------------------------------------------------------------------------
// Amazon CloudWatch — the Query protocol (Version=2010-08-01), the metrics and
// alarms API that sits beside the separate CloudWatch Logs API above.
// ---------------------------------------------------------------------------

const CLOUDWATCH_VERSION = "2010-08-01";

export interface CWAlarm {
  alarmName: string;
  alarmArn: string;
  stateValue: string;
  stateReason: string;
  metricName: string;
  namespace: string;
  statistic: string;
  comparisonOperator: string;
  threshold: number;
  stateUpdatedTimestamp: string;
}

export const fetchCWAlarms = async (): Promise<CWAlarm[]> => {
  const xml = await awsQuery("monitoring", CLOUDWATCH_VERSION, "DescribeAlarms");
  return xmlList(xml, "MetricAlarms").map((member) => ({
    alarmName: childText(member, "AlarmName"),
    alarmArn: childText(member, "AlarmArn"),
    stateValue: childText(member, "StateValue"),
    stateReason: childText(member, "StateReason"),
    metricName: childText(member, "MetricName"),
    namespace: childText(member, "Namespace"),
    statistic: childText(member, "Statistic"),
    comparisonOperator: childText(member, "ComparisonOperator"),
    threshold: Number(childText(member, "Threshold") || "0"),
    stateUpdatedTimestamp: childText(member, "StateUpdatedTimestamp"),
  }));
};

export const deleteCWAlarms = async (alarmNames: string[]): Promise<void> => {
  await awsQuery("monitoring", CLOUDWATCH_VERSION, "DeleteAlarms", xmlIndexedParams("AlarmNames.member", alarmNames));
};

export const putCWMetricData = async (namespace: string, metricName: string, value: number): Promise<void> => {
  await awsQuery("monitoring", CLOUDWATCH_VERSION, "PutMetricData", {
    Namespace: namespace,
    "MetricData.member.1.MetricName": metricName,
    "MetricData.member.1.Value": String(value),
  });
};

export const putCWMetricAlarm = async (input: {
  alarmName: string;
  namespace: string;
  metricName: string;
  threshold: number;
  alarmAction?: string;
}): Promise<void> => {
  await awsQuery("monitoring", CLOUDWATCH_VERSION, "PutMetricAlarm", {
    AlarmName: input.alarmName,
    Namespace: input.namespace,
    MetricName: input.metricName,
    Statistic: "Average",
    Period: "60",
    EvaluationPeriods: "1",
    DatapointsToAlarm: "1",
    Threshold: String(input.threshold),
    ComparisonOperator: "GreaterThanOrEqualToThreshold",
    ...(input.alarmAction ? { "AlarmActions.member.1": input.alarmAction, ActionsEnabled: "true" } : {}),
  });
};

export const setCWAlarmActions = async (alarmNames: string[], enabled: boolean): Promise<void> => {
  await awsQuery(
    "monitoring",
    CLOUDWATCH_VERSION,
    enabled ? "EnableAlarmActions" : "DisableAlarmActions",
    xmlIndexedParams("AlarmNames.member", alarmNames),
  );
};

export const putCWDashboard = async (name: string, body: string): Promise<void> => {
  await awsQuery("monitoring", CLOUDWATCH_VERSION, "PutDashboard", { DashboardName: name, DashboardBody: body });
};

export const deleteCWDashboards = async (names: string[]): Promise<void> => {
  await awsQuery("monitoring", CLOUDWATCH_VERSION, "DeleteDashboards", xmlIndexedParams("DashboardNames.member", names));
};

export interface CWDashboard {
  dashboardName: string;
  dashboardArn: string;
  lastModified: string;
  size: number;
}

export const fetchCWDashboards = async (): Promise<CWDashboard[]> => {
  const xml = await awsQuery("monitoring", CLOUDWATCH_VERSION, "ListDashboards");
  return xmlList(xml, "DashboardEntries").map((member) => ({
    dashboardName: childText(member, "DashboardName"),
    dashboardArn: childText(member, "DashboardArn"),
    lastModified: childText(member, "LastModified"),
    size: Number(childText(member, "Size") || "0"),
  }));
};

// ---------------------------------------------------------------------------
// AWS CloudTrail — awsjson1.1, X-Amz-Target CloudTrail_20131101.<Op>.
// ---------------------------------------------------------------------------

const CLOUDTRAIL_TARGET_PREFIX = "CloudTrail_20131101.";

export interface CloudTrailTrail {
  name: string;
  trailARN: string;
  s3BucketName: string;
  homeRegion: string;
  isMultiRegionTrail: boolean;
  includeGlobalServiceEvents: boolean;
  logFileValidationEnabled: boolean;
  isLogging: boolean;
}

export const fetchCloudTrailTrails = async (): Promise<CloudTrailTrail[]> => {
  const described = await awsJson<{
    trailList?: {
      Name?: string;
      TrailARN?: string;
      S3BucketName?: string;
      HomeRegion?: string;
      IsMultiRegionTrail?: boolean;
      IncludeGlobalServiceEvents?: boolean;
      LogFileValidationEnabled?: boolean;
    }[];
  }>("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}DescribeTrails`, {});
  return Promise.all((described.trailList ?? []).map(async (trail) => {
    const name = trail.Name ?? "";
    const status = await awsJson<{ IsLogging?: boolean }>(
      "cloudtrail",
      `${CLOUDTRAIL_TARGET_PREFIX}GetTrailStatus`,
      { Name: name },
    );
    return {
      name,
      trailARN: trail.TrailARN ?? "",
      s3BucketName: trail.S3BucketName ?? "",
      homeRegion: trail.HomeRegion ?? "",
      isMultiRegionTrail: trail.IsMultiRegionTrail ?? false,
      includeGlobalServiceEvents: trail.IncludeGlobalServiceEvents ?? false,
      logFileValidationEnabled: trail.LogFileValidationEnabled ?? false,
      isLogging: status.IsLogging ?? false,
    };
  }));
};

export interface CloudTrailEvent {
  eventId: string;
  eventName: string;
  eventSource: string;
  eventTime: number;
  username: string;
  readOnly: string;
  cloudTrailEvent: string;
}

// LookupEvents is the API behind the real console's Event history page.
export const fetchCloudTrailEvents = async (): Promise<CloudTrailEvent[]> => {
  const looked = await awsJson<{
    Events?: {
      EventId?: string;
      EventName?: string;
      EventSource?: string;
      EventTime?: number;
      Username?: string;
      ReadOnly?: string;
      CloudTrailEvent?: string;
    }[];
  }>("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}LookupEvents`, { MaxResults: 50 });
  return (looked.Events ?? []).map((event) => ({
    eventId: event.EventId ?? "",
    eventName: event.EventName ?? "",
    eventSource: event.EventSource ?? "",
    eventTime: event.EventTime ?? 0,
    username: event.Username ?? "",
    readOnly: event.ReadOnly ?? "",
    cloudTrailEvent: event.CloudTrailEvent ?? "",
  }));
};

export const createCloudTrailTrail = async (name: string, s3BucketName: string): Promise<void> => {
  await awsJson("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}CreateTrail`, {
    Name: name,
    S3BucketName: s3BucketName,
    IncludeGlobalServiceEvents: true,
    IsMultiRegionTrail: true,
    EnableLogFileValidation: true,
  });
};

export const startCloudTrailLogging = async (name: string): Promise<void> => {
  await awsJson("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}StartLogging`, { Name: name });
};

export const stopCloudTrailLogging = async (name: string): Promise<void> => {
  await awsJson("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}StopLogging`, { Name: name });
};

export const deleteCloudTrailTrail = async (name: string): Promise<void> => {
  await awsJson("cloudtrail", `${CLOUDTRAIL_TARGET_PREFIX}DeleteTrail`, { Name: name });
};

// ---------------------------------------------------------------------------
// Amazon Data Firehose — awsjson1.1, X-Amz-Target
// Firehose_20150804.<Op>. The browser uses the same control plane as the AWS
// SDK and AWS CLI; delivered records travel through the configured cloud
// destination rather than a console-only endpoint.
// ---------------------------------------------------------------------------

const FIREHOSE_TARGET_PREFIX = "Firehose_20150804.";

const firehoseJson = <T>(operation: string, input: Record<string, unknown> = {}): Promise<T> =>
  awsJson<T>("firehose", `${FIREHOSE_TARGET_PREFIX}${operation}`, input);

function base64UTF8(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

export interface FirehoseDeliveryStream {
  name: string;
  arn: string;
  status: string;
  type: string;
  versionId: string;
  createdAt: number;
  destinationId: string;
  bucketArn: string;
  prefix: string;
  compressionFormat: string;
  encryptionStatus: string;
}

export const fetchFirehoseDeliveryStreams = async (): Promise<FirehoseDeliveryStream[]> => {
  const listed = await firehoseJson<{ DeliveryStreamNames?: string[] }>("ListDeliveryStreams");
  return Promise.all((listed.DeliveryStreamNames ?? []).map(async (name) => {
    const described = await firehoseJson<{
      DeliveryStreamDescription?: {
        DeliveryStreamName?: string;
        DeliveryStreamARN?: string;
        DeliveryStreamStatus?: string;
        DeliveryStreamType?: string;
        VersionId?: string;
        CreateTimestamp?: number;
        DeliveryStreamEncryptionConfiguration?: { Status?: string };
        Destinations?: {
          DestinationId?: string;
          ExtendedS3DestinationDescription?: {
            BucketARN?: string;
            Prefix?: string;
            CompressionFormat?: string;
          };
        }[];
      };
    }>("DescribeDeliveryStream", { DeliveryStreamName: name });
    const stream = described.DeliveryStreamDescription ?? {};
    const destination = stream.Destinations?.[0] ?? {};
    const s3 = destination.ExtendedS3DestinationDescription ?? {};
    return {
      name: stream.DeliveryStreamName ?? name,
      arn: stream.DeliveryStreamARN ?? "",
      status: stream.DeliveryStreamStatus ?? "",
      type: stream.DeliveryStreamType ?? "",
      versionId: stream.VersionId ?? "",
      createdAt: stream.CreateTimestamp ?? 0,
      destinationId: destination.DestinationId ?? "",
      bucketArn: s3.BucketARN ?? "",
      prefix: s3.Prefix ?? "",
      compressionFormat: s3.CompressionFormat ?? "UNCOMPRESSED",
      encryptionStatus: stream.DeliveryStreamEncryptionConfiguration?.Status ?? "DISABLED",
    };
  }));
};

export const createFirehoseDeliveryStream = async (input: {
  name: string;
  bucketArn: string;
  roleArn: string;
  prefix: string;
  compressionFormat: "UNCOMPRESSED" | "GZIP" | "ZIP";
}): Promise<void> => {
  await firehoseJson("CreateDeliveryStream", {
    DeliveryStreamName: input.name,
    DeliveryStreamType: "DirectPut",
    ExtendedS3DestinationConfiguration: {
      BucketARN: input.bucketArn,
      RoleARN: input.roleArn,
      Prefix: input.prefix,
      CompressionFormat: input.compressionFormat,
      BufferingHints: { SizeInMBs: 1, IntervalInSeconds: 60 },
    },
  });
};

export const deleteFirehoseDeliveryStream = async (name: string): Promise<void> => {
  await firehoseJson("DeleteDeliveryStream", { DeliveryStreamName: name, AllowForceDelete: true });
};

export const setFirehoseEncryption = async (name: string, enabled: boolean): Promise<void> => {
  await firehoseJson(enabled ? "StartDeliveryStreamEncryption" : "StopDeliveryStreamEncryption", {
    DeliveryStreamName: name,
    ...(enabled
      ? { DeliveryStreamEncryptionConfigurationInput: { KeyType: "AWS_OWNED_CMK" } }
      : {}),
  });
};

export const putFirehoseRecord = async (name: string, data: string): Promise<string> => {
  const result = await firehoseJson<{ RecordId?: string }>("PutRecord", {
    DeliveryStreamName: name,
    Record: { Data: base64UTF8(data) },
  });
  return result.RecordId ?? "";
};

// ---------------------------------------------------------------------------
// AWS Private Certificate Authority — awsjson1.1, X-Amz-Target
// ACMPrivateCA.<Op>. Root activation deliberately follows AWS's public
// CSR → IssueCertificate → GetCertificate → ImportCertificate chain.
// ---------------------------------------------------------------------------

const PRIVATE_CA_TARGET_PREFIX = "ACMPrivateCA.";

const privateCAJson = <T>(operation: string, input: Record<string, unknown> = {}): Promise<T> =>
  awsJson<T>("acm-pca", `${PRIVATE_CA_TARGET_PREFIX}${operation}`, input);

export interface PrivateCertificateAuthority {
  arn: string;
  commonName: string;
  organization: string;
  type: string;
  status: string;
  keyAlgorithm: string;
  signingAlgorithm: string;
  createdAt: number;
  notAfter: number;
  usageMode: string;
}

export const fetchPrivateCertificateAuthorities = async (): Promise<PrivateCertificateAuthority[]> => {
  const listed = await privateCAJson<{
    CertificateAuthorities?: {
      Arn?: string;
      Type?: string;
      Status?: string;
      CreatedAt?: number;
      NotAfter?: number;
      UsageMode?: string;
      CertificateAuthorityConfiguration?: {
        KeyAlgorithm?: string;
        SigningAlgorithm?: string;
        Subject?: { CommonName?: string; Organization?: string };
      };
    }[];
  }>("ListCertificateAuthorities");
  return (listed.CertificateAuthorities ?? []).map((authority) => ({
    arn: authority.Arn ?? "",
    commonName: authority.CertificateAuthorityConfiguration?.Subject?.CommonName ?? "",
    organization: authority.CertificateAuthorityConfiguration?.Subject?.Organization ?? "",
    type: authority.Type ?? "",
    status: authority.Status ?? "",
    keyAlgorithm: authority.CertificateAuthorityConfiguration?.KeyAlgorithm ?? "",
    signingAlgorithm: authority.CertificateAuthorityConfiguration?.SigningAlgorithm ?? "",
    createdAt: authority.CreatedAt ?? 0,
    notAfter: authority.NotAfter ?? 0,
    usageMode: authority.UsageMode ?? "",
  }));
};

export const createAndActivateRootPrivateCA = async (input: {
  commonName: string;
  organization: string;
  keyAlgorithm: "RSA_2048" | "RSA_3072" | "RSA_4096";
  validityYears: number;
}): Promise<string> => {
  const signingAlgorithm = input.keyAlgorithm === "RSA_4096" ? "SHA512WITHRSA" : "SHA256WITHRSA";
  const created = await privateCAJson<{ CertificateAuthorityArn?: string }>("CreateCertificateAuthority", {
    CertificateAuthorityType: "ROOT",
    CertificateAuthorityConfiguration: {
      KeyAlgorithm: input.keyAlgorithm,
      SigningAlgorithm: signingAlgorithm,
      Subject: { CommonName: input.commonName, Organization: input.organization },
    },
    Tags: [{ Key: "ManagedBy", Value: "AWSConsole" }],
  });
  const arn = created.CertificateAuthorityArn ?? "";
  if (!arn) throw new Error("CreateCertificateAuthority returned no certificate authority ARN.");
  const csr = await privateCAJson<{ Csr?: string }>("GetCertificateAuthorityCsr", {
    CertificateAuthorityArn: arn,
  });
  if (!csr.Csr) throw new Error("GetCertificateAuthorityCsr returned no certificate signing request.");
  const issued = await privateCAJson<{ CertificateArn?: string }>("IssueCertificate", {
    CertificateAuthorityArn: arn,
    Csr: base64UTF8(csr.Csr),
    SigningAlgorithm: signingAlgorithm,
    TemplateArn: "arn:aws:acm-pca:::template/RootCACertificate/V1",
    Validity: { Type: "YEARS", Value: input.validityYears },
  });
  if (!issued.CertificateArn) throw new Error("IssueCertificate returned no certificate ARN.");
  const certificate = await privateCAJson<{ Certificate?: string; CertificateChain?: string }>("GetCertificate", {
    CertificateAuthorityArn: arn,
    CertificateArn: issued.CertificateArn,
  });
  if (!certificate.Certificate) throw new Error("GetCertificate returned no certificate.");
  await privateCAJson("ImportCertificateAuthorityCertificate", {
    CertificateAuthorityArn: arn,
    Certificate: base64UTF8(certificate.Certificate),
    ...(certificate.CertificateChain ? { CertificateChain: base64UTF8(certificate.CertificateChain) } : {}),
  });
  return arn;
};

export const setPrivateCertificateAuthorityEnabled = async (arn: string, enabled: boolean): Promise<void> => {
  await privateCAJson("UpdateCertificateAuthority", {
    CertificateAuthorityArn: arn,
    Status: enabled ? "ACTIVE" : "DISABLED",
  });
};

export const deletePrivateCertificateAuthority = async (arn: string): Promise<void> => {
  await privateCAJson("DeleteCertificateAuthority", {
    CertificateAuthorityArn: arn,
    PermanentDeletionTimeInDays: 7,
  });
};

export const restorePrivateCertificateAuthority = async (arn: string): Promise<void> => {
  await privateCAJson("RestoreCertificateAuthority", { CertificateAuthorityArn: arn });
};

// ---------------------------------------------------------------------------
// AWS Systems Manager — awsjson1.1, X-Amz-Target AmazonSSM.<Op>.
// ---------------------------------------------------------------------------

const SSM_TARGET_PREFIX = "AmazonSSM.";

const ssmJson = <T>(operation: string, input: Record<string, unknown> = {}): Promise<T> =>
  awsJson<T>("ssm", `${SSM_TARGET_PREFIX}${operation}`, input);

export interface SSMParameter {
  name: string;
  type: string;
  version: number;
  dataType: string;
  description: string;
  lastModifiedDate: number;
  tier: string;
}

export const fetchSSMParameters = async (): Promise<SSMParameter[]> => {
  const described = await ssmJson<{
    Parameters?: {
      Name?: string;
      Type?: string;
      Version?: number;
      DataType?: string;
      Description?: string;
      LastModifiedDate?: number;
      Tier?: string;
    }[];
  }>("DescribeParameters");
  return (described.Parameters ?? []).map((parameter) => ({
    name: parameter.Name ?? "",
    type: parameter.Type ?? "",
    version: parameter.Version ?? 0,
    dataType: parameter.DataType ?? "",
    description: parameter.Description ?? "",
    lastModifiedDate: parameter.LastModifiedDate ?? 0,
    tier: parameter.Tier ?? "",
  }));
};

// PutParameter with Overwrite left off is a create — real Systems Manager
// answers ParameterAlreadyExists when the name is taken, which is exactly what
// the real console's "Create parameter" form surfaces.
export const createSSMParameter = async (
  name: string,
  value: string,
  type: "String" | "StringList" | "SecureString",
): Promise<void> => {
  await ssmJson("PutParameter", { Name: name, Value: value, Type: type });
};

export const deleteSSMParameter = async (name: string): Promise<void> => {
  await ssmJson("DeleteParameter", { Name: name });
};

export interface SSMDocument {
  name: string;
  documentType: string;
  documentFormat: string;
  owner: string;
  documentVersion: string;
  platformTypes: string[];
}

export const fetchSSMDocuments = async (): Promise<SSMDocument[]> => {
  const listed = await ssmJson<{
    DocumentIdentifiers?: {
      Name?: string;
      DocumentType?: string;
      DocumentFormat?: string;
      Owner?: string;
      DocumentVersion?: string;
      PlatformTypes?: string[];
    }[];
  }>("ListDocuments");
  return (listed.DocumentIdentifiers ?? []).map((document) => ({
    name: document.Name ?? "",
    documentType: document.DocumentType ?? "",
    documentFormat: document.DocumentFormat ?? "",
    owner: document.Owner ?? "",
    documentVersion: document.DocumentVersion ?? "",
    platformTypes: document.PlatformTypes ?? [],
  }));
};

// ---------------------------------------------------------------------------
// AWS Secrets Manager — awsjson1.1, X-Amz-Target secretsmanager.<Op>.
// ---------------------------------------------------------------------------

export interface Secret {
  arn: string;
  name: string;
  description: string;
  rotationEnabled: boolean;
  createdDate: number;
  lastChangedDate: number;
  primaryRegion: string;
  replicationStatus: {
    region: string;
    kmsKeyId: string;
    status: string;
    statusMessage: string;
    lastAccessedDate: number;
  }[];
}

export const fetchSecrets = async (): Promise<Secret[]> => {
  const listed = await awsJson<{
    SecretList?: {
      ARN?: string;
      Name?: string;
      Description?: string;
      RotationEnabled?: boolean;
      CreatedDate?: number;
      LastChangedDate?: number;
      PrimaryRegion?: string;
    }[];
  }>("secretsmanager", "secretsmanager.ListSecrets", {});
  return Promise.all((listed.SecretList ?? []).map(async (secret) => {
    const described = await awsJson<{
      PrimaryRegion?: string;
      ReplicationStatus?: {
        Region?: string;
        KmsKeyId?: string;
        Status?: string;
        StatusMessage?: string;
        LastAccessedDate?: number;
      }[];
    }>("secretsmanager", "secretsmanager.DescribeSecret", { SecretId: secret.ARN ?? secret.Name ?? "" });
    return {
      arn: secret.ARN ?? "",
      name: secret.Name ?? "",
      description: secret.Description ?? "",
      rotationEnabled: secret.RotationEnabled ?? false,
      createdDate: secret.CreatedDate ?? 0,
      lastChangedDate: secret.LastChangedDate ?? 0,
      primaryRegion: described.PrimaryRegion ?? secret.PrimaryRegion ?? "",
      replicationStatus: (described.ReplicationStatus ?? []).map((replica) => ({
        region: replica.Region ?? "",
        kmsKeyId: replica.KmsKeyId ?? "",
        status: replica.Status ?? "",
        statusMessage: replica.StatusMessage ?? "",
        lastAccessedDate: replica.LastAccessedDate ?? 0,
      })),
    };
  }));
};

export const createSecret = async (name: string, secretString: string, description: string): Promise<void> => {
  const input: Record<string, unknown> = { Name: name, SecretString: secretString };
  if (description) input.Description = description;
  await awsJson("secretsmanager", "secretsmanager.CreateSecret", input);
};

// DeleteSecret without ForceDeleteWithoutRecovery schedules the deletion after
// the service's recovery window, which is the behaviour the real console's
// delete dialog describes and defaults to.
export const deleteSecret = async (secretId: string): Promise<void> => {
  await awsJson("secretsmanager", "secretsmanager.DeleteSecret", { SecretId: secretId });
};

export const replicateSecret = async (secretId: string, region: string): Promise<void> => {
  await awsJson("secretsmanager", "secretsmanager.ReplicateSecretToRegions", {
    SecretId: secretId,
    AddReplicaRegions: [{ Region: region }],
  });
};

export const removeSecretReplica = async (secretId: string, region: string): Promise<void> => {
  await awsJson("secretsmanager", "secretsmanager.RemoveRegionsFromReplication", {
    SecretId: secretId,
    RemoveReplicaRegions: [region],
  });
};

// ---------------------------------------------------------------------------
// AWS Key Management Service (KMS) — awsjson1.1, X-Amz-Target
// TrentService.<Op>.
// ---------------------------------------------------------------------------

export interface KMSKey {
  keyId: string;
  arn: string;
  keyState: string;
  keyUsage: string;
  keySpec: string;
  description: string;
  enabled: boolean;
  creationDate: number;
  aliases: string[];
}

// ListKeys answers ids; DescribeKey answers each key's metadata and ListAliases
// the display names — the three calls the real console's Customer managed keys
// page makes.
export const fetchKMSKeys = async (): Promise<KMSKey[]> => {
  const [listed, aliased] = await Promise.all([
    awsJson<{ Keys?: { KeyId?: string; KeyArn?: string }[] }>("kms", "TrentService.ListKeys", {}),
    awsJson<{ Aliases?: { AliasName?: string; TargetKeyId?: string }[] }>("kms", "TrentService.ListAliases", {}),
  ]);
  const aliasesByKey = new Map<string, string[]>();
  for (const alias of aliased.Aliases ?? []) {
    if (!alias.TargetKeyId || !alias.AliasName) continue;
    aliasesByKey.set(alias.TargetKeyId, [...(aliasesByKey.get(alias.TargetKeyId) ?? []), alias.AliasName]);
  }
  const keys: KMSKey[] = [];
  for (const entry of listed.Keys ?? []) {
    const keyId = entry.KeyId ?? "";
    const described = await awsJson<{
      KeyMetadata?: {
        KeyId?: string;
        Arn?: string;
        KeyState?: string;
        KeyUsage?: string;
        KeySpec?: string;
        Description?: string;
        Enabled?: boolean;
        CreationDate?: number;
      };
    }>("kms", "TrentService.DescribeKey", { KeyId: keyId });
    const metadata = described.KeyMetadata ?? {};
    keys.push({
      keyId: metadata.KeyId ?? keyId,
      arn: metadata.Arn ?? entry.KeyArn ?? "",
      keyState: metadata.KeyState ?? "",
      keyUsage: metadata.KeyUsage ?? "",
      keySpec: metadata.KeySpec ?? "",
      description: metadata.Description ?? "",
      enabled: metadata.Enabled ?? false,
      creationDate: metadata.CreationDate ?? 0,
      aliases: aliasesByKey.get(keyId) ?? [],
    });
  }
  return keys;
};

export const createKMSKey = async (description: string): Promise<void> => {
  await awsJson("kms", "TrentService.CreateKey", { Description: description });
};

// Real KMS never deletes a key immediately: ScheduleKeyDeletion sets a waiting
// period, and the real console's delete dialog says so.
export const scheduleKMSKeyDeletion = async (keyId: string, pendingWindowInDays: number): Promise<void> => {
  await awsJson("kms", "TrentService.ScheduleKeyDeletion", { KeyId: keyId, PendingWindowInDays: pendingWindowInDays });
};

export const setKMSKeyEnabled = async (keyId: string, enabled: boolean): Promise<void> => {
  await awsJson("kms", `TrentService.${enabled ? "EnableKey" : "DisableKey"}`, { KeyId: keyId });
};

// ---------------------------------------------------------------------------
// AWS Certificate Manager — awsjson1.1, X-Amz-Target CertificateManager.<Op>.
// ---------------------------------------------------------------------------

export interface ACMCertificate {
  certificateArn: string;
  domainName: string;
  status: string;
  type: string;
  keyAlgorithm: string;
  inUseBy: string[];
  notAfter: number;
}

export const fetchACMCertificates = async (): Promise<ACMCertificate[]> => {
  const listed = await awsJson<{
    CertificateSummaryList?: {
      CertificateArn?: string;
      DomainName?: string;
      Status?: string;
      Type?: string;
      KeyAlgorithm?: string;
      InUse?: boolean;
      NotAfter?: number;
      InUseBy?: string[];
    }[];
  }>("acm", "CertificateManager.ListCertificates", {});
  return (listed.CertificateSummaryList ?? []).map((certificate) => ({
    certificateArn: certificate.CertificateArn ?? "",
    domainName: certificate.DomainName ?? "",
    status: certificate.Status ?? "",
    type: certificate.Type ?? "",
    keyAlgorithm: certificate.KeyAlgorithm ?? "",
    inUseBy: certificate.InUseBy ?? [],
    notAfter: certificate.NotAfter ?? 0,
  }));
};

export const deleteACMCertificate = async (certificateArn: string): Promise<void> => {
  await awsJson("acm", "CertificateManager.DeleteCertificate", { CertificateArn: certificateArn });
};

export interface ACMAcmeEndpoint {
  acmeEndpointArn: string;
  endpointUrl: string;
  status: string;
  contact: string;
  authorizationBehavior: string;
  createdAt: number;
}

export interface ACMAcmeDomainValidation {
  acmeDomainValidationArn: string;
  domainName: string;
  status: string;
  createdAt: number;
  recordName: string;
  recordValue: string;
}

export interface ACMAcmeExternalAccountBinding {
  acmeExternalAccountBindingArn: string;
  roleArn: string;
  createdAt: number;
  expiresAt: number;
  revokedAt: number;
}

export interface ACMAcmeAccount {
  accountUrl: string;
  contacts: string[];
  status: string;
  publicKeyThumbprint: string;
  createdAt: number;
}

export const fetchACMAcmeEndpoints = async (): Promise<ACMAcmeEndpoint[]> => {
  const listed = await awsJson<{
    AcmeEndpoints?: {
      AcmeEndpointArn?: string;
      EndpointUrl?: string;
      Status?: string;
      Contact?: string;
      AuthorizationBehavior?: string;
      CreatedAt?: number;
    }[];
  }>("acm", "CertificateManager.ListAcmeEndpoints", {});
  return (listed.AcmeEndpoints ?? []).map((endpoint) => ({
    acmeEndpointArn: endpoint.AcmeEndpointArn ?? "",
    endpointUrl: endpoint.EndpointUrl ?? "",
    status: endpoint.Status ?? "",
    contact: endpoint.Contact ?? "",
    authorizationBehavior: endpoint.AuthorizationBehavior ?? "",
    createdAt: endpoint.CreatedAt ?? 0,
  }));
};

export const createACMAcmeEndpoint = async (contact: "REQUIRED" | "NOT_REQUIRED"): Promise<void> => {
  await awsJson("acm", "CertificateManager.CreateAcmeEndpoint", {
    AuthorizationBehavior: "PRE_APPROVED",
    CertificateAuthority: {
      PublicCertificateAuthority: { AllowedKeyAlgorithms: ["RSA_2048", "EC_prime256v1", "EC_secp384r1"] },
    },
    Contact: contact,
  });
};

export const deleteACMAcmeEndpoint = async (acmeEndpointArn: string): Promise<void> => {
  await awsJson("acm", "CertificateManager.DeleteAcmeEndpoint", { AcmeEndpointArn: acmeEndpointArn });
};

export const fetchACMAcmeDomainValidations = async (
  acmeEndpointArn: string,
): Promise<ACMAcmeDomainValidation[]> => {
  const listed = await awsJson<{
    AcmeDomainValidations?: {
      AcmeDomainValidationArn?: string;
      DomainName?: string;
      Status?: string;
      CreatedAt?: number;
      PrevalidationDetails?: {
        DnsPrevalidation?: { ResourceRecord?: { Name?: string; Value?: string } };
      };
    }[];
  }>("acm", "CertificateManager.ListAcmeDomainValidations", { AcmeEndpointArn: acmeEndpointArn });
  return (listed.AcmeDomainValidations ?? []).map((validation) => ({
    acmeDomainValidationArn: validation.AcmeDomainValidationArn ?? "",
    domainName: validation.DomainName ?? "",
    status: validation.Status ?? "",
    createdAt: validation.CreatedAt ?? 0,
    recordName: validation.PrevalidationDetails?.DnsPrevalidation?.ResourceRecord?.Name ?? "",
    recordValue: validation.PrevalidationDetails?.DnsPrevalidation?.ResourceRecord?.Value ?? "",
  }));
};

export const createACMAcmeDomainValidation = async (
  acmeEndpointArn: string,
  domainName: string,
  hostedZoneId: string,
): Promise<void> => {
  await awsJson("acm", "CertificateManager.CreateAcmeDomainValidation", {
    AcmeEndpointArn: acmeEndpointArn,
    DomainName: domainName,
    PrevalidationOptions: {
      DnsPrevalidation: {
        DomainScope: { ExactDomain: "ENABLED", Subdomains: "ENABLED", Wildcards: "ENABLED" },
        ...(hostedZoneId ? { HostedZoneId: hostedZoneId } : {}),
      },
    },
  });
};

export const deleteACMAcmeDomainValidation = async (acmeDomainValidationArn: string): Promise<void> => {
  await awsJson("acm", "CertificateManager.DeleteAcmeDomainValidation", {
    AcmeDomainValidationArn: acmeDomainValidationArn,
  });
};

export const fetchACMAcmeExternalAccountBindings = async (
  acmeEndpointArn: string,
): Promise<ACMAcmeExternalAccountBinding[]> => {
  const listed = await awsJson<{
    ExternalAccountBindings?: {
      AcmeExternalAccountBindingArn?: string;
      RoleArn?: string;
      CreatedAt?: number;
      ExpiresAt?: number;
      RevokedAt?: number;
    }[];
  }>("acm", "CertificateManager.ListAcmeExternalAccountBindings", { AcmeEndpointArn: acmeEndpointArn });
  return (listed.ExternalAccountBindings ?? []).map((binding) => ({
    acmeExternalAccountBindingArn: binding.AcmeExternalAccountBindingArn ?? "",
    roleArn: binding.RoleArn ?? "",
    createdAt: binding.CreatedAt ?? 0,
    expiresAt: binding.ExpiresAt ?? 0,
    revokedAt: binding.RevokedAt ?? 0,
  }));
};

export const createACMAcmeExternalAccountBinding = async (
  acmeEndpointArn: string,
  roleArn: string,
): Promise<ACMAcmeExternalAccountBinding> => {
  const created = await awsJson<{
    ExternalAccountBinding?: {
      AcmeExternalAccountBindingArn?: string;
      RoleArn?: string;
      CreatedAt?: number;
      ExpiresAt?: number;
      RevokedAt?: number;
    };
  }>("acm", "CertificateManager.CreateAcmeExternalAccountBinding", {
    AcmeEndpointArn: acmeEndpointArn,
    RoleArn: roleArn,
    Expiration: { Type: "DAYS", Value: 7 },
  });
  const binding = created.ExternalAccountBinding ?? {};
  return {
    acmeExternalAccountBindingArn: binding.AcmeExternalAccountBindingArn ?? "",
    roleArn: binding.RoleArn ?? "",
    createdAt: binding.CreatedAt ?? 0,
    expiresAt: binding.ExpiresAt ?? 0,
    revokedAt: binding.RevokedAt ?? 0,
  };
};

export const getACMAcmeExternalAccountBindingCredentials = async (
  acmeExternalAccountBindingArn: string,
): Promise<{ keyId: string; macKey: string }> => {
  const credentials = await awsJson<{ KeyId?: string; MacKey?: string }>(
    "acm",
    "CertificateManager.GetAcmeExternalAccountBindingCredentials",
    { AcmeExternalAccountBindingArn: acmeExternalAccountBindingArn },
  );
  return { keyId: credentials.KeyId ?? "", macKey: credentials.MacKey ?? "" };
};

export const revokeACMAcmeExternalAccountBinding = async (
  acmeExternalAccountBindingArn: string,
): Promise<void> => {
  await awsJson("acm", "CertificateManager.RevokeAcmeExternalAccountBinding", {
    AcmeExternalAccountBindingArn: acmeExternalAccountBindingArn,
  });
};

export const fetchACMAcmeAccounts = async (acmeEndpointArn: string): Promise<ACMAcmeAccount[]> => {
  const listed = await awsJson<{
    AcmeAccounts?: {
      AccountUrl?: string;
      Contacts?: string[];
      Status?: string;
      PublicKeyThumbprint?: string;
      CreatedAt?: number;
    }[];
  }>("acm", "CertificateManager.ListAcmeAccounts", { AcmeEndpointArn: acmeEndpointArn });
  return (listed.AcmeAccounts ?? []).map((account) => ({
    accountUrl: account.AccountUrl ?? "",
    contacts: account.Contacts ?? [],
    status: account.Status ?? "",
    publicKeyThumbprint: account.PublicKeyThumbprint ?? "",
    createdAt: account.CreatedAt ?? 0,
  }));
};

export const revokeACMAcmeAccount = async (acmeEndpointArn: string, accountUrl: string): Promise<void> => {
  await awsJson("acm", "CertificateManager.RevokeAcmeAccount", {
    AcmeEndpointArn: acmeEndpointArn,
    AccountUrl: accountUrl,
  });
};

// ---------------------------------------------------------------------------
// AWS WAF — awsjson1.1, X-Amz-Target AWSWAF_20190729.<Op>. Every WAF read takes
// a Scope: REGIONAL resources and CloudFront distributions are separate
// namespaces, which is why the real console makes an operator choose one.
// ---------------------------------------------------------------------------

export type WAFScope = "REGIONAL" | "CLOUDFRONT";

export interface WAFWebACL {
  id: string;
  name: string;
  arn: string;
  description: string;
  lockToken: string;
}

export const fetchWAFWebACLs = async (scope: WAFScope): Promise<WAFWebACL[]> => {
  const listed = await awsJson<{
    WebACLs?: { Id?: string; Name?: string; ARN?: string; Description?: string; LockToken?: string }[];
  }>("wafv2", "AWSWAF_20190729.ListWebACLs", { Scope: scope });
  return (listed.WebACLs ?? []).map((acl) => ({
    id: acl.Id ?? "",
    name: acl.Name ?? "",
    arn: acl.ARN ?? "",
    description: acl.Description ?? "",
    lockToken: acl.LockToken ?? "",
  }));
};

export interface WAFIPSet {
  id: string;
  name: string;
  arn: string;
  description: string;
  lockToken: string;
}

export const fetchWAFIPSets = async (scope: WAFScope): Promise<WAFIPSet[]> => {
  const listed = await awsJson<{
    IPSets?: { Id?: string; Name?: string; ARN?: string; Description?: string; LockToken?: string }[];
  }>("wafv2", "AWSWAF_20190729.ListIPSets", { Scope: scope });
  return (listed.IPSets ?? []).map((set) => ({
    id: set.Id ?? "",
    name: set.Name ?? "",
    arn: set.ARN ?? "",
    description: set.Description ?? "",
    lockToken: set.LockToken ?? "",
  }));
};

// DeleteWebACL takes the LockToken the list read returned — WAF's optimistic
// concurrency control, which the real console carries through the same way.
export const deleteWAFWebACL = async (acl: WAFWebACL, scope: WAFScope): Promise<void> => {
  await awsJson("wafv2", "AWSWAF_20190729.DeleteWebACL", {
    Name: acl.name,
    Id: acl.id,
    Scope: scope,
    LockToken: acl.lockToken,
  });
};

// ---------------------------------------------------------------------------
// AWS Security Token Service (STS) and AWS Budgets. Budgets is scoped to an
// account id, and the console learns its own the way every AWS client does —
// GetCallerIdentity — rather than being told one out of band.
// ---------------------------------------------------------------------------

export interface CallerIdentity {
  account: string;
  arn: string;
  userId: string;
}

export const fetchCallerIdentity = async (): Promise<CallerIdentity> => {
  const xml = await awsQuery("sts", "2011-06-15", "GetCallerIdentity");
  return {
    account: elementText(xml, "Account"),
    arn: elementText(xml, "Arn"),
    userId: elementText(xml, "UserId"),
  };
};

export interface Budget {
  budgetName: string;
  budgetType: string;
  timeUnit: string;
  limitAmount: string;
  limitUnit: string;
  actualAmount: string;
}

export const fetchBudgets = async (): Promise<Budget[]> => {
  const identity = await fetchCallerIdentity();
  const described = await awsJson<{
    Budgets?: {
      BudgetName?: string;
      BudgetType?: string;
      TimeUnit?: string;
      BudgetLimit?: { Amount?: string; Unit?: string };
      CalculatedSpend?: { ActualSpend?: { Amount?: string; Unit?: string } };
    }[];
  }>("budgets", "AWSBudgetServiceGateway.DescribeBudgets", { AccountId: identity.account });
  return (described.Budgets ?? []).map((budget) => ({
    budgetName: budget.BudgetName ?? "",
    budgetType: budget.BudgetType ?? "",
    timeUnit: budget.TimeUnit ?? "",
    limitAmount: budget.BudgetLimit?.Amount ?? "",
    limitUnit: budget.BudgetLimit?.Unit ?? "",
    actualAmount: budget.CalculatedSpend?.ActualSpend?.Amount ?? "",
  }));
};
