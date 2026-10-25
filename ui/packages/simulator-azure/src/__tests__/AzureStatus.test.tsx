import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge, statusKind } from "../portal/AzurePortal.js";

describe("StatusBadge", () => {
  // A substring test reports success for failure states: "unavailable"
  // contains "available", and "inactive" contains "active".
  it.each([
    ["Unavailable", "error"],
    ["Inactive", "error"],
    ["Failed", "error"],
    ["Deallocated", "error"],
    ["Available", "success"],
    ["Running", "success"],
    ["Succeeded", "success"],
    ["Provisioning", "warning"],
    ["Accepted", "warning"],
    ["Unknown", "inactive"],
  ])("reports %s as %s", (status, expected) => {
    expect(statusKind(status)).toBe(expected);
    const { container } = render(<StatusBadge status={status} />);
    expect(container.querySelector(`.az-status-${expected}`)).not.toBeNull();
  });

  it("takes the caller's meaning over the wording", () => {
    const { container } = render(<StatusBadge status="Simulator subscription" kind="error" />);
    expect(container.querySelector(".az-status-error")).not.toBeNull();
  });

  it("draws a different shape per state, so meaning does not rest on colour", () => {
    const success = render(<StatusBadge status="Available" />).container.querySelector("svg[aria-hidden] path");
    const error = render(<StatusBadge status="Unavailable" />).container.querySelector("svg[aria-hidden] path");
    expect(success).not.toBeNull();
    expect(success?.getAttribute("d")).not.toBe(error?.getAttribute("d"));
  });
});
