import { describe, expect, it } from "vitest";
import { CATEGORIES, DEFAULT_FAVORITES, HOME_SERVICES, SERVICES, SERVICE_BY_ID, matchesService } from "../catalog.js";
import { SERVICE_BLADES, bladeForResource, browseRoute } from "../services.js";
import { CREATE_SPECS } from "../pages/create/specs.js";

describe("catalog", () => {
  it("lists every service blade under a known category", () => {
    for (const blade of SERVICE_BLADES) {
      const service = SERVICE_BY_ID.get(blade.slug);
      expect(service, blade.slug).toBeDefined();
      expect(CATEGORIES).toContain(service!.category);
      expect(service!.route).toBe(browseRoute(blade));
    }
  });

  it("gives every service a unique id, label and route", () => {
    for (const key of ["id", "label", "route"] as const) {
      const values = SERVICES.map((service) => service[key]);
      expect(new Set(values).size, key).toBe(values.length);
    }
  });

  it("names only real services in Favorites and Home", () => {
    for (const id of [...DEFAULT_FAVORITES, ...HOME_SERVICES]) expect(SERVICE_BY_ID.has(id), id).toBe(true);
  });

  it("matches services by name, description and keyword", () => {
    const storage = SERVICE_BY_ID.get("storage-accounts")!;
    expect(matchesService(storage, "storage")).toBe(true);
    expect(matchesService(storage, "blob")).toBe(true);
    expect(matchesService(storage, "kubernetes")).toBe(false);
  });

  it("splits Microsoft.Web/sites between App Service and Function App on kind", () => {
    expect(bladeForResource("Microsoft.Web/sites", "functionapp,linux")?.slug).toBe("function-apps");
    expect(bladeForResource("Microsoft.Web/sites", "app,linux")?.slug).toBe("app-service");
    expect(bladeForResource("microsoft.storage/storageaccounts")?.slug).toBe("storage-accounts");
  });

  it("opens every create wizard from a listed service", () => {
    for (const spec of CREATE_SPECS) expect(SERVICE_BY_ID.has(spec.serviceId), spec.id).toBe(true);
  });
});
