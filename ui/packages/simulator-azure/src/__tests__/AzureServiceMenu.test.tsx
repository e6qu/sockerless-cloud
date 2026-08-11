import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it } from "vitest";
import { AzureServiceMenu } from "../portal/AzurePortal.js";

describe("AzureServiceMenu", () => {
  // The vitest config runs without testing-library's auto-cleanup globals, so
  // each render must be unmounted between tests to keep screen queries
  // scoped to one render.
  afterEach(cleanup);

  const groups = [
    {
      label: "Compute",
      items: [
        { label: "Function Apps", to: "/ui/functions", supported: true },
        { label: "Virtual machines", to: "/ui/not-supported/virtual-machines", supported: false },
      ],
    },
  ];

  it("gives a supported service a plain accessible name and no badge", () => {
    render(
      <MemoryRouter>
        <AzureServiceMenu flat={[]} groups={groups} />
      </MemoryRouter>,
    );
    const link = screen.getByRole("link", { name: "Function Apps" });
    expect(link.textContent).not.toContain("Not supported");
  });

  it("marks an unsupported service with a real link, a text badge, and an explicit accessible name", () => {
    render(
      <MemoryRouter>
        <AzureServiceMenu flat={[]} groups={groups} />
      </MemoryRouter>,
    );
    // The accessible name comes from aria-label, not the visible label alone
    // — a screen reader announces the reason, not just "Virtual machines".
    const link = screen.getByRole("link", { name: "Virtual machines, not supported in this simulator" });
    expect(link.getAttribute("href")).toBe("/ui/not-supported/virtual-machines");
    // The badge is real text content in the DOM, not a colour-only cue.
    expect(link.textContent).toContain("Not supported");
    expect(link.querySelector("svg[aria-hidden]")).not.toBeNull();
  });
});
