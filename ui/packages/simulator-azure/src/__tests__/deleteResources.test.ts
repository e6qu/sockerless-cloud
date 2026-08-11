import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  deleteACRRegistry,
  deleteContainerAppJob,
  deleteFunctionApp,
  deleteStorageAccount,
} from "../api.js";

/**
 * delete{ACRRegistry,StorageAccount,ContainerAppJob,FunctionApp} each drive
 * one real Azure Resource Manager DELETE against the resource's own ARM ID.
 * These tests prove the exact wire shape — path, api-version, method — against
 * a mocked fetch, that a 204 (ARM's idempotent "already gone") resolves the
 * same as a 200, and that Azure Resource Manager's own error envelope
 * surfaces rather than being masked, the way createResources.test.ts proves
 * it for the create flows.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

function armError(code: string, message: string, status: number): Response {
  return jsonResponse({ error: { code, message } }, status);
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("deleteACRRegistry", () => {
  const id = "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myregistry1";

  it("DELETEs the registry's own ARM ID at the registries api-version", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      return new Response(null, { status: 200 });
    });

    await deleteACRRegistry(id);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${id}?api-version=2023-07-01`);
    expect(calls[0].init?.method).toBe("DELETE");
  });

  it("resolves on a 204 (already gone) the same as a 200", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      return new Response(null, { status: 204 });
    });

    await expect(deleteACRRegistry(id)).resolves.toBeUndefined();
  });

  it("surfaces Azure Resource Manager's own error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      return armError("RegistryNotFound", "The registry could not be found.", 404);
    });

    await expect(deleteACRRegistry(id)).rejects.toThrow(/could not be found/);
  });
});

describe("deleteStorageAccount", () => {
  const id = "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.Storage/storageAccounts/mystorageacct1";

  it("DELETEs the account's own ARM ID at the storage api-version", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      return new Response(null, { status: 200 });
    });

    await deleteStorageAccount(id);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${id}?api-version=2023-01-01`);
    expect(calls[0].init?.method).toBe("DELETE");
  });

  it("surfaces Azure Resource Manager's own error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      return armError("AuthorizationFailed", "The client does not have authorization.", 403);
    });

    await expect(deleteStorageAccount(id)).rejects.toThrow(/does not have authorization/);
  });
});

describe("deleteContainerAppJob", () => {
  const id = "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.App/jobs/myjob1";

  it("DELETEs the job's own ARM ID at the jobs api-version", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      return new Response(null, { status: 200 });
    });

    await deleteContainerAppJob(id);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${id}?api-version=2024-03-01`);
    expect(calls[0].init?.method).toBe("DELETE");
  });

  it("surfaces Azure Resource Manager's own error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      return armError("JobBusy", "The job has an execution in progress.", 409);
    });

    await expect(deleteContainerAppJob(id)).rejects.toThrow(/execution in progress/);
  });
});

describe("deleteFunctionApp", () => {
  const id = "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.Web/sites/mysite1";

  it("DELETEs the site's own ARM ID at the sites api-version", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      return new Response(null, { status: 200 });
    });

    await deleteFunctionApp(id);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${id}?api-version=2023-12-01`);
    expect(calls[0].init?.method).toBe("DELETE");
  });

  it("surfaces Azure Resource Manager's own error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      return armError("SiteNotFound", "The site could not be found.", 404);
    });

    await expect(deleteFunctionApp(id)).rejects.toThrow(/could not be found/);
  });
});
