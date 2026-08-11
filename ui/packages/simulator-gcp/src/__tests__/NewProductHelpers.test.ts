import { describe, expect, it } from "vitest";
import { instanceZone } from "../pages/ComputeEnginePage.js";
import { formatFirewallActions } from "../pages/VpcNetworkPage.js";
import { datasetId, formatEpochMillis } from "../pages/BigQueryPage.js";
import { jobStateLabel } from "../pages/DataflowPage.js";
import { buildDuration } from "../pages/CloudBuildPage.js";
import { triggerDestination, triggerEventType } from "../pages/EventarcPage.js";
import { replicationLabel } from "../pages/SecretManagerPage.js";
import { addBinding, flattenBindings, removeBinding } from "../pages/IamPage.js";

// The pure readers each new product page uses to turn a real API resource into
// what the console shows. They are exercised here against the exact shapes the
// simulator returns, so a wire-shape change is caught apart from the rendering.

describe("instanceZone", () => {
  it("reads the bare zone out of the resource's zone URL", () => {
    expect(instanceZone({ name: "vm", zone: "https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a" })).toBe(
      "us-central1-a",
    );
  });

  it("is empty when the resource carries no zone", () => {
    expect(instanceZone({ name: "vm" })).toBe("");
  });
});

describe("formatFirewallActions", () => {
  it("renders protocol:ports the way gcloud spells a rule", () => {
    expect(formatFirewallActions([{ IPProtocol: "tcp", ports: ["80", "443"] }])).toBe("tcp:80,443");
  });

  it("renders a protocol with no ports as the protocol alone", () => {
    expect(formatFirewallActions([{ IPProtocol: "icmp" }])).toBe("icmp");
  });

  it("reports an em dash for an absent or empty rule list", () => {
    expect(formatFirewallActions(undefined)).toBe("—");
    expect(formatFirewallActions([])).toBe("—");
  });
});

describe("BigQuery readers", () => {
  it("reads the dataset ID out of the datasetReference", () => {
    expect(datasetId({ datasetReference: { projectId: "p", datasetId: "sales" }, id: "p:sales" })).toBe("sales");
  });

  it("falls back to the composite id when no reference is present", () => {
    expect(datasetId({ id: "p:sales" })).toBe("p:sales");
  });

  it("renders BigQuery's epoch-milliseconds strings as a date-time", () => {
    expect(formatEpochMillis("0")).toBe(new Date(0).toLocaleString());
    expect(formatEpochMillis(undefined)).toBe("—");
    expect(formatEpochMillis("not-a-number")).toBe("not-a-number");
  });
});

describe("jobStateLabel", () => {
  it("renders the Dataflow JobState enum in human form", () => {
    expect(jobStateLabel("JOB_STATE_RUNNING")).toBe("RUNNING");
    expect(jobStateLabel("JOB_STATE_CANCELLING")).toBe("CANCELLING");
    expect(jobStateLabel(undefined)).toBe("Unknown");
  });
});

describe("buildDuration", () => {
  it("measures the elapsed time between the build's two RFC 3339 stamps", () => {
    expect(
      buildDuration({ id: "b", startTime: "2026-01-01T00:00:00Z", finishTime: "2026-01-01T00:00:45Z" }),
    ).toBe("45s");
    expect(
      buildDuration({ id: "b", startTime: "2026-01-01T00:00:00Z", finishTime: "2026-01-01T00:02:05Z" }),
    ).toBe("2m 5s");
  });

  it("reports an em dash while the build has not finished", () => {
    expect(buildDuration({ id: "b", startTime: "2026-01-01T00:00:00Z" })).toBe("—");
  });
});

describe("Eventarc readers", () => {
  it("reads the event type out of the `type` event filter", () => {
    expect(
      triggerEventType({
        name: "t",
        eventFilters: [
          { attribute: "type", value: "google.cloud.pubsub.topic.v1.messagePublished" },
          { attribute: "topic", value: "projects/p/topics/t" },
        ],
      }),
    ).toBe("google.cloud.pubsub.topic.v1.messagePublished");
    expect(triggerEventType({ name: "t" })).toBe("—");
  });

  it("names the destination oneof arm rather than the wrapper", () => {
    expect(
      triggerDestination({ name: "t", destination: { cloudRun: { service: "svc", region: "us-central1" } } }),
    ).toBe("Cloud Run: svc (us-central1)");
    expect(
      triggerDestination({ name: "t", destination: { cloudFunction: "projects/p/locations/l/functions/fn" } }),
    ).toBe("Cloud Run function: fn");
    expect(triggerDestination({ name: "t" })).toBe("—");
  });
});

describe("replicationLabel", () => {
  it("names the replication oneof arm the secret carries", () => {
    expect(replicationLabel({ name: "s", replication: { automatic: {} } })).toBe("Automatic");
    expect(
      replicationLabel({ name: "s", replication: { userManaged: { replicas: [{ location: "us-central1" }] } } }),
    ).toBe("User managed (us-central1)");
    expect(replicationLabel({ name: "s" })).toBe("—");
  });
});

describe("IAM allow-policy editing", () => {
  const policy = {
    etag: "abc",
    version: 1,
    bindings: [{ role: "roles/viewer", members: ["user:a@example.com", "user:b@example.com"] }],
  };

  it("flattens the role-grouped bindings into per-principal grants", () => {
    expect(flattenBindings(policy)).toEqual([
      { member: "user:a@example.com", role: "roles/viewer" },
      { member: "user:b@example.com", role: "roles/viewer" },
    ]);
    expect(flattenBindings(undefined)).toEqual([]);
  });

  it("adds a principal to an existing role binding without touching the etag", () => {
    const next = addBinding(policy, "user:c@example.com", "roles/viewer");
    expect(next.etag).toBe("abc");
    expect(next.bindings).toEqual([
      { role: "roles/viewer", members: ["user:a@example.com", "user:b@example.com", "user:c@example.com"] },
    ]);
    // The policy the page read is not mutated in place.
    expect(policy.bindings[0].members).toHaveLength(2);
  });

  it("creates a new binding for a role the policy does not yet carry", () => {
    const next = addBinding(policy, "user:c@example.com", "roles/editor");
    expect(next.bindings).toContainEqual({ role: "roles/editor", members: ["user:c@example.com"] });
  });

  it("is idempotent when the principal already holds the role", () => {
    const next = addBinding(policy, "user:a@example.com", "roles/viewer");
    expect(next.bindings?.[0].members).toEqual(["user:a@example.com", "user:b@example.com"]);
  });

  it("removes a principal and drops the binding once its last principal is gone", () => {
    const one = removeBinding(policy, "user:a@example.com", "roles/viewer");
    expect(one.bindings).toEqual([{ role: "roles/viewer", members: ["user:b@example.com"] }]);
    const none = removeBinding(one, "user:b@example.com", "roles/viewer");
    expect(none.bindings).toEqual([]);
  });
});
