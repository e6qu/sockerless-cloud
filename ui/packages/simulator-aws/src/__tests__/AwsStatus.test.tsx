import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AwsStatus } from "../console/AwsConsole.js";

/**
 * Status is matched on whole words, not substrings. "Unavailable" contains
 * "available" and "Inactive" contains "active", so a substring test reports a
 * green success tick for a failure — which is worse than showing nothing,
 * because an operator reads the tick and stops looking.
 *
 * `AwsStatus` renders a real Cloudscape `StatusIndicator`, whose root element
 * carries a `status-<type>` class (a literal substring inside a build-hashed
 * class name, e.g. `awsui_status-error_1cbgc_1nbms_212`) — `kindOf` reads
 * that literal substring rather than depending on the hash suffix.
 */
describe("AwsStatus", () => {
  function kindOf(status: string): string {
    const { container } = render(<AwsStatus status={status} />);
    const element = container.querySelector('[class*="status-"]');
    return element?.className.match(/status-([a-z-]+)/)?.[1] ?? "";
  }

  it("reports failure states as failures even when a success word is a substring", () => {
    expect(kindOf("Unavailable")).toBe("error");
    expect(kindOf("Inactive")).toBe("error");
    expect(kindOf("Deactivated")).toBe("error");
    expect(kindOf("STOPPED")).toBe("error");
  });

  it("reports genuine success states as successes", () => {
    expect(kindOf("Running")).toBe("success");
    expect(kindOf("Available")).toBe("success");
    expect(kindOf("Active")).toBe("success");
  });

  it("reports transitional states as in progress", () => {
    expect(kindOf("Pending")).toBe("in-progress");
    expect(kindOf("Provisioning")).toBe("in-progress");
  });

  it("carries a shape so the state does not depend on colour alone", () => {
    const { container } = render(<AwsStatus status="Unavailable" />);
    const status = container.querySelector('[class*="status-error"]');
    expect(status?.textContent).toContain("Unavailable");
    // The icon is a distinct shape per state — a screen reader ignores it
    // (aria-hidden, since no iconAriaLabel is set) and reads the word, while
    // a sighted user reads the shape.
    const icon = status?.querySelector("svg");
    expect(icon).not.toBeNull();
    // The error and success shapes differ, so state is legible without colour.
    const success = render(<AwsStatus status="Running" />).container.querySelector(
      '[class*="status-success"] svg path',
    );
    const error = icon?.querySelector("path");
    expect(success?.getAttribute("d")).not.toBe(error?.getAttribute("d"));
  });
});
