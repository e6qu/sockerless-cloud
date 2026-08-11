import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { ACRRegistryDetailPage } from "../pages/ACRRegistryDetailPage.js";
import { StorageAccountDetailPage } from "../pages/StorageAccountDetailPage.js";
import { ContainerAppDetailPage } from "../pages/ContainerAppDetailPage.js";
import { FunctionAppDetailPage } from "../pages/FunctionAppDetailPage.js";

/**
 * The detail-blade UPDATE + lifecycle round trips, end to end against a mocked
 * federated fetch: the command opens the real Fluent editor, a submit fires the
 * resource's real ARM PATCH/PUT/POST, and the editor closes on success. The
 * lightweight Playwright suite (no identity provider) can only assert these
 * commands render disabled without a live read; this suite is where the
 * authenticated round trip runs under jsdom, the same split
 * ResourceDeleteFlows.test.tsx documents for the delete dialogs.
 */

const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

afterEach(() => {
  cleanup();
  mockFetch.mockReset();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
}

type Rule = { when: (url: string, init?: RequestInit) => boolean; respond: (init?: RequestInit) => Response };

// installFetch answers config.json and the given rules; any other GET returns an
// empty ARM list/object so the blade's secondary reads (executions, settings,
// functions) resolve without extra setup, and an unmatched mutation throws.
function installFetch(rules: Rule[]) {
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    const rule = rules.find((candidate) => candidate.when(url, init));
    if (rule) return rule.respond(init);
    const method = init?.method ?? "GET";
    if (method === "GET" || url.endsWith("/config/appsettings/list")) return jsonResponse({ value: [], properties: {} });
    throw new Error(`unhandled fetch: ${method} ${url}`);
  });
}

