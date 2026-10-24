import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { GcpStatus } from "../console/GcpConsole.js";

describe("GcpStatus", () => {
  // A substring test reports success for failure states: "unavailable" contains
  // "available", "inactive" contains "active". A tick beside a failed resource
  // stops an operator looking any further.
  it.each([
    ["Unavailable", "gc-status-error"],
    ["FAILED", "gc-status-error"],
    ["DELETING", "gc-status-progress"],
    ["DEPLOYING", "gc-status-progress"],
    ["ACTIVE", "gc-status-success"],
    ["Available", "gc-status-success"],
    ["TERMINATED", "gc-status-inactive"],
    ["INACTIVE", "gc-status-inactive"],
  ])("reports %s as %s", (status, expected) => {
    const { container } = render(<GcpStatus status={status} />);
    expect(container.querySelector(`.${expected}`)).not.toBeNull();
  });

  it("lets a caller that knows the meaning state it rather than inferring from wording", () => {
    const { container } = render(<GcpStatus status="Simulator project" kind="error" />);
    expect(container.querySelector(".gc-status-error")).not.toBeNull();
  });

  it("shows the state's icon beside its name, so the meaning does not rest on colour alone", () => {
    const { container } = render(<GcpStatus status="RUNNING" label="Running" />);
    expect(container.querySelector("svg")).not.toBeNull();
    expect(container.textContent).toBe("Running");
  });
});
