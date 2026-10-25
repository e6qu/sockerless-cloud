import { describe, expect, it } from "vitest";
import {
  SERVICES,
  filterServices,
  findService,
  navLinkForPath,
  searchConsole,
  serviceForPath,
} from "../console/serviceCatalog.js";
import { crumbTrail } from "../console/AwsApp.js";
import { formatAccountId, principalLabel } from "../console/AwsShell.js";
import { filterRows, type AwsColumn } from "../console/AwsTable.js";

/**
 * The shell's routing data — which service owns a route, which side-navigation
 * entry it sits under, what the breadcrumb trail says, and what the unified
 * search finds — is plain data logic, proven directly here.
 */

describe("serviceForPath", () => {
  it("finds a service by its own routes", () => {
    expect(serviceForPath("/ui/lambda")?.id).toBe("lambda");
    expect(serviceForPath("/ui/lambda/my-function")?.id).toBe("lambda");
  });

  it("files routes the real console files under another service's navigation", () => {
    expect(serviceForPath("/ui/logs")?.id).toBe("cloudwatch");
    expect(serviceForPath("/ui/elb/target-groups")?.id).toBe("ec2");
    expect(serviceForPath("/ui/scheduler/groups")?.id).toBe("eventbridge");
  });

  it("answers nothing for Console Home", () => {
    expect(serviceForPath("/ui/")).toBeUndefined();
  });

  it("finds a service with no console page from its not-supported route", () => {
    expect(serviceForPath("/ui/not-supported/eks")?.name).toBe("Elastic Kubernetes Service");
  });

  it("never claims a prefix that only shares leading characters", () => {
    expect(serviceForPath("/ui/ec2x")).toBeUndefined();
  });
});

describe("navLinkForPath", () => {
  const iam = findService("iam")!;
  it("matches a side-navigation route exactly", () => {
    expect(navLinkForPath(iam, "/ui/iam/roles")).toEqual({ link: { text: "Roles", href: "/ui/iam/roles", create: "Create role" }, exact: true });
  });

  it("files a detail route under its nearest parent entry", () => {
    expect(navLinkForPath(iam, "/ui/iam/roles/app")?.link.text).toBe("Roles");
    expect(navLinkForPath(iam, "/ui/iam/users/alice")?.link.text).toBe("Users");
  });
});

describe("crumbTrail", () => {
  it("roots the trail at the service and names the page", () => {
    expect(crumbTrail(findService("ec2"), "/ui/ec2")).toEqual([
      { text: "EC2", href: "/ui/ec2/home" },
      { text: "Instances" },
    ]);
  });

  it("names the resource on a detail route", () => {
    expect(crumbTrail(findService("iam"), "/ui/iam/roles/app%20role")).toEqual([
      { text: "IAM", href: "/ui/iam" },
      { text: "Roles", href: "/ui/iam/roles" },
      { text: "app role" },
    ]);
  });

  it("names the cluster and the service on an ECS service route", () => {
    expect(crumbTrail(findService("ecs"), "/ui/ecs/services/default/web").map((crumb) => crumb.text)).toEqual([
      "Elastic Container Service",
      "Services",
      "default",
      "web",
    ]);
  });

  it("is empty on Console Home", () => {
    expect(crumbTrail(undefined, "/ui/")).toEqual([]);
  });
});

describe("searchConsole", () => {
  it("ranks a service ahead of a feature with the same name", () => {
    const results = searchConsole("lambda");
    expect(results[0]).toMatchObject({ kind: "service", label: "Lambda" });
  });

  it("finds a feature by its own name and labels it with its service", () => {
    const roles = searchConsole("roles").find((entry) => entry.kind === "feature");
    expect(roles).toMatchObject({ label: "Roles", description: "IAM", href: "/ui/iam/roles" });
  });

  it("finds nothing for an empty query", () => {
    expect(searchConsole("   ")).toEqual([]);
  });
});

describe("filterServices", () => {
  it("narrows a category to matching services, sorted by name", () => {
    expect(filterServices("", "Containers").map((service) => service.id)).toEqual(["ecr", "ecs", "eks"]);
  });

  it("matches the description as well as the name", () => {
    expect(filterServices("nosql").map((service) => service.id)).toEqual(["dynamodb"]);
  });

  it("files every service under a category the Services menu lists", () => {
    for (const service of SERVICES) expect(service.category).toBeTruthy();
  });
});

describe("account labels", () => {
  it("prints an account ID in the console's dashed form", () => {
    expect(formatAccountId("123456789012")).toBe("1234-5678-9012");
  });

  it("names an assumed-role principal by role and session", () => {
    expect(principalLabel("arn:aws:sts::123456789012:assumed-role/ConsoleOperator/console")).toBe("ConsoleOperator/console");
    expect(principalLabel("arn:aws:iam::123456789012:user/team/alice")).toBe("alice");
  });
});

describe("filterRows", () => {
  const columns: AwsColumn<{ name: string; state: string }>[] = [
    { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
    { id: "state", header: "State", cell: (row) => row.state, value: (row) => row.state },
  ];
  const rows = [
    { name: "web-1", state: "running" },
    { name: "web-2", state: "stopped" },
    { name: "db", state: "running" },
  ];

  it("applies a property token to its own column", () => {
    expect(filterRows(rows, { operation: "and", tokens: [{ propertyKey: "state", operator: "=", value: "running" }] }, columns)).toEqual([
      rows[0],
      rows[2],
    ]);
  });

  it("matches free text against every column", () => {
    expect(filterRows(rows, { operation: "and", tokens: [{ operator: ":", value: "web" }] }, columns)).toEqual([rows[0], rows[1]]);
  });

  it("joins tokens with the query's operation", () => {
    const tokens = [
      { propertyKey: "name", operator: "=", value: "db" },
      { propertyKey: "state", operator: "=", value: "stopped" },
    ];
    expect(filterRows(rows, { operation: "or", tokens }, columns)).toEqual([rows[1], rows[2]]);
    expect(filterRows(rows, { operation: "and", tokens }, columns)).toEqual([]);
  });

  it("excludes with a negated free-text token", () => {
    expect(filterRows(rows, { operation: "and", tokens: [{ operator: "!:", value: "web" }] }, columns)).toEqual([rows[2]]);
  });
});
