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
    ["DELETING", "gc-status-error"],
    ["ACTIVE", "gc-status-success"],
    ["Available", "gc-status-success"],
    ["DEPLOYING", "gc-status-warning"],
  ])("reports %s as %s", (status, expected) => {
    const { container } = render(<GcpStatus status={status} />);
    expect(container.querySelector(`.${expected}`)).not.toBeNull();
  });

  it("lets a caller that knows the meaning state it rather than inferring from wording", () => {
    const { container } = render(<GcpStatus status="Simulator project" kind="error" />);
    expect(container.querySelector(".gc-status-error")).not.toBeNull();
  });

  it("carries a glyph so the meaning does not rest on colour alone", () => {
    const { container } = render(<GcpStatus status="ACTIVE" />);
    expect(container.textContent).toContain("✔");
  });
});
