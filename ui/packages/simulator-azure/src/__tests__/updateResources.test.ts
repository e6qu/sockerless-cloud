import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createContainerAppJob,
  createFunctionApp,
  restartFunctionApp,
  startFunctionApp,
  stopFunctionApp,
  updateACRRegistry,
  updateContainerAppJob,
  updateFunctionAppTags,
  updateResourceTags,
  updateStorageAccount,
} from "../api.js";

/**
 * The console's UPDATE + lifecycle + compute-create story drives real Azure
 * Resource Manager PATCH/PUT/POST verbs. These tests pin the exact wire shape —
 * paths, api-versions, methods, and bodies — against a mocked federated fetch,
 * the way createResources.test.ts pins the create PUTs, and prove ARM's own
 * error envelope surfaces rather than being masked.
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

// captureCalls records every non-config fetch and returns the given responses
// in order (or a default 200 JSON echo).
function captureCalls(responder?: (url: string, init?: RequestInit) => Response) {
  const calls: { url: string; init?: RequestInit }[] = [];
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    calls.push({ url, init });
    return responder?.(url, init) ?? jsonResponse({ id: url.split("?")[0], name: "resource", location: "eastus" });
  });
  return calls;
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("updateResourceTags", () => {
  it("PATCHes just the tags map at the resource's own api-version", async () => {
    const calls = captureCalls();
    await updateResourceTags(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/acct",
      "2023-01-01",
      { env: "prod", team: "core" },
    );
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/acct?api-version=2023-01-01",
    );
    expect(calls[0].init?.method).toBe("PATCH");
    expect(await bodyOf(calls[0].init)).toEqual({ tags: { env: "prod", team: "core" } });
  });

  it("surfaces ARM's own error rather than masking it", async () => {
    captureCalls(() => armError("InvalidRequestContent", "A tag name cannot be empty.", 400));
    await expect(updateResourceTags("/id", "2023-01-01", { "": "x" })).rejects.toThrow(/cannot be empty/);
  });
});

describe("updateStorageAccount", () => {
  it("PATCHes sku.name and properties.accessTier", async () => {
    const calls = captureCalls();
    await updateStorageAccount("/subscriptions/s/rg/acct", { skuName: "Standard_GRS", accessTier: "Cool" });
    expect(calls[0].url).toBe("/subscriptions/s/rg/acct?api-version=2023-01-01");
    expect(calls[0].init?.method).toBe("PATCH");
    expect(await bodyOf(calls[0].init)).toEqual({ sku: { name: "Standard_GRS" }, properties: { accessTier: "Cool" } });
  });
});

describe("updateACRRegistry", () => {
  it("PATCHes sku.name and properties.adminUserEnabled", async () => {
    const calls = captureCalls();
    await updateACRRegistry("/subscriptions/s/rg/reg", { skuName: "Premium", adminUserEnabled: true });
    expect(calls[0].url).toBe("/subscriptions/s/rg/reg?api-version=2023-07-01");
    expect(calls[0].init?.method).toBe("PATCH");
    expect(await bodyOf(calls[0].init)).toEqual({ sku: { name: "Premium" }, properties: { adminUserEnabled: true } });
  });
});

describe("updateFunctionAppTags", () => {
  it("PATCHes tags and re-sends the current httpsOnly so a tags edit can't clear it", async () => {
    const calls = captureCalls();
    await updateFunctionAppTags("/subscriptions/s/rg/site", { env: "prod" }, true);
    expect(calls[0].url).toBe("/subscriptions/s/rg/site?api-version=2023-12-01");
    expect(calls[0].init?.method).toBe("PATCH");
    expect(await bodyOf(calls[0].init)).toEqual({ tags: { env: "prod" }, properties: { httpsOnly: true } });
  });
});

describe("Function App lifecycle actions", () => {
  it("POSTs the real start / stop / restart action routes", async () => {
    for (const [fn, suffix] of [
      [startFunctionApp, "start"],
      [stopFunctionApp, "stop"],
      [restartFunctionApp, "restart"],
    ] as const) {
      const calls = captureCalls(() => new Response(null, { status: 200 }));
      await fn("/subscriptions/s/rg/site");
      expect(calls[0].url).toBe(`/subscriptions/s/rg/site/${suffix}?api-version=2023-12-01`);
      expect(calls[0].init?.method).toBe("POST");
    }
  });
});

describe("updateContainerAppJob", () => {
  it("re-PUTs the whole job with the edited configuration and template", async () => {
    const calls = captureCalls();
    await updateContainerAppJob(
      "/subscriptions/s/rg/providers/Microsoft.App/jobs/job1",
      "eastus",
      "/subscriptions/s/rg/providers/Microsoft.App/managedEnvironments/env1",
      {
        triggerType: "Manual",
        replicaTimeout: 600,
        replicaRetryLimit: 1,
        parallelism: 2,
        cronExpression: "",
        containers: [{ name: "worker", image: "alpine:3.20", command: [], args: [], env: [{ name: "K", value: "V" }] }],
      },
    );
    expect(calls[0].url).toBe("/subscriptions/s/rg/providers/Microsoft.App/jobs/job1?api-version=2024-03-01");
    expect(calls[0].init?.method).toBe("PUT");
    expect(await bodyOf(calls[0].init)).toEqual({
      location: "eastus",
      properties: {
        environmentId: "/subscriptions/s/rg/providers/Microsoft.App/managedEnvironments/env1",
        configuration: {
          triggerType: "Manual",
          replicaTimeout: 600,
          replicaRetryLimit: 1,
          manualTriggerConfig: { parallelism: 2, replicaCompletionCount: 2 },
        },
        template: { containers: [{ name: "worker", image: "alpine:3.20", env: [{ name: "K", value: "V" }] }] },
      },
    });
  });

  it("nests parallelism and cron under scheduleTriggerConfig for a Schedule job", async () => {
    const calls = captureCalls();
    await updateContainerAppJob("/id", "eastus", "/env", {
      triggerType: "Schedule",
      replicaTimeout: 300,
      replicaRetryLimit: 0,
      parallelism: 3,
      cronExpression: "0 * * * *",
      containers: [{ name: "c", image: "img", command: ["/bin/run"], args: ["--flag"], env: [] }],
    });
    const body = (await bodyOf(calls[0].init)) as {
      properties: { configuration: Record<string, unknown>; template: { containers: unknown[] } };
    };
    expect(body.properties.configuration.scheduleTriggerConfig).toEqual({
      parallelism: 3,
      replicaCompletionCount: 3,
      cronExpression: "0 * * * *",
    });
    expect(body.properties.template.containers[0]).toEqual({
      name: "c",
      image: "img",
      command: ["/bin/run"],
      args: ["--flag"],
    });
  });
});

describe("createContainerAppJob", () => {
  it("ensures the resource group and managed environment, then PUTs the job referencing the env", async () => {
    const envId = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env1";
    const calls = captureCalls((url) => {
      if (url.includes("/managedEnvironments/")) return jsonResponse({ id: envId, name: "env1", location: "eastus" });
      if (url.includes("/jobs/")) {
        return jsonResponse({
          id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.App/jobs/job1",
          name: "job1",
          location: "eastus",
          type: "Microsoft.App/jobs",
        });
      }
      return jsonResponse({ id: "rg", name: "rg", location: "eastus" });
    });

    const result = await createContainerAppJob({
      subscriptionId: "s",
      resourceGroup: "rg",
      name: "job1",
      location: "eastus",
      environmentName: "env1",
      config: {
        triggerType: "Manual",
        replicaTimeout: 1800,
        replicaRetryLimit: 0,
        parallelism: 1,
        cronExpression: "",
        containers: [{ name: "job1", image: "alpine", command: [], args: [], env: [] }],
      },
    });

    expect(result).toEqual({
      id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.App/jobs/job1",
      name: "job1",
      location: "eastus",
      type: "Microsoft.App/jobs",
    });
    expect(calls[0].url).toBe("/subscriptions/s/resourceGroups/rg?api-version=2021-04-01");
    expect(calls[1].url).toBe(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env1?api-version=2024-03-01",
    );
    expect(calls[1].init?.method).toBe("PUT");
    expect(calls[2].url).toBe(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.App/jobs/job1?api-version=2024-03-01",
    );
    const jobBody = (await bodyOf(calls[2].init)) as { properties: { environmentId: string } };
    expect(jobBody.properties.environmentId).toBe(envId);
  });
});

describe("createFunctionApp", () => {
  it("ensures the resource group and hosting plan, then PUTs the site with the runtime app setting", async () => {
    const planId = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/serverfarms/plan1";
    const calls = captureCalls((url) => {
      if (url.includes("/serverfarms/")) return jsonResponse({ id: planId, name: "plan1", location: "eastus" });
      if (url.includes("/sites/")) {
        return jsonResponse({
          id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/site1",
          name: "site1",
          location: "eastus",
          kind: "functionapp",
        });
      }
      return jsonResponse({ id: "rg", name: "rg", location: "eastus" });
    });

    const result = await createFunctionApp({
      subscriptionId: "s",
      resourceGroup: "rg",
      name: "site1",
      location: "eastus",
      runtime: "python",
      planName: "plan1",
    });

    expect(result.name).toBe("site1");
    expect(calls[0].url).toBe("/subscriptions/s/resourceGroups/rg?api-version=2021-04-01");
    expect(calls[1].url).toBe(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/serverfarms/plan1?api-version=2023-12-01",
    );
    expect(await bodyOf(calls[1].init)).toEqual({
      location: "eastus",
      sku: { name: "Y1", tier: "Dynamic" },
      properties: {},
    });
    expect(calls[2].url).toBe(
      "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/site1?api-version=2023-12-01",
    );
    const siteBody = (await bodyOf(calls[2].init)) as {
      kind: string;
      properties: { serverFarmId: string; siteConfig: { appSettings: { name: string; value: string }[] } };
    };
    expect(siteBody.kind).toBe("functionapp");
    expect(siteBody.properties.serverFarmId).toBe(planId);
    expect(siteBody.properties.siteConfig.appSettings).toContainEqual({ name: "FUNCTIONS_WORKER_RUNTIME", value: "python" });
  });
});
