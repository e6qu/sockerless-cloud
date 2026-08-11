import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createCloudFunction,
  createCloudRunJob,
  runCloudRunJob,
  updateARRepository,
  updateCloudFunction,
  updateCloudRunJob,
  updateGCSBucket,
  type CloudRunJob,
} from "../api.js";
import { GcpApiError } from "../console/federation.js";

/**
 * Direct request-shaping coverage for the update / create / run operations this
 * pass added — the exact method, path (including the un-encoded `:run` verb and
 * the `updateMask` query string), and body each sends, and the real Google
 * Cloud error body surfacing through GcpApiError on failure. The component
 * round trips live in ResourceEditFlows.test.tsx / ResourceComputeFlows.test.tsx;
 * the authenticated end-to-end loops live in the Shauth relying-party suite.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  mockFetch.mockReset();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

interface Seen {
  url: string;
  method: string;
  body: unknown;
}

function installCapture(respond: (url: string, init?: RequestInit) => Response): Seen[] {
  const seen: Seen[] = [];
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    seen.push({
      url,
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return respond(url, init);
  });
  return seen;
}

describe("updateGCSBucket", () => {
  it("PATCHes the bucket's own path with the storage class and label diff", async () => {
    const seen = installCapture(() => jsonResponse({ name: "b1", storageClass: "NEARLINE" }));
    const bucket = await updateGCSBucket("b1", { storageClass: "NEARLINE", labels: { team: "core", stale: null } });
    expect(seen[0].method).toBe("PATCH");
    expect(seen[0].url).toBe("/storage/v1/b/b1");
    expect(seen[0].body).toEqual({ storageClass: "NEARLINE", labels: { team: "core", stale: null } });
    expect(bucket.storageClass).toBe("NEARLINE");
  });
});

describe("updateARRepository", () => {
  it("PATCHes the repository path with labels and returns the Repository synchronously", async () => {
    const name = "projects/p/locations/us-central1/repositories/r1";
    const seen = installCapture(() => jsonResponse({ name, labels: { env: "prod" } }));
    const repo = await updateARRepository("p", "r1", { labels: { env: "prod" } });
    expect(seen[0].method).toBe("PATCH");
    expect(seen[0].url).toBe("/v1/projects/p/locations/us-central1/repositories/r1");
    expect(seen[0].body).toEqual({ labels: { env: "prod" } });
    expect(repo.name).toBe(name);
  });

  it("surfaces the real error body on a failed update", async () => {
    installCapture(() =>
      jsonResponse({ error: { code: 403, message: "permission denied", status: "PERMISSION_DENIED" } }, 403),
    );
    await expect(updateARRepository("p", "r1", { labels: {} })).rejects.toBeInstanceOf(GcpApiError);
  });
});

describe("updateCloudFunction", () => {
  it("PATCHes with the serviceConfig updateMask and the full merged serviceConfig", async () => {
    const seen = installCapture(() =>
      jsonResponse({ name: "op", done: true, response: { "@type": "…functions.v2.Function" } }),
    );
    const op = await updateCloudFunction("p", "fn", {
      availableMemory: "512Mi",
      timeoutSeconds: 120,
      minInstanceCount: 1,
      maxInstanceCount: 5,
      environmentVariables: { LOG: "debug" },
    });
    expect(seen[0].method).toBe("PATCH");
    expect(seen[0].url).toBe(
      "/v2/projects/p/locations/us-central1/functions/fn?updateMask=" +
        encodeURIComponent(
          "serviceConfig.availableMemory,serviceConfig.timeoutSeconds,serviceConfig.minInstanceCount,serviceConfig.maxInstanceCount,serviceConfig.environmentVariables",
        ),
    );
    expect(seen[0].body).toEqual({
      serviceConfig: {
        availableMemory: "512Mi",
        timeoutSeconds: 120,
        minInstanceCount: 1,
        maxInstanceCount: 5,
        environmentVariables: { LOG: "debug" },
      },
    });
    expect(op.done).toBe(true);
  });
});

describe("updateCloudRunJob", () => {
  it("PATCHes the job path with the full Job resource (UpdateJob full-replace)", async () => {
    const job: CloudRunJob = {
      name: "projects/p/locations/us-central1/jobs/j1",
      template: { taskCount: 3, template: { timeout: "900s", containers: [{ image: "img:2" }] } },
    };
    const seen = installCapture(() => jsonResponse({ name: "op", done: true }));
    await updateCloudRunJob("p", "j1", job);
    expect(seen[0].method).toBe("PATCH");
    expect(seen[0].url).toBe("/v2/projects/p/locations/us-central1/jobs/j1");
    expect(seen[0].body).toEqual(job);
  });
});

describe("runCloudRunJob", () => {
  it("POSTs to the job's :run verb without URL-encoding the colon", async () => {
    const seen = installCapture(() => jsonResponse({ name: "op", done: true }));
    await runCloudRunJob("p", "j1");
    expect(seen[0].method).toBe("POST");
    expect(seen[0].url).toBe("/v2/projects/p/locations/us-central1/jobs/j1:run");
    expect(seen[0].body).toEqual({});
  });
});

describe("createCloudRunJob", () => {
  it("POSTs ?jobId= with the nested execution/task template shape", async () => {
    const seen = installCapture(() => jsonResponse({ name: "op", done: true }));
    await createCloudRunJob("p", "j1", {
      image: "gcr.io/p/img",
      taskCount: 4,
      timeoutSeconds: 300,
      env: [{ name: "MODE", value: "batch" }],
    });
    expect(seen[0].method).toBe("POST");
    expect(seen[0].url).toBe("/v2/projects/p/locations/us-central1/jobs?jobId=j1");
    expect(seen[0].body).toEqual({
      name: "",
      template: {
        taskCount: 4,
        template: {
          timeout: "300s",
          containers: [{ image: "gcr.io/p/img", env: [{ name: "MODE", value: "batch" }] }],
        },
      },
    });
  });

  it("omits env when none is set", async () => {
    const seen = installCapture(() => jsonResponse({ name: "op", done: true }));
    await createCloudRunJob("p", "j2", { image: "img", taskCount: 1, timeoutSeconds: 600, env: [] });
    const body = seen[0].body as { template: { template: { containers: Array<Record<string, unknown>> } } };
    expect(body.template.template.containers[0]).toEqual({ image: "img" });
  });
});

describe("createCloudFunction", () => {
  it("POSTs ?functionId= with the buildConfig runtime and entry point", async () => {
    const seen = installCapture(() => jsonResponse({ name: "op", done: true }));
    await createCloudFunction("p", "fn1", { runtime: "nodejs20", entryPoint: "handler" });
    expect(seen[0].method).toBe("POST");
    expect(seen[0].url).toBe("/v2/projects/p/locations/us-central1/functions?functionId=fn1");
    expect(seen[0].body).toEqual({
      buildConfig: { runtime: "nodejs20", entryPoint: "handler" },
      environment: "GEN_2",
    });
  });
});
