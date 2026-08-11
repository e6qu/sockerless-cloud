import { afterEach, describe, expect, it, vi } from "vitest";
import {
  deleteARRepository,
  deleteCloudFunction,
  deleteCloudRunJob,
  deleteGCSBucket,
  fetchV2Operation,
  waitV2Operation,
  type CrmOperation,
} from "../api.js";
import { GcpApiError } from "../console/federation.js";

/**
 * Direct request-shaping coverage for the four delete operations this pass
 * added, apart from the component round trips in ResourceDeleteFlows.test.tsx
 * — the exact method and path each sends, storage.buckets.delete's 204 body
 * handling, waitV2Operation's poll-until-done loop (the v2 counterpart to
 * waitArOperation, for Cloud Functions and Cloud Run jobs), and the real
 * Google Cloud error body surfacing through GcpApiError on a 404.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  vi.useRealTimers();
  mockFetch.mockReset();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

function emptyResponse(status = 204): Response {
  return new Response(null, { status });
}

function installFetch(rules: { when: (url: string, init?: RequestInit) => boolean; respond: () => Response }[]) {
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    const rule = rules.find((candidate) => candidate.when(url, init));
    if (!rule) throw new Error(`unhandled fetch: ${init?.method ?? "GET"} ${url}`);
    return rule.respond();
  });
}

describe("deleteGCSBucket", () => {
  it("sends DELETE to the bucket's own path, without a project segment", async () => {
    let seenMethod: string | undefined;
    installFetch([
      {
        when: (url) => url === "/storage/v1/b/my-bucket",
        respond: () => {
          seenMethod = "DELETE";
          return emptyResponse(204);
        },
      },
    ]);
    const result = await deleteGCSBucket("my-bucket");
    expect(seenMethod).toBe("DELETE");
    expect(result).toBeUndefined();
  });

  it("surfaces the real 404 body instead of throwing a JSON-parse error on a failed delete", async () => {
    installFetch([
      {
        when: (url) => url === "/storage/v1/b/missing-bucket",
        respond: () => jsonResponse({ error: { code: 404, message: 'bucket "missing-bucket" not found', status: "NOT_FOUND" } }, 404),
      },
    ]);
    await expect(deleteGCSBucket("missing-bucket")).rejects.toMatchObject({
      message: expect.stringContaining('bucket "missing-bucket" not found'),
      status: 404,
      reason: "NOT_FOUND",
    });
  });
});

describe("deleteARRepository / deleteCloudFunction / deleteCloudRunJob", () => {
  it("sends DELETE to the repository's real path and returns the long-running Operation", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v1/projects/sockerless/locations/us-central1/repositories/runner-images" && init?.method === "DELETE",
        respond: () => jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-1", done: true }),
      },
    ]);
    const op = await deleteARRepository("sockerless", "runner-images");
    expect(op).toEqual({ name: "projects/sockerless/locations/us-central1/operations/op-1", done: true });
  });

  it("sends DELETE to the function's real path and returns the long-running Operation", async () => {
    installFetch([
      {
        when: (url, init) =>
          url === "/v2/projects/sockerless/locations/us-central1/functions/runner" && init?.method === "DELETE",
        respond: () => jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-2", done: false }),
      },
    ]);
    const op = await deleteCloudFunction("sockerless", "runner");
    expect(op).toEqual({ name: "projects/sockerless/locations/us-central1/operations/op-2", done: false });
  });

  it("sends DELETE to the job's real path and returns the long-running Operation", async () => {
    installFetch([
      {
        when: (url, init) => url === "/v2/projects/sockerless/locations/us-central1/jobs/runner" && init?.method === "DELETE",
        respond: () => jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-3", done: false }),
      },
    ]);
    const op = await deleteCloudRunJob("sockerless", "runner");
    expect(op).toEqual({ name: "projects/sockerless/locations/us-central1/operations/op-3", done: false });
  });

  it("surfaces the real error body on a 404 delete instead of a bare HTTP status", async () => {
    installFetch([
      {
        when: (url, init) => url === "/v2/projects/sockerless/locations/us-central1/jobs/missing" && init?.method === "DELETE",
        respond: () => jsonResponse({ error: { code: 404, message: 'job "missing" not found', status: "NOT_FOUND" } }, 404),
      },
    ]);
    await expect(deleteCloudRunJob("sockerless", "missing")).rejects.toBeInstanceOf(GcpApiError);
    await expect(deleteCloudRunJob("sockerless", "missing")).rejects.toMatchObject({
      message: expect.stringContaining('job "missing" not found'),
      status: 404,
    });
  });
});

describe("waitV2Operation", () => {
  it("polls the v2 operations.get endpoint until the operation is done", async () => {
    let polls = 0;
    installFetch([
      {
        when: (url) => url === "/v2/projects/sockerless/locations/us-central1/operations/op-poll",
        respond: () => {
          polls += 1;
          return polls < 3
            ? jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-poll", done: false })
            : jsonResponse({
                name: "projects/sockerless/locations/us-central1/operations/op-poll",
                done: true,
                response: { "@type": "type.googleapis.com/google.cloud.run.v2.Job" },
              });
        },
      },
    ]);
    const result = await waitV2Operation({ name: "projects/sockerless/locations/us-central1/operations/op-poll", done: false });
    expect(polls).toBe(3);
    expect(result.done).toBe(true);
  });

  it("throws the operation's own error message once it settles failed", async () => {
    installFetch([]);
    const failed: CrmOperation = {
      name: "projects/sockerless/locations/us-central1/operations/op-failed",
      done: true,
      error: { code: 9, message: "job has active executions" },
    };
    await expect(waitV2Operation(failed)).rejects.toThrow("job has active executions");
  });

  it("reads the immediately-done operation without polling operations.get at all", async () => {
    installFetch([]);
    const done: CrmOperation = { name: "projects/sockerless/locations/us-central1/operations/op-done", done: true };
    await expect(waitV2Operation(done)).resolves.toEqual(done);
  });
});

describe("fetchV2Operation", () => {
  it("reads the /v2/{name} path directly", async () => {
    installFetch([
      {
        when: (url) => url === "/v2/projects/sockerless/locations/us-central1/operations/op-x",
        respond: () => jsonResponse({ name: "projects/sockerless/locations/us-central1/operations/op-x", done: true }),
      },
    ]);
    const op = await fetchV2Operation("projects/sockerless/locations/us-central1/operations/op-x");
    expect(op.done).toBe(true);
  });
});
