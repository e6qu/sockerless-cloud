import { uniqueName } from "@sockerless/ui-core/testing";
import {
  createCloudFunction,
  createCloudRunJob,
  gcsBucketRequest,
  sendRequest,
  waitArOperation,
  waitV2Operation,
  type CrmOperation,
} from "../api.js";
import { authorizedJSONPost } from "../console/federation.js";

// Resources the console tests arrange through the console's own API client
// against the real simulator.

export const PROJECT = "sockerless";

/** Google Cloud resource IDs start with a letter and take lowercase letters, digits and hyphens. */
export function resourceId(prefix: string): string {
  return uniqueName(prefix).toLowerCase();
}

export async function createBucket(): Promise<string> {
  const name = resourceId("bucket");
  await sendRequest(
    gcsBucketRequest(PROJECT, {
      name,
      location: "US",
      storageClass: "STANDARD",
      uniformAccess: true,
      publicAccessPrevention: true,
      labels: {},
    }),
  );
  return name;
}

export async function createRepository(): Promise<string> {
  const id = resourceId("repo");
  await waitArOperation(
    await authorizedJSONPost<CrmOperation>(
      `/v1/projects/${PROJECT}/locations/us-central1/repositories?repositoryId=${id}`,
      { format: "DOCKER" },
    ),
  );
  return id;
}

export async function createJob(image = "us-docker.pkg.dev/cloudrun/container/job"): Promise<string> {
  const id = resourceId("job");
  await waitV2Operation(await createCloudRunJob(PROJECT, id, { image, taskCount: 1, timeoutSeconds: 600, env: [] }));
  return id;
}

export async function createFunction(): Promise<string> {
  const id = resourceId("fn");
  await waitV2Operation(await createCloudFunction(PROJECT, id, { runtime: "nodejs20", entryPoint: "handler" }));
  return id;
}
