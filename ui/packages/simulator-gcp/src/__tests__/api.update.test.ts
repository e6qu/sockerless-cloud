import { beforeAll, describe, expect, it } from "vitest";
import { signInToConsole } from "@sockerless/ui-core/testing";
import {
  createCloudFunction,
  createCloudRunJob,
  fetchARRepo,
  fetchCloudFunction,
  fetchCloudRunJob,
  fetchCloudRunJobExecutions,
  fetchGCSBucket,
  runCloudRunJob,
  updateARRepository,
  updateCloudFunction,
  updateCloudRunJob,
  updateGCSBucket,
  waitV2Operation,
} from "../api.js";
import { GcpApiError } from "../console/federation.js";
import { PROJECT, createBucket, createFunction, createJob, createRepository, resourceId } from "./fixtures.js";

// The update, create and run operations against the real simulator: each call
// goes out over the console's authorized fetch, and the test reads the
// resource back to see what the API made of it.

beforeAll(signInToConsole);

describe("updateGCSBucket", () => {
  it("patches the bucket's storage class and merges the label diff", async () => {
    const bucket = await createBucket();
    await updateGCSBucket(bucket, { labels: { team: "core", stale: "yes" } });
    const updated = await updateGCSBucket(bucket, { storageClass: "NEARLINE", labels: { team: "core", stale: null } });
    expect(updated.storageClass).toBe("NEARLINE");
    expect(updated.labels).toEqual({ team: "core" });
    expect(await fetchGCSBucket(bucket)).toMatchObject({ storageClass: "NEARLINE", labels: { team: "core" } });
  });
});

describe("updateARRepository", () => {
  it("patches the repository's labels and returns the Repository synchronously", async () => {
    const repo = await createRepository();
    const updated = await updateARRepository(PROJECT, repo, { labels: { env: "prod" } });
    expect(updated.name).toBe(`projects/${PROJECT}/locations/us-central1/repositories/${repo}`);
    expect(updated.labels).toEqual({ env: "prod" });
    expect((await fetchARRepo(PROJECT, repo)).labels).toEqual({ env: "prod" });
  });

  it("surfaces the real error body on a failed update", async () => {
    const missing = resourceId("missing-repo");
    const failure = updateARRepository(PROJECT, missing, { labels: {} });
    await expect(failure).rejects.toBeInstanceOf(GcpApiError);
    await expect(failure).rejects.toMatchObject({ status: 404, message: expect.stringContaining(missing) });
  });
});

describe("updateCloudFunction", () => {
  it("replaces the serviceConfig fields its updateMask names", async () => {
    const fn = await createFunction();
    const op = await updateCloudFunction(PROJECT, fn, {
      availableMemory: "512Mi",
      timeoutSeconds: 120,
      minInstanceCount: 1,
      maxInstanceCount: 5,
      environmentVariables: { LOG: "debug" },
    });
    await waitV2Operation(op);
    expect((await fetchCloudFunction(PROJECT, fn)).serviceConfig).toMatchObject({
      availableMemory: "512Mi",
      timeoutSeconds: 120,
      minInstanceCount: 1,
      maxInstanceCount: 5,
      environmentVariables: { LOG: "debug" },
    });
  });
});

describe("updateCloudRunJob", () => {
  it("replaces the job with the full Job resource it sends", async () => {
    const job = await createJob();
    const loaded = await fetchCloudRunJob(PROJECT, job);
    await waitV2Operation(
      await updateCloudRunJob(PROJECT, job, {
        ...loaded,
        template: { taskCount: 3, template: { timeout: "900s", containers: [{ image: "us-docker.pkg.dev/cloudrun/container/job:v2" }] } },
      }),
    );
    expect((await fetchCloudRunJob(PROJECT, job)).template).toMatchObject({
      taskCount: 3,
      template: { timeout: "900s", containers: [{ image: "us-docker.pkg.dev/cloudrun/container/job:v2" }] },
    });
  });
});

describe("runCloudRunJob", () => {
  it("starts an execution of the job through its :run verb", async () => {
    const job = await createJob();
    await runCloudRunJob(PROJECT, job);
    const executions = await fetchCloudRunJobExecutions(PROJECT, job);
    expect(executions).toHaveLength(1);
    expect(executions[0].name).toMatch(new RegExp(`/jobs/${job}/executions/`));
  });
});

describe("createCloudRunJob", () => {
  it("creates the job with the nested execution/task template", async () => {
    const job = resourceId("job");
    await waitV2Operation(
      await createCloudRunJob(PROJECT, job, {
        image: "us-docker.pkg.dev/cloudrun/container/job",
        taskCount: 4,
        timeoutSeconds: 300,
        env: [{ name: "MODE", value: "batch" }],
      }),
    );
    expect((await fetchCloudRunJob(PROJECT, job)).template).toMatchObject({
      taskCount: 4,
      template: {
        timeout: "300s",
        containers: [{ image: "us-docker.pkg.dev/cloudrun/container/job", env: [{ name: "MODE", value: "batch" }] }],
      },
    });
  });

  it("creates a container without env when none is set", async () => {
    const job = resourceId("job");
    await waitV2Operation(
      await createCloudRunJob(PROJECT, job, { image: "us-docker.pkg.dev/cloudrun/container/job", taskCount: 1, timeoutSeconds: 600, env: [] }),
    );
    const [container] = (await fetchCloudRunJob(PROJECT, job)).template?.template?.containers ?? [];
    expect(container.image).toBe("us-docker.pkg.dev/cloudrun/container/job");
    expect(container.env ?? []).toEqual([]);
  });
});

describe("createCloudFunction", () => {
  it("creates a Gen 2 function with the buildConfig runtime and entry point", async () => {
    const fn = resourceId("fn");
    await waitV2Operation(await createCloudFunction(PROJECT, fn, { runtime: "nodejs20", entryPoint: "handler" }));
    expect(await fetchCloudFunction(PROJECT, fn)).toMatchObject({
      name: `projects/${PROJECT}/locations/us-central1/functions/${fn}`,
      environment: "GEN_2",
      buildConfig: { runtime: "nodejs20", entryPoint: "handler" },
    });
  });
});
