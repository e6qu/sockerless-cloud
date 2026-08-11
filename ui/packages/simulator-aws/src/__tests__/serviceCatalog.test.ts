import { describe, expect, it } from "vitest";
import { filterNavGroups, type NavGroup } from "../console/serviceCatalog.js";

/**
 * The "Services" mega-menu's search field narrows the catalog live as an
 * operator types, the way the real console's flyout does. The filtering
 * itself is plain data logic — independent of the dialog, focus trap, and
 * keyboard handling around it — so it is proven directly here rather than
 * only through a rendered dialog.
 */

const GROUPS: NavGroup[] = [
  {
    label: "Compute",
    items: [
      { label: "Lambda", to: "/ui/lambda" },
      { label: "EC2", to: "/ui/not-supported/ec2", supported: false },
    ],
  },
  {
    label: "Containers",
    items: [{ label: "Elastic Container Service", to: "/ui/ecs" }],
  },
  {
    label: "Storage",
    items: [{ label: "Simple Storage Service", to: "/ui/s3" }],
  },
];

describe("filterNavGroups", () => {
  it("returns every group and item unchanged for an empty query", () => {
    expect(filterNavGroups(GROUPS, "")).toBe(GROUPS);
  });

  it("returns every group and item unchanged for a whitespace-only query", () => {
    expect(filterNavGroups(GROUPS, "   ")).toBe(GROUPS);
  });

  it("matches by service name, case-insensitively", () => {
    const result = filterNavGroups(GROUPS, "LAMBDA");
    expect(result).toEqual([{ label: "Compute", items: [{ label: "Lambda", to: "/ui/lambda" }] }]);
  });

  it("matches by substring, not only a full-name match", () => {
    const result = filterNavGroups(GROUPS, "stor");
    expect(result.map((g) => g.label)).toEqual(["Storage"]);
    expect(result[0]!.items).toEqual([{ label: "Simple Storage Service", to: "/ui/s3" }]);
  });

  it("drops a category entirely when none of its services match, rather than rendering it empty", () => {
    const result = filterNavGroups(GROUPS, "lambda");
    expect(result.map((g) => g.label)).toEqual(["Compute"]);
  });

  it("keeps an unsupported service's supported:false flag intact through filtering", () => {
    const result = filterNavGroups(GROUPS, "ec2");
    expect(result).toEqual([
      { label: "Compute", items: [{ label: "EC2", to: "/ui/not-supported/ec2", supported: false }] },
    ]);
  });

  it("returns no groups when nothing matches", () => {
    expect(filterNavGroups(GROUPS, "zzzznotaservice")).toEqual([]);
  });

  it("never matches on the category label itself", () => {
    // "Compute" is a category name, not a service name; a search for it must
    // not keep every service in that category visible.
    expect(filterNavGroups(GROUPS, "compute")).toEqual([]);
  });
});