function renderDetail(path: string, routePath: string, element: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={routePath} element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const SUBS: Rule = {
  when: (url) => url === "/subscriptions?api-version=2022-12-01",
  respond: () => jsonResponse({ value: [{ subscriptionId: "sub-1" }] }),
};

describe("ACR registry detail — Update", () => {
  const id = "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/reg";
  const rules: Rule[] = [
    SUBS,
    {
      when: (url) => url === "/subscriptions/sub-1/providers/Microsoft.ContainerRegistry/registries?api-version=2023-07-01",
      respond: () => jsonResponse({ value: [{ id, name: "reg", location: "eastus" }] }),
    },
    {
      when: (url, init) => url === `${id}?api-version=2023-07-01` && (init?.method ?? "GET") === "GET",
      respond: () =>
        jsonResponse({
          id,
          name: "reg",
          location: "eastus",
          sku: { name: "Basic", tier: "Basic" },
          properties: { loginServer: "reg.azurecr.io", adminUserEnabled: false, provisioningState: "Succeeded" },
        }),
    },
  ];

  it("opens the update form and PATCHes the edited SKU and admin-user toggle", async () => {
    let patched: unknown = null;
    installFetch([
      ...rules,
      {
        when: (url, init) => url === `${id}?api-version=2023-07-01` && init?.method === "PATCH",
        respond: (init) => {
          patched = JSON.parse(init!.body as string);
          return jsonResponse({ id, name: "reg", sku: { name: "Premium" }, properties: { adminUserEnabled: true } });
        },
      },
    ]);
    renderDetail("/ui/acr/reg", "/ui/acr/:name", <ACRRegistryDetailPage />);
    await waitFor(() => expect((screen.getByTestId("acr-registry-update") as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByTestId("acr-registry-update"));
    fireEvent.change(await screen.findByTestId("acr-update-sku"), { target: { value: "Premium" } });
    fireEvent.click(screen.getByRole("switch"));
    fireEvent.click(screen.getByTestId("acr-update-save"));

    await waitFor(() => expect(patched).toEqual({ sku: { name: "Premium" }, properties: { adminUserEnabled: true } }));
    await waitFor(() => expect(screen.queryByTestId("acr-update-form")).toBeNull());
  });
});

describe("Storage account detail — Edit tags", () => {
  const id = "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/acct";
  const rules: Rule[] = [
    SUBS,
    {
      when: (url) => url === "/subscriptions/sub-1/providers/Microsoft.Storage/storageAccounts?api-version=2023-01-01",
      respond: () => jsonResponse({ value: [{ id, name: "acct", location: "eastus", kind: "StorageV2" }] }),
    },
    {
      when: (url, init) => url === `${id}?api-version=2023-01-01` && (init?.method ?? "GET") === "GET",
      respond: () =>
        jsonResponse({
          id,
          name: "acct",
          location: "eastus",
          kind: "StorageV2",
          tags: { env: "prod" },
          sku: { name: "Standard_LRS" },
          properties: { provisioningState: "Succeeded", accessTier: "Hot", primaryEndpoints: {} },
        }),
    },
  ];

  it("seeds the existing tags and PATCHes the edited map", async () => {
    let patched: unknown = null;
    installFetch([
      ...rules,
      {
        when: (url, init) => url === `${id}?api-version=2023-01-01` && init?.method === "PATCH",
        respond: (init) => {
          patched = JSON.parse(init!.body as string);
          return jsonResponse({ id, name: "acct", tags: {} });
        },
      },
    ]);
    renderDetail("/ui/storage/acct", "/ui/storage/:name", <StorageAccountDetailPage />);
    await waitFor(() => expect((screen.getByTestId("storage-account-tags") as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByTestId("storage-account-tags"));
    const key0 = (await screen.findByTestId("storage-account-tags-key-0")) as HTMLInputElement;
    expect(key0.value).toBe("env");
    fireEvent.change(screen.getByTestId("storage-account-tags-value-0"), { target: { value: "staging" } });
    fireEvent.click(screen.getByTestId("storage-account-tags-save"));

    await waitFor(() => expect(patched).toEqual({ tags: { env: "staging" } }));
    await waitFor(() => expect(screen.queryByTestId("storage-account-tags-form")).toBeNull());
  });
});

describe("Container Apps job detail — Edit", () => {
  const id = "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.App/jobs/job1";
  const rules: Rule[] = [
    SUBS,
    {
      when: (url) => url === "/subscriptions/sub-1/providers/Microsoft.App/jobs?api-version=2024-03-01",
      respond: () => jsonResponse({ value: [{ id, name: "job1", location: "eastus", type: "Microsoft.App/jobs" }] }),
    },
    {
      when: (url, init) => url === `${id}?api-version=2024-03-01` && (init?.method ?? "GET") === "GET",
      respond: () =>
        jsonResponse({
          id,
          name: "job1",
          location: "eastus",
          properties: {
            provisioningState: "Succeeded",
            environmentId: "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env1",
            configuration: { triggerType: "Manual", replicaTimeout: 300, manualTriggerConfig: { parallelism: 1 } },
            template: { containers: [{ name: "worker", image: "old:1" }] },
          },
        }),
    },
  ];

  it("opens the edit form and PUTs the whole job with the edited image and parallelism", async () => {
    let put: { url: string; body: unknown } | null = null;
    installFetch([
      ...rules,
      {
        when: (url, init) => url === `${id}?api-version=2024-03-01` && init?.method === "PUT",
        respond: (init) => {
          put = { url: `${id}?api-version=2024-03-01`, body: JSON.parse(init!.body as string) };
          return jsonResponse({ id, name: "job1", location: "eastus", type: "Microsoft.App/jobs" });
        },
      },
    ]);
    renderDetail("/ui/container-apps/job1", "/ui/container-apps/:name", <ContainerAppDetailPage />);
    await waitFor(() => expect((screen.getByTestId("ca-job-edit") as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByTestId("ca-job-edit"));
    fireEvent.change(await screen.findByTestId("ca-job-edit-image-0"), { target: { value: "new:2" } });
    fireEvent.change(screen.getByTestId("ca-job-edit-parallelism"), { target: { value: "4" } });
    fireEvent.click(screen.getByTestId("ca-job-edit-save"));

    await waitFor(() => expect(put).not.toBeNull());
    const body = put!.body as {
      location: string;
      properties: { environmentId: string; configuration: Record<string, unknown>; template: { containers: unknown[] } };
    };
    expect(body.properties.environmentId).toContain("managedEnvironments/env1");
    expect(body.properties.configuration.manualTriggerConfig).toEqual({ parallelism: 4, replicaCompletionCount: 4 });
    expect(body.properties.template.containers[0]).toEqual({ name: "worker", image: "new:2" });
    await waitFor(() => expect(screen.queryByTestId("ca-job-edit-form")).toBeNull());
  });
});

describe("Function App detail — Start action", () => {
  const id = "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Web/sites/site1";
  const rules: Rule[] = [
    SUBS,
    {
      when: (url) => url === "/subscriptions/sub-1/providers/Microsoft.Web/sites?api-version=2023-12-01",
      respond: () => jsonResponse({ value: [{ id, name: "site1", location: "eastus", kind: "functionapp" }] }),
    },
    {
      when: (url, init) => url === `${id}?api-version=2023-12-01` && (init?.method ?? "GET") === "GET",
      respond: () =>
        jsonResponse({
          id,
          name: "site1",
          location: "eastus",
          kind: "functionapp",
          properties: { state: "Stopped", defaultHostName: "site1.azurewebsites.net", httpsOnly: true, enabled: false },
        }),
    },
  ];

  it("POSTs the real /start action for the loaded site", async () => {
    let started: string | null = null;
    installFetch([
      ...rules,
      {
        when: (url, init) => url === `${id}/start?api-version=2023-12-01` && init?.method === "POST",
        respond: () => {
          started = "start";
          return new Response(null, { status: 200 });
        },
      },
    ]);
    renderDetail("/ui/functions/site1", "/ui/functions/:name", <FunctionAppDetailPage />);
    await waitFor(() => expect((screen.getByTestId("fn-site-start") as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByTestId("fn-site-start"));
    await waitFor(() => expect(started).toBe("start"));
  });

  it("PATCHes tags while preserving the current httpsOnly", async () => {
    let patched: unknown = null;
    installFetch([
      ...rules,
      {
        when: (url, init) => url === `${id}?api-version=2023-12-01` && init?.method === "PATCH",
        respond: (init) => {
          patched = JSON.parse(init!.body as string);
          return jsonResponse({ id, name: "site1" });
        },
      },
    ]);
    renderDetail("/ui/functions/site1", "/ui/functions/:name", <FunctionAppDetailPage />);
    await waitFor(() => expect((screen.getByTestId("fn-site-tags") as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByTestId("fn-site-tags"));
    fireEvent.change(await screen.findByTestId("fn-site-tags-key-0"), { target: { value: "owner" } });
    fireEvent.change(screen.getByTestId("fn-site-tags-value-0"), { target: { value: "core" } });
    fireEvent.click(screen.getByTestId("fn-site-tags-save"));

    await waitFor(() => expect(patched).toEqual({ tags: { owner: "core" }, properties: { httpsOnly: true } }));
  });
});
