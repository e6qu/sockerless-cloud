import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AzureStatus } from "../portal/AzurePortal.js";

describe("AzureStatus", () => {
  // A substring test reports success for failure states: "unavailable"
  // contains "available", and "inactive" contains "active". A tick beside a
  // failed resource stops an operator looking any further.
  it.each([
    ["Unavailable", "az-status-error"],
    ["Inactive", "az-status-error"],
    ["Failed", "az-status-error"],
    ["Available", "az-status-success"],
    ["Running", "az-status-success"],
    ["Provisioning", "az-status-warning"],
  ])("reports %s as %s", (status, expected) => {
    const { container } = render(<AzureStatus status={status} />);
    expect(container.querySelector(`.${expected}`)).not.toBeNull();
  });

  it("lets a caller that knows the meaning state it rather than inferring from wording", () => {
    const { container } = render(<AzureStatus status="Simulator subscription" kind="error" />);
    expect(container.querySelector(".az-status-error")).not.toBeNull();
  });

  it("carries a shape so the meaning does not rest on colour alone", () => {
    const { container } = render(<AzureStatus status="Available" />);
    // A distinct icon per state — a screen reader ignores it (aria-hidden) and
    // reads the word, while a sighted user reads the shape.
    const success = container.querySelector("svg[aria-hidden] path");
    expect(success).not.toBeNull();
    // The success and error shapes differ, so state is legible without colour.
    const error = render(<AzureStatus status="Unavailable" />).container.querySelector("svg[aria-hidden] path");
    expect(success?.getAttribute("d")).not.toBe(error?.getAttribute("d"));
  });
});
