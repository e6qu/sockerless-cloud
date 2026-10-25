import { useSyncExternalStore } from "react";
import type { OperationProgress } from "./arm.js";

// The deployments the create wizards start in this session, each with the
// resource operations it runs. A deployment runs to its end whether or not
// its page stays open, and the page follows it, as the portal's deployment
// page follows a template deployment.

export type DeploymentStatus = "InProgress" | "Succeeded" | "Failed";

export interface DeploymentOperation {
  resourceName: string;
  resourceType: string;
  resourceId: string;
  status: DeploymentStatus;
  /** The Azure-AsyncOperation or Location URL ARM answered with. */
  operationUrl?: string;
  statusMessage?: string;
}

export interface Deployment {
  name: string;
  subscriptionId: string;
  resourceGroup: string;
  status: DeploymentStatus;
  startTime: number;
  endTime?: number;
  operations: DeploymentOperation[];
  /** The resource the deployment exists for, opened by "Go to resource". */
  resourceId: string;
  error?: string;
}

const STORAGE_KEY = "azure-portal:deployments";

function read(): Record<string, Deployment> {
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as Record<string, Deployment>) : {};
  } catch {
    return {};
  }
}

let deployments = read();
const listeners = new Set<() => void>();

function write(next: Record<string, Deployment>) {
  deployments = next;
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Storage blocked: the deployment is still followed on this page.
  }
  for (const listener of listeners) listener();
}

function update(name: string, change: (deployment: Deployment) => Deployment) {
  const current = deployments[name];
  if (current) write({ ...deployments, [name]: change(current) });
}

export function useDeployment(name: string): Deployment | undefined {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => deployments[name],
  );
}

export interface DeploymentStep {
  resourceName: string;
  resourceType: string;
  resourceId: string;
  run: (onProgress: (progress: OperationProgress) => void) => Promise<unknown>;
}

/** Starts a deployment that runs its steps in order and settles with them. */
export function startDeployment(
  name: string,
  subscriptionId: string,
  resourceGroup: string,
  resourceId: string,
  steps: DeploymentStep[],
): Promise<void> {
  write({
    ...deployments,
    [name]: {
      name,
      subscriptionId,
      resourceGroup,
      resourceId,
      status: "InProgress",
      startTime: Date.now(),
      operations: steps.map((step) => ({ resourceName: step.resourceName, resourceType: step.resourceType, resourceId: step.resourceId, status: "InProgress" })),
    },
  });
  return (async () => {
    for (const [index, step] of steps.entries()) {
      const setOperation = (change: Partial<DeploymentOperation>) =>
        update(name, (deployment) => ({
          ...deployment,
          operations: deployment.operations.map((operation, position) => (position === index ? { ...operation, ...change } : operation)),
        }));
      try {
        await step.run((progress) => setOperation({ operationUrl: progress.operationUrl }));
        setOperation({ status: "Succeeded", statusMessage: "OK" });
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        setOperation({ status: "Failed", statusMessage: message });
        update(name, (deployment) => ({ ...deployment, status: "Failed", endTime: Date.now(), error: message }));
        throw error;
      }
    }
    update(name, (deployment) => ({ ...deployment, status: "Succeeded", endTime: Date.now() }));
  })();
}

/** The portal's deployment name: the resource's prefix and the start time. */
export function deploymentName(prefix: string): string {
  const now = new Date();
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${prefix}-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
}
