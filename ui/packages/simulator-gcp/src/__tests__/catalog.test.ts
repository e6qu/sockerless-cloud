import { describe, expect, it } from "vitest";
import { PRODUCTS, locate, productHome, searchCatalog } from "../console/catalog.js";
import { filterRows, parseChip, type GcpColumn } from "../console/GcpTable.js";

describe("locate", () => {
  it("finds the product and the current navigation item for a list page", () => {
    const { product, item } = locate("/ui/compute/instances");
    expect(product?.name).toBe("Compute Engine");
    expect(item?.label).toBe("VM instances");
  });

  it("marks the list's item current on its detail and create pages", () => {
    expect(locate("/ui/compute/instancesDetail/zones/us-central1-a/instances/vm-1").item?.label).toBe("VM instances");
    expect(locate("/ui/storage/create-bucket").item?.label).toBe("Buckets");
    expect(locate("/ui/storage/browser/my-bucket").item?.label).toBe("Buckets");
  });

  it("prefers the longest matching prefix", () => {
    expect(locate("/ui/run/jobs/details/us-central1/job-1/executions").item?.label).toBe("Jobs");
    expect(locate("/ui/run/detail/us-central1/svc").item?.label).toBe("Services");
  });

  it("places the Cloud overview pages outside every product", () => {
    expect(locate("/ui/welcome").product).toBeUndefined();
    expect(locate("/ui/home/dashboard").product).toBeUndefined();
  });

  it("gives every product a home page among its own navigation", () => {
    for (const product of PRODUCTS) {
      expect(locate(productHome(product)).product?.id).toBe(product.id);
    }
  });
});

describe("searchCatalog", () => {
  it("ranks a product whose name starts with the query first", () => {
    expect(searchCatalog("compute")[0]?.label).toBe("Compute Engine");
  });

  it("finds a product's pages", () => {
    const labels = searchCatalog("subscriptions").map((entry) => entry.label);
    expect(labels).toContain("Subscriptions");
  });

  it("returns nothing for an empty query or one nothing matches", () => {
    expect(searchCatalog("  ")).toEqual([]);
    expect(searchCatalog("zzzz-no-such-product")).toEqual([]);
  });
});

interface Row {
  name: string;
  zone: string;
}

const columns: GcpColumn<Row>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "zone", header: "Zone", cell: (row) => row.zone, value: (row) => row.zone },
];
const rows: Row[] = [
  { name: "web-1", zone: "us-central1-a" },
  { name: "db-1", zone: "europe-west1-b" },
];

describe("filter bar", () => {
  it("parses Property: value against the column headers", () => {
    expect(parseChip("Zone: europe", columns)).toEqual({ column: "zone", value: "europe" });
    expect(parseChip("web", columns)).toEqual({ value: "web" });
    expect(parseChip("Unknown: x", columns)).toEqual({ value: "Unknown: x" });
  });

  it("keeps the rows that match every chip", () => {
    expect(filterRows(rows, columns, [{ column: "zone", value: "us-" }]).map((row) => row.name)).toEqual(["web-1"]);
    expect(filterRows(rows, columns, [{ value: "1" }]).length).toBe(2);
    expect(filterRows(rows, columns, [{ value: "db" }, { column: "zone", value: "us" }])).toEqual([]);
  });
});
