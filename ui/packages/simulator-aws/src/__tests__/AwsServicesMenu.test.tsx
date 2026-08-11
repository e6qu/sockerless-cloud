import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { AwsServicesMenu } from "../console/AwsConsole.js";
import type { NavGroup } from "../console/serviceCatalog.js";

/**
 * The full open/close, focus-trap, and Playwright-level behaviour of the
 * "Services" mega-menu is proven end to end in the package's e2e suite
 * (real layout, real focus, real keyboard events — jsdom does not lay
 * anything out). This unit-level suite instead pins the two things a render
 * test verifies more directly than an e2e assertion would: the trigger's
 * disclosure contract (`aria-haspopup`/`aria-expanded` before and after a
 * click) and the accessible-name/badge contract the AWS side navigation
 * already holds — a supported service gets a plain name, an unsupported one
 * states why in its own accessible name rather than relying on the
 * decorative pill alone.
 */

afterEach(cleanup);

const GROUPS: NavGroup[] = [
  {
    label: "Compute",
    items: [
      { label: "Lambda", to: "/ui/lambda" },
      { label: "EC2", to: "/ui/not-supported/ec2", supported: false },
    ],
  },
];

describe("AwsServicesMenu", () => {
  it("starts closed, with aria-haspopup and aria-expanded=false on the trigger", () => {
    render(
      <MemoryRouter>
        <AwsServicesMenu groups={GROUPS} />
      </MemoryRouter>,
    );
    const trigger = screen.getByRole("button", { name: "Services" });
    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens a labelled dialog on click and flips aria-expanded", () => {
    render(
      <MemoryRouter>
        <AwsServicesMenu groups={GROUPS} />
      </MemoryRouter>,
    );
    const trigger = screen.getByRole("button", { name: "Services" });
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("dialog", { name: "All services" })).toBeTruthy();
  });

  it("gives a supported service a plain accessible name and no badge", () => {
    render(
      <MemoryRouter>
        <AwsServicesMenu groups={GROUPS} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    const link = screen.getByRole("link", { name: "Lambda" });
    expect(link.textContent).not.toContain("Not supported");
  });

  it("marks an unsupported service with a real link and an explicit accessible name, with a 'Not supported' pill beside it", () => {
    render(
      <MemoryRouter>
        <AwsServicesMenu groups={GROUPS} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    const link = screen.getByRole("link", { name: "EC2, not supported in this simulator" });
    expect(link.getAttribute("href")).toBe("/ui/not-supported/ec2");
    // The "Not supported" pill sits beside the link, in reading order, rather
    // than fused into its accessible name — the accessible name already
    // states the fact explicitly via `ariaLabel`, and the pill is this
    // console's own visible affordance layered on top.
    expect(link.closest("li")?.textContent).toContain("Not supported");
  });

  it("narrows to matching services as the search field's value changes", () => {
    render(
      <MemoryRouter>
        <AwsServicesMenu groups={GROUPS} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    expect(screen.getByRole("link", { name: "Lambda" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "EC2, not supported in this simulator" })).toBeTruthy();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search services" }), { target: { value: "lambda" } });
    expect(screen.getByRole("link", { name: "Lambda" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: /EC2/ })).toBeNull();
  });
});
