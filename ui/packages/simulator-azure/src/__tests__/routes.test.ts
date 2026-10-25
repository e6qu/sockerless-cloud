import { describe, expect, it } from "vitest";
import { parseResourceId } from "../azure.js";
import { armPathOf } from "../portal/arm.js";
import { resourceRoute, splitResourcePath } from "../portal/routes.js";

const SUB = "/subscriptions/00000000-0000-0000-0000-000000000001";
const ACCOUNT = `${SUB}/resourceGroups/rg1/providers/Microsoft.Storage/storageAccounts/acct1`;

describe("resource routes", () => {
  it.each([
    [`${SUB}/overview`, SUB, ["overview"]],
    [`${SUB}/resourceGroups/rg1/users`, `${SUB}/resourceGroups/rg1`, ["users"]],
    [`${ACCOUNT}/containers/logs`, ACCOUNT, ["containers", "logs"]],
    [ACCOUNT, ACCOUNT, []],
  ])("splits %s", (splat, id, section) => {
    expect(splitResourcePath(splat.slice(1))).toEqual({ id, section });
  });

  it("round-trips a resource route", () => {
    expect(splitResourcePath(resourceRoute(ACCOUNT, "tags").replace("/ui/resource/", ""))).toEqual({ id: ACCOUNT, section: ["tags"] });
  });
});

describe("parseResourceId", () => {
  it("reads a subscription, a resource group and a provider resource", () => {
    expect(parseResourceId(SUB)).toMatchObject({ type: "Microsoft.Resources/subscriptions" });
    expect(parseResourceId(`${SUB}/resourceGroups/rg1`)).toMatchObject({ type: "Microsoft.Resources/resourceGroups", name: "rg1", resourceGroup: "rg1" });
    expect(parseResourceId(ACCOUNT)).toMatchObject({ type: "Microsoft.Storage/storageAccounts", name: "acct1", resourceGroup: "rg1" });
    expect(parseResourceId(`${SUB}/resourceGroups/rg1/providers/Microsoft.ServiceBus/namespaces/ns/queues/q1`)).toMatchObject({
      type: "Microsoft.ServiceBus/namespaces/queues",
      name: "q1",
    });
  });
});

describe("armPathOf", () => {
  it("keeps the path and query of an absolute ARM URL", () => {
    expect(armPathOf("http://localhost:19331/subscriptions/x/resources?api-version=2021-04-01&$skiptoken=abc")).toBe(
      "/subscriptions/x/resources?api-version=2021-04-01&$skiptoken=abc",
    );
  });
});
