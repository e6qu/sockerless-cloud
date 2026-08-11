import { describe, expect, it } from "vitest";
import { SERVICE_CATALOG, catalogItemForPath } from "../catalog.js";
import { SERVICE_BLADES } from "../services.js";

describe("SERVICE_CATALOG", () => {
  const items = SERVICE_CATALOG.flatMap((group) => group.items);

  it("gives every item a unique route", () => {
    const routes = items.map((item) => item.to);
    expect(new Set(routes).size).toBe(routes.length);
  });

  it("gives every item a unique label, so the service menu never shows two of one name", () => {
    const labels = items.map((item) => item.label);
    expect(new Set(labels).size).toBe(labels.length);
  });

  it("routes every unsupported item to the shared not-implemented page", () => {
    for (const item of items.filter((candidate) => !candidate.supported)) {
      expect(item.to).toMatch(/^\/ui\/not-supported\/[a-z0-9-]+$/);
    }
  });

  it("routes every supported item to its own page rather than the not-implemented one", () => {
    for (const item of items.filter((candidate) => candidate.supported)) {
      expect(item.to).not.toMatch(/^\/ui\/not-supported\//);
    }
  });

  // The accuracy invariant: "Not supported" is a claim about the simulator, so
  // no service that has a blade — a descriptor naming the real Azure Resource
  // Manager operations the simulator serves for it — may carry that badge, and
  // every blade must reach the menu.
  it("never marks a service unsupported when a service blade reads its real ARM operations", () => {
    const bladedLabels = new Set(SERVICE_BLADES.map((blade) => blade.label));
    for (const item of items.filter((candidate) => !candidate.supported)) {
      expect(bladedLabels.has(item.label), `${item.label} has a blade but is marked unsupported`).toBe(false);
    }
  });

  it("puts every service blade in the menu at its own route", () => {
    const byRoute = new Map(items.map((item) => [item.to, item]));
    for (const blade of SERVICE_BLADES) {
      const item = byRoute.get(`/ui/${blade.slug}`);
      expect(item, `no menu item for the ${blade.label} blade`).toBeDefined();
      expect(item!.supported).toBe(true);
      expect(item!.label).toBe(blade.label);
      expect(item!.kind).toBe(blade.kind);
    }
  });

  // The full list of services this simulator genuinely has no Azure Resource
  // Manager routes for. Pinning it exactly is what keeps the badge honest in
  // both directions: implementing a provider and forgetting to give it a blade
  // fails here, and so does marking a working service unsupported.
  it("keeps every remaining unsupported item to a service the simulator has no ARM surface for", () => {
    const unsupported = items.filter((item) => !item.supported).map((item) => item.label);
    expect(unsupported).toEqual([
      "Management groups",
      "Azure Kubernetes Service",
      "Azure SQL",
      "Front Door and CDN profiles",
      "Cost Management + Billing",
      "Microsoft Defender for Cloud",
      "Azure DevOps organizations",
    ]);
  });

  it("carries at least one supported item per Azure resource family the simulator implements", () => {
    const supportedLabels = items.filter((item) => item.supported).map((item) => item.label);
    expect(supportedLabels).toEqual(
      expect.arrayContaining([
        "Subscriptions",
        "Resource groups",
        "Function Apps",
        "Container Apps",
        "Container App jobs",
        "Container registries",
        "Storage accounts",
        "Logs",
        "App registrations",
        "Virtual machines",
        "Azure Cosmos DB",
        "Key vaults",
        "Virtual networks",
      ]),
    );
  });

  it("resolves a catalog item by its route for both supported and unsupported items", () => {
    expect(catalogItemForPath("/ui/container-apps")?.label).toBe("Container App jobs");
    expect(catalogItemForPath("/ui/virtual-machines")?.label).toBe("Virtual machines");
    expect(catalogItemForPath("/ui/virtual-machines")?.supported).toBe(true);
    expect(catalogItemForPath("/ui/not-supported/azure-kubernetes-service")?.label).toBe("Azure Kubernetes Service");
    expect(catalogItemForPath("/ui/not-supported/azure-kubernetes-service")?.supported).toBe(false);
    expect(catalogItemForPath("/ui/does-not-exist")).toBeUndefined();
  });
});
