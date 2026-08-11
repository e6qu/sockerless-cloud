import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ServiceListPage } from "../pages/ServiceListPage.js";
import { ServiceDetailPage } from "../pages/ServiceDetailPage.js";
import { SERVICE_BLADES, type ServiceBlade } from "../services.js";

/**
 * The service blades read real Azure Resource Manager operations — a
 * provider's subscription-wide List, a resource's Get, the sub-resource Lists
 * ARM nests under it, its lifecycle action POSTs, its tags PATCH, and its
 * DELETE. These tests drive those round trips against a mocked federated
 * fetch and assert the exact wire shape (path, api-version, method) each one
 * puts on the wire, the way createResources/deleteResources do for the
 * hand-written blades. The Playwright suite has no identity provider, so it
 * can only assert structure without a live cloud read; this is where the
 * authenticated read → render → act path is proven.
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

const requests: { url: string; method: string }[] = [];

function installFetch(rules: Rule[]) {
  requests.length = 0;
  mockFetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url === "/ui/config.json") return jsonResponse({});
    requests.push({ url, method: init?.method ?? "GET" });
    const rule = rules.find((candidate) => candidate.when(url, init));
    if (!rule) throw new Error(`unhandled fetch: ${init?.method ?? "GET"} ${url}`);
    return rule.respond(init);
  });
}

function renderWithQuery(ui: React.ReactElement, path = "/") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const subscriptionsRule: Rule = {
  when: (url) => url === "/subscriptions?api-version=2022-12-01",
  respond: () => jsonResponse({ value: [{ subscriptionId: "sub-1" }] }),
};

function bladeFor(label: string): ServiceBlade {
  const blade = SERVICE_BLADES.find((candidate) => candidate.label === label);
  if (!blade) throw new Error(`no service blade labelled ${label}`);
  return blade;
}

function listPath(blade: ServiceBlade): string {
  return blade.provider
    ? `/subscriptions/sub-1/providers/${blade.provider}?api-version=${blade.apiVersion}`
    : `/subscriptions/sub-1/resourceGroups?api-version=${blade.apiVersion}`;
}

describe("every service blade names a real ARM list operation", () => {
  it("gives each blade a distinct slug, testid and route target", () => {
    expect(new Set(SERVICE_BLADES.map((blade) => blade.slug)).size).toBe(SERVICE_BLADES.length);
    expect(new Set(SERVICE_BLADES.map((blade) => blade.testid)).size).toBe(SERVICE_BLADES.length);
    expect(new Set(SERVICE_BLADES.map((blade) => blade.label)).size).toBe(SERVICE_BLADES.length);
  });

  it("scopes every provider to a real Microsoft.* resource type at a dated api-version", () => {
    for (const blade of SERVICE_BLADES) {
      if (blade.provider) {
        expect(blade.provider, blade.label).toMatch(/^Microsoft\.[A-Za-z]+\/[A-Za-z]+$/);
      }
      expect(blade.apiVersion, blade.label).toMatch(/^\d{4}-\d{2}-\d{2}(-preview)?$/);
      expect(blade.columns.length, blade.label).toBeGreaterThan(0);
      expect(blade.essentials.length, blade.label).toBeGreaterThan(0);
    }
  });

  // A blade's list is only real if it actually issues the operation its
  // descriptor names, so every blade is driven here rather than a sample.
  for (const blade of SERVICE_BLADES) {
    it(`${blade.label} lists through ${blade.provider || "/subscriptions/{id}/resourceGroups"}`, async () => {
      installFetch([
        subscriptionsRule,
        {
          when: (url) => url === listPath(blade),
          respond: () =>
            jsonResponse({
              value: [
                {
                  id: `/subscriptions/sub-1/resourceGroups/blade-rg/providers/${blade.provider || "Microsoft.Resources/resourceGroups"}/probe-resource`,
                  name: "probe-resource",
                  type: blade.provider || "Microsoft.Resources/resourceGroups",
                  location: "eastus",
                  kind: blade.slug === "app-service" ? "app,linux" : "",
                  properties: {},
                },
              ],
            }),
        },
      ]);

      renderWithQuery(<ServiceListPage blade={blade} />);
      expect(await screen.findByRole("link", { name: "probe-resource" })).toBeTruthy();
      expect(requests.map((request) => request.url)).toContain(listPath(blade));
      // Fluent's TableSelectionCell precedes the first header cell, which maps
      // that cell's role to rowheader rather than columnheader; both count.
      for (const column of blade.columns) {
        const headers =
          screen.queryAllByRole("columnheader", { name: column.header }).length +
          screen.queryAllByRole("rowheader", { name: column.header }).length;
        expect(headers, `${blade.label} is missing the ${column.header} column`).toBeGreaterThan(0);
      }
    });
  }
});

describe("Virtual machines blade", () => {
  const blade = bladeFor("Virtual machines");
  const id = "/subscriptions/sub-1/resourceGroups/vm-rg/providers/Microsoft.Compute/virtualMachines/probe-vm";
  const vm = {
    id,
    name: "probe-vm",
    type: "Microsoft.Compute/virtualMachines",
    location: "eastus",
    tags: { env: "test" },
    properties: {
      vmId: "11111111-2222-3333-4444-555555555555",
      provisioningState: "Succeeded",
      hardwareProfile: { vmSize: "Standard_B1s" },
      osProfile: { computerName: "probe-vm", adminUsername: "azureuser" },
      storageProfile: { osDisk: { name: "probe-vm-osdisk" }, imageReference: { offer: "0001-com-ubuntu-server-jammy" } },
    },
  };
  const listRule: Rule = {
    when: (url) => url === `/subscriptions/sub-1/providers/Microsoft.Compute/virtualMachines?api-version=${blade.apiVersion}`,
    respond: () => jsonResponse({ value: [vm] }),
  };
  const getRule: Rule = {
    when: (url) => url === `${id}?api-version=${blade.apiVersion}`,
    respond: (init) => (init?.method === "DELETE" ? new Response(null, { status: 200 }) : jsonResponse(vm)),
  };

  it("reads the subscription-wide VirtualMachines_ListAll operation the simulator now serves", async () => {
    installFetch([subscriptionsRule, listRule]);
    renderWithQuery(<ServiceListPage blade={blade} />);
    await screen.findByRole("link", { name: "probe-vm" });
    expect(requests[1].url).toBe(
      `/subscriptions/sub-1/providers/Microsoft.Compute/virtualMachines?api-version=${blade.apiVersion}`,
    );
    expect(screen.getByText("Standard_B1s")).toBeTruthy();
  });

  it("opens the machine on its real Get and shows the documented properties in Essentials", async () => {
    installFetch([subscriptionsRule, listRule, getRule]);
    renderWithQuery(
      <Routes>
        <Route path="/ui/virtual-machines/:name" element={<ServiceDetailPage blade={blade} />} />
      </Routes>,
      "/ui/virtual-machines/probe-vm",
    );
    const essentials = await screen.findByRole("region", { name: "Essentials" });
    await waitFor(() => expect(essentials.textContent).toContain("Standard_B1s"));
    expect(essentials.textContent).toContain("11111111-2222-3333-4444-555555555555");
    expect(essentials.textContent).toContain("vm-rg");
    expect(essentials.textContent).toContain("env : test");
    expect(requests.map((request) => request.url)).toContain(`${id}?api-version=${blade.apiVersion}`);
  });

  it("drives the real ARM lifecycle action POSTs the machine exposes", async () => {
    const posted: string[] = [];
    installFetch([
      subscriptionsRule,
      listRule,
      getRule,
      {
        when: (url, init) => init?.method === "POST" && url.startsWith(`${id}/`),
        respond: () => new Response(null, { status: 200 }),
      },
    ]);
    renderWithQuery(
      <Routes>
        <Route path="/ui/virtual-machines/:name" element={<ServiceDetailPage blade={blade} />} />
      </Routes>,
      "/ui/virtual-machines/probe-vm",
    );
    await screen.findByRole("region", { name: "Essentials" });
    await waitFor(() => expect((screen.getByTestId("vm-restart") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId("vm-restart"));
    await waitFor(() => {
      posted.push(...requests.filter((request) => request.method === "POST").map((request) => request.url));
      expect(posted).toContain(`${id}/restart?api-version=${blade.apiVersion}`);
    });
  });

  it("confirms then deletes the machine through its real ARM DELETE", async () => {
    let deleted = "";
    installFetch([
      subscriptionsRule,
      listRule,
      {
        when: (url) => url === `${id}?api-version=${blade.apiVersion}`,
        respond: (init) => {
          if (init?.method === "DELETE") {
            deleted = `${id}?api-version=${blade.apiVersion}`;
            return new Response(null, { status: 200 });
          }
          return jsonResponse(vm);
        },
      },
    ]);
    renderWithQuery(
      <Routes>
        <Route path="/ui/virtual-machines/:name" element={<ServiceDetailPage blade={blade} />} />
        <Route path="/ui/virtual-machines" element={<div>listing</div>} />
      </Routes>,
      "/ui/virtual-machines/probe-vm",
    );
    await screen.findByRole("region", { name: "Essentials" });
    await waitFor(() => expect((screen.getByTestId("vm-detail-delete") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId("vm-detail-delete"));
    const dialog = await screen.findByTestId("vm-detail-delete-dialog");
    expect(within(dialog).getByText(/Delete probe-vm\?/)).toBeTruthy();
    expect(dialog.textContent).toContain("can't be undone");
    fireEvent.click(screen.getByTestId("vm-detail-delete-dialog-confirm"));
    await waitFor(() => expect(deleted).toBe(`${id}?api-version=${blade.apiVersion}`));
  });
});

describe("Virtual networks blade", () => {
  const blade = bladeFor("Virtual networks");
  const id = "/subscriptions/sub-1/resourceGroups/net-rg/providers/Microsoft.Network/virtualNetworks/probe-vnet";
  const vnet = {
    id,
    name: "probe-vnet",
    type: "Microsoft.Network/virtualNetworks",
    location: "eastus",
    properties: { provisioningState: "Succeeded", addressSpace: { addressPrefixes: ["10.0.0.0/16"] } },
  };

  it("reads the subnets and peerings sub-resource List operations ARM nests under the network", async () => {
    installFetch([
      subscriptionsRule,
      {
        when: (url) => url === `/subscriptions/sub-1/providers/Microsoft.Network/virtualNetworks?api-version=${blade.apiVersion}`,
        respond: () => jsonResponse({ value: [vnet] }),
      },
      { when: (url) => url === `${id}?api-version=${blade.apiVersion}`, respond: () => jsonResponse(vnet) },
      {
        when: (url) => url === `${id}/subnets?api-version=${blade.apiVersion}`,
        respond: () =>
          jsonResponse({
            value: [{ id: `${id}/subnets/default`, name: "default", properties: { addressPrefix: "10.0.1.0/24" } }],
          }),
      },
      {
        when: (url) => url === `${id}/virtualNetworkPeerings?api-version=${blade.apiVersion}`,
        respond: () => jsonResponse({ value: [] }),
      },
    ]);

    renderWithQuery(
      <Routes>
        <Route path="/ui/virtual-networks/:name" element={<ServiceDetailPage blade={blade} />} />
      </Routes>,
      "/ui/virtual-networks/probe-vnet",
    );

    const subnets = await screen.findByTestId("vnet-subnets");
    await waitFor(() => expect(within(subnets).getByText("default")).toBeTruthy());
    expect(within(subnets).getByText("10.0.1.0/24")).toBeTruthy();
    const peerings = screen.getByTestId("vnet-peerings");
    await waitFor(() => expect(peerings.textContent).toContain("No peerings to display"));
  });
});

describe("Container instances blade", () => {
  const blade = bladeFor("Container instances");
  const id =
    "/subscriptions/sub-1/resourceGroups/aci-rg/providers/Microsoft.ContainerInstance/containerGroups/probe-group";
  const group = {
    id,
    name: "probe-group",
    type: "Microsoft.ContainerInstance/containerGroups",
    location: "eastus",
    properties: {
      provisioningState: "Succeeded",
      osType: "Linux",
      ipAddress: { ip: "10.1.2.3" },
      containers: [
        { name: "worker", properties: { image: "alpine:3.20", instanceView: { currentState: { state: "Running" }, restartCount: 0 } } },
      ],
    },
  };

  // ARM returns a container group's containers inline on the group rather than
  // as a collection endpoint; the blade renders them from the group's own Get
  // instead of inventing a request ARM would 404.
  it("renders the containers ARM returns inline on the group, with no extra request", async () => {
    installFetch([
      subscriptionsRule,
      {
        when: (url) =>
          url === `/subscriptions/sub-1/providers/Microsoft.ContainerInstance/containerGroups?api-version=${blade.apiVersion}`,
        respond: () => jsonResponse({ value: [group] }),
      },
      { when: (url) => url === `${id}?api-version=${blade.apiVersion}`, respond: () => jsonResponse(group) },
    ]);

    renderWithQuery(
      <Routes>
        <Route path="/ui/container-instances/:name" element={<ServiceDetailPage blade={blade} />} />
      </Routes>,
      "/ui/container-instances/probe-group",
    );

    const containers = await screen.findByTestId("aci-containers");
    await waitFor(() => expect(within(containers).getByText("worker")).toBeTruthy());
    expect(within(containers).getByText("alpine:3.20")).toBeTruthy();
    expect(requests.some((request) => request.url.includes("/containers?"))).toBe(false);
  });
});

describe("Resource groups blade", () => {
  const blade = bladeFor("Resource groups");
  const id = "/subscriptions/sub-1/resourceGroups/probe-rg";
  const group = { id, name: "probe-rg", type: "Microsoft.Resources/resourceGroups", location: "eastus", properties: { provisioningState: "Succeeded" } };

  it("lists at ARM's own /subscriptions/{id}/resourceGroups path, not under /providers", async () => {
    installFetch([
      subscriptionsRule,
      {
        when: (url) => url === `/subscriptions/sub-1/resourceGroups?api-version=${blade.apiVersion}`,
        respond: () => jsonResponse({ value: [group] }),
      },
    ]);
    renderWithQuery(<ServiceListPage blade={blade} />);
    await screen.findByRole("link", { name: "probe-rg" });
    expect(requests.every((request) => !request.url.includes("/providers/Microsoft.Resources"))).toBe(true);
  });

  it("shows what the group holds through Resources_ListByResourceGroup", async () => {
    installFetch([
      subscriptionsRule,
      {
        when: (url) => url === `/subscriptions/sub-1/resourceGroups?api-version=${blade.apiVersion}`,
        respond: () => jsonResponse({ value: [group] }),
      },
      { when: (url) => url === `${id}?api-version=${blade.apiVersion}`, respond: () => jsonResponse(group) },
      {
        when: (url) => url === `${id}/resources?api-version=${blade.apiVersion}`,
        respond: () =>
          jsonResponse({
            value: [
              {
                id: `${id}/providers/Microsoft.Storage/storageAccounts/probeaccount`,
                name: "probeaccount",
                type: "Microsoft.Storage/storageAccounts",
                location: "eastus",
              },
            ],
          }),
      },
    ]);
    renderWithQuery(
      <Routes>
        <Route path="/ui/resource-groups/:name" element={<ServiceDetailPage blade={blade} />} />
      </Routes>,
      "/ui/resource-groups/probe-rg",
    );
    const resources = await screen.findByTestId("rg-resources");
    await waitFor(() => expect(within(resources).getByText("probeaccount")).toBeTruthy());
    expect(within(resources).getByText("Microsoft.Storage/storageAccounts")).toBeTruthy();
  });
});

describe("paged ARM lists", () => {
  const blade = bladeFor("Key vaults");

  // ARM pages its list operations; rendering only the first page silently
  // hides resources, so the blade follows nextLink the way every real ARM
  // client does.
  it("follows ARM's nextLink to the end rather than showing only the first page", async () => {
    const first = "/subscriptions/sub-1/providers/Microsoft.KeyVault/vaults?api-version=" + blade.apiVersion;
    installFetch([
      subscriptionsRule,
      {
        when: (url) => url === first,
        respond: () =>
          jsonResponse({
            value: [{ id: "/subscriptions/sub-1/resourceGroups/kv-rg/providers/Microsoft.KeyVault/vaults/vault-one", name: "vault-one", location: "eastus", properties: {} }],
            nextLink: `https://management.azure.com${first}&$skiptoken=1`,
          }),
      },
      {
        when: (url) => url === `${first}&%24skiptoken=1` || url === `${first}&$skiptoken=1`,
        respond: () =>
          jsonResponse({
            value: [{ id: "/subscriptions/sub-1/resourceGroups/kv-rg/providers/Microsoft.KeyVault/vaults/vault-two", name: "vault-two", location: "eastus", properties: {} }],
          }),
      },
    ]);

    renderWithQuery(<ServiceListPage blade={blade} />);
    await screen.findByRole("link", { name: "vault-one" });
    expect(await screen.findByRole("link", { name: "vault-two" })).toBeTruthy();
  });
});
