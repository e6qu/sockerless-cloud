import { beforeAll, describe, expect, it } from "vitest";
import { signInToConsole } from "@sockerless/ui-core/testing";
import {
  deleteARRepository,
  deleteCloudFunction,
  deleteCloudRunJob,
  deleteGCSBucket,
  fetchARRepos,
  fetchCloudFunctions,
  fetchCloudRunJobsReal,
  fetchGCSBucket,
  fetchV2Operation,
  waitArOperation,
  waitV2Operation,
  type CrmOperation,
} from "../api.js";
import { GcpApiError } from "../console/federation.js";
import { PROJECT, createBucket, createFunction, createJob, createRepository, resourceId } from "./fixtures.js";

// The four delete operations against the real simulator: storage.buckets.delete
// answers 204 with no body, the Artifact Registry, Cloud Run functions and
// Cloud Run jobs deletes answer a long-running Operation the console drives to
// completion, and a delete of a missing resource surfaces the API's own error
// body through GcpApiError.

beforeAll(signInToConsole);

describe("deleteGCSBucket", () => {
  it("deletes the bucket at its own path, without a project segment", async () => {
    const bucket = await createBucket();
    await expect(deleteGCSBucket(bucket)).resolves.toBeUndefined();
    await expect(fetchGCSBucket(bucket)).rejects.toMatchObject({ status: 404 });
  });

  it("surfaces the real 404 body instead of throwing a JSON-parse error on a failed delete", async () => {
    const missing = resourceId("missing-bucket");
    const failure = deleteGCSBucket(missing);
    await expect(failure).rejects.toBeInstanceOf(GcpApiError);
    await expect(failure).rejects.toMatchObject({
      message: expect.stringContaining(missing),
      status: 404,
      reason: "NOT_FOUND",
    });
  });
});

describe("deleteARRepository / deleteCloudFunction / deleteCloudRunJob", () => {
  it("deletes the repository through the long-running Operation it returns", async () => {
    const repo = await createRepository();
    const op = await deleteARRepository(PROJECT, repo);
    expect(op.name).toMatch(new RegExp(`^projects/${PROJECT}/locations/us-central1/operations/`));
    await waitArOperation(op);
    expect((await fetchARRepos(PROJECT)).map((r) => r.name)).not.toContain(
      `projects/${PROJECT}/locations/us-central1/repositories/${repo}`,
    );
  });

  it("deletes the function through the long-running Operation it returns", async () => {
    const fn = await createFunction();
    const op = await deleteCloudFunction(PROJECT, fn);
    expect(op.name).toMatch(new RegExp(`^projects/${PROJECT}/locations/us-central1/operations/`));
    await waitV2Operation(op);
    expect((await fetchCloudFunctions(PROJECT)).map((f) => f.name)).not.toContain(
      `projects/${PROJECT}/locations/us-central1/functions/${fn}`,
    );
  });

  it("deletes the job through the long-running Operation it returns", async () => {
    const job = await createJob();
    const op = await deleteCloudRunJob(PROJECT, job);
    expect(op.name).toMatch(new RegExp(`^projects/${PROJECT}/locations/us-central1/operations/`));
    await waitV2Operation(op);
    expect((await fetchCloudRunJobsReal(PROJECT)).map((j) => j.name)).not.toContain(
      `projects/${PROJECT}/locations/us-central1/jobs/${job}`,
    );
  });

  it("surfaces the real error body on a 404 delete instead of a bare HTTP status", async () => {
    const missing = resourceId("missing-job");
    const failure = deleteCloudRunJob(PROJECT, missing);
    await expect(failure).rejects.toBeInstanceOf(GcpApiError);
    await expect(failure).rejects.toMatchObject({ message: expect.stringContaining(missing), status: 404 });
  });
});

describe("waitV2Operation", () => {
  it("drives the operation operations.get reports to done", async () => {
    const job = await createJob();
    const result = await waitV2Operation(await deleteCloudRunJob(PROJECT, job));
    expect(result.done).toBe(true);
    expect(result.error).toBeUndefined();
  });

  it("throws the operation's own error message once it settles failed", async () => {
    const failed: CrmOperation = {
      name: "projects/sockerless/locations/us-central1/operations/op-failed",
      done: true,
      error: { code: 9, message: "job has active executions" },
    };
    await expect(waitV2Operation(failed)).rejects.toThrow("job has active executions");
  });

  it("returns an operation that is already done as it is", async () => {
    const done: CrmOperation = { name: "projects/sockerless/locations/us-central1/operations/op-done", done: true };
    await expect(waitV2Operation(done)).resolves.toEqual(done);
  });
});

describe("fetchV2Operation", () => {
  it("reads the operation at its /v2/{name} path", async () => {
    const job = await createJob();
    const op = await deleteCloudRunJob(PROJECT, job);
    const read = await fetchV2Operation(op.name);
    expect(read.name).toBe(op.name);
  });
});
