import { beforeEach, describe, expect, it, vi } from "vitest";
import { createACRRegistry, createStorageAccount } from "../api.js";

/**
 * createStorageAccount / createACRRegistry drive two real Azure Resource
 * Manager PUTs each: the scoping resource group (idempotent, the same
 * `az group create` a real client issues first) and the resource itself.
 * These tests prove the exact wire shape — paths, api-versions, methods, and
 * bodies — against a mocked fetch, and that Azure Resource Manager's own
 * error envelope surfaces rather than being masked, the way
 * ResourceHeaderActions.test.tsx proves it for the AWS console's create
 * flows.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

function armError(code: string, message: string, status: number): Response {
  return jsonResponse({ error: { code, message } }, status);
}

async function bodyOf(init: RequestInit | undefined): Promise<unknown> {
  return init?.body ? JSON.parse(init.body as string) : undefined;
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("createStorageAccount", () => {
  it("ensures the resource group exists, then PUTs the account with the location/sku/kind the form collected", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      if (url.startsWith("/subscriptions/sub-1/resourceGroups/my-rg?")) {
        return jsonResponse({ id: "/subscriptions/sub-1/resourceGroups/my-rg", name: "my-rg", location: "eastus" }, 201);
      }
      return jsonResponse({
        id: "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.Storage/storageAccounts/mystorageacct1",
        name: "mystorageacct1",
        location: "eastus",
        kind: "StorageV2",
      });
    });

    const result = await createStorageAccount({
      subscriptionId: "sub-1",
      resourceGroup: "my-rg",
      name: "mystorageacct1",
      location: "eastus",
      skuName: "Standard_LRS",
      kind: "StorageV2",
    });

    expect(result).toEqual({
      id: "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.Storage/storageAccounts/mystorageacct1",
      name: "mystorageacct1",
      location: "eastus",
      kind: "StorageV2",
    });

    expect(calls).toHaveLength(2);
    expect(calls[0].url).toBe("/subscriptions/sub-1/resourceGroups/my-rg?api-version=2021-04-01");
    expect(calls[0].init?.method).toBe("PUT");
    expect(await bodyOf(calls[0].init)).toEqual({ location: "eastus" });

    expect(calls[1].url).toBe(
      "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.Storage/storageAccounts/mystorageacct1?api-version=2023-01-01",
    );
    expect(calls[1].init?.method).toBe("PUT");
    expect(await bodyOf(calls[1].init)).toEqual({
      location: "eastus",
      sku: { name: "Standard_LRS" },
      kind: "StorageV2",
    });
  });

  it("surfaces Azure Resource Manager's own conflict error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      if (url.includes("/resourceGroups/my-rg?")) {
        return jsonResponse({ id: "rg", name: "my-rg", location: "eastus" }, 200);
      }
      return armError("StorageAccountAlreadyTaken", "The storage account named mystorageacct1 is already taken.", 409);
    });

    await expect(
      createStorageAccount({
        subscriptionId: "sub-1",
        resourceGroup: "my-rg",
        name: "mystorageacct1",
        location: "eastus",
        skuName: "Standard_LRS",
        kind: "StorageV2",
      }),
    ).rejects.toThrow(/already taken/);
  });
});

describe("createACRRegistry", () => {
  it("ensures the resource group exists, then PUTs the registry with the location/sku the form collected", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      calls.push({ url, init });
      if (url.startsWith("/subscriptions/sub-1/resourceGroups/my-rg?")) {
        return jsonResponse({ id: "/subscriptions/sub-1/resourceGroups/my-rg", name: "my-rg", location: "eastus" }, 201);
      }
      return jsonResponse({
        id: "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myregistry1",
        name: "myregistry1",
        location: "eastus",
      });
    });

    const result = await createACRRegistry({
      subscriptionId: "sub-1",
      resourceGroup: "my-rg",
      name: "myregistry1",
      location: "eastus",
      skuName: "Premium",
    });

    expect(result).toEqual({
      id: "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myregistry1",
      name: "myregistry1",
      location: "eastus",
    });

    expect(calls).toHaveLength(2);
    expect(calls[0].url).toBe("/subscriptions/sub-1/resourceGroups/my-rg?api-version=2021-04-01");
    expect(calls[0].init?.method).toBe("PUT");
    expect(await bodyOf(calls[0].init)).toEqual({ location: "eastus" });

    expect(calls[1].url).toBe(
      "/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myregistry1?api-version=2023-07-01",
    );
    expect(calls[1].init?.method).toBe("PUT");
    expect(await bodyOf(calls[1].init)).toEqual({ location: "eastus", sku: { name: "Premium" } });
  });

  it("surfaces Azure Resource Manager's own validation error rather than masking it", async () => {
    mockFetch.mockImplementation(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/ui/config.json") return jsonResponse({});
      if (url.includes("/resourceGroups/my-rg?")) {
        return jsonResponse({ id: "rg", name: "my-rg", location: "eastus" }, 200);
      }
      return armError("InvalidRequestContent", "The 'location' property is required.", 400);
    });

    await expect(
      createACRRegistry({
        subscriptionId: "sub-1",
        resourceGroup: "my-rg",
        name: "myregistry1",
        location: "",
        skuName: "Basic",
      }),
    ).rejects.toThrow(/location.*required/);
  });
});
