import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { AwsTabs } from "../console/AwsTabs.js";

/**
 * `AwsTabs` is a thin wrapper over the real Cloudscape `Tabs` component. Its
 * ARIA structure and keyboard contract are proven directly here (against
 * inert tab content), rather than only through Playwright, because the
 * detail pages that use it always require a live cloud read.
 *
 * Cloudscape's `Tabs` renders every panel `<div role="tabpanel">` up front —
 * visibility between them is CSS-driven, not an DOM `hidden` attribute — but
 * only the *active* tab's content is ever mounted inside its panel (the
 * `contentRenderStrategy="active"` default), so an inactive panel exists in
 * the accessibility tree but is empty. That is what this suite checks,
 * rather than a `hidden` attribute Cloudscape doesn't use.
 *
 * Cloudscape's own keyboard handling reads the legacy `KeyboardEvent.keyCode`
 * property, so the keyboard tests below pass it explicitly — jsdom does not
 * synthesize it from `key` the way a real browser does.
 */

afterEach(cleanup);

const ARROW_LEFT = { key: "ArrowLeft", keyCode: 37 };
const ARROW_RIGHT = { key: "ArrowRight", keyCode: 39 };
const HOME = { key: "Home", keyCode: 36 };
const END = { key: "End", keyCode: 35 };

function renderTabs() {
  return render(
    <AwsTabs
      ariaLabel="Test detail"
      tabs={[
        { id: "a", label: "First", content: <p data-testid="panel-a">First content</p> },
        { id: "b", label: "Second", content: <p data-testid="panel-b">Second content</p> },
        { id: "c", label: "Third", content: <p data-testid="panel-c">Third content</p> },
      ]}
    />,
  );
}

/** The panel Cloudscape renders for a given tab, found via that tab's own
 * `aria-controls`/id pairing — present in the DOM whether or not it is the
 * active tab (only its *content* is conditional). */
function panelFor(tabLabel: string): HTMLElement {
  const tab = screen.getByRole("tab", { name: tabLabel });
  const panelId = tab.getAttribute("aria-controls");
  const panel = panelId ? document.getElementById(panelId) : null;
  if (!panel) throw new Error(`no panel found for tab "${tabLabel}"`);
  return panel;
}

describe("AwsTabs", () => {
  it("exposes a tablist with one tab per entry and marks the first as selected by default", () => {
    renderTabs();
    expect(screen.getByRole("tablist", { name: "Test detail" })).toBeTruthy();
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual(["First", "Second", "Third"]);
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(tabs[1].getAttribute("aria-selected")).toBe("false");
    expect(tabs[2].getAttribute("aria-selected")).toBe("false");
  });

  it("mounts only the active panel's content, each panel labelled by its own tab", () => {
    renderTabs();
    expect(panelFor("First").textContent).toBe("First content");
    expect(panelFor("Second").textContent).toBe("");
    expect(panelFor("Third").textContent).toBe("");
    expect(screen.getByTestId("panel-a").textContent).toBe("First content");
    expect(screen.queryByTestId("panel-b")).toBeNull();
    expect(screen.queryByTestId("panel-c")).toBeNull();
    const firstTab = screen.getByRole("tab", { name: "First" });
    expect(panelFor("First").getAttribute("aria-labelledby")).toBe(firstTab.id);
  });

  it("switches the active tab and panel on click, mounting only the newly active content", () => {
    renderTabs();
    fireEvent.click(screen.getByRole("tab", { name: "Second" }));
    expect(screen.getByRole("tab", { name: "Second" }).getAttribute("aria-selected")).toBe("true");
    expect(panelFor("Second").textContent).toBe("Second content");
    expect(panelFor("First").textContent).toBe("");
    expect(screen.getByTestId("panel-b").textContent).toBe("Second content");
    expect(screen.queryByTestId("panel-a")).toBeNull();
  });

  it("keeps only the active tab in the page's Tab order (roving tabindex)", () => {
    renderTabs();
    const [first, second, third] = screen.getAllByRole("tab");
    expect(first.tabIndex).toBe(0);
    expect(second.tabIndex).toBe(-1);
    expect(third.tabIndex).toBe(-1);
    fireEvent.click(second);
    expect(first.tabIndex).toBe(-1);
    expect(second.tabIndex).toBe(0);
  });

  it("moves focus and selection with ArrowRight/ArrowLeft, wrapping at the ends", () => {
    renderTabs();
    const [first, second, third] = screen.getAllByRole("tab");
    first.focus();
    fireEvent.keyDown(first, ARROW_RIGHT);
    expect(document.activeElement).toBe(second);
    expect(second.getAttribute("aria-selected")).toBe("true");

    fireEvent.keyDown(second, ARROW_RIGHT);
    expect(document.activeElement).toBe(third);

    // Wraps forward from the last tab back to the first.
    fireEvent.keyDown(third, ARROW_RIGHT);
    expect(document.activeElement).toBe(first);

    // Wraps backward from the first tab to the last.
    fireEvent.keyDown(first, ARROW_LEFT);
    expect(document.activeElement).toBe(third);
  });

  it("jumps to the first and last tab with Home and End", () => {
    renderTabs();
    const [first, second, third] = screen.getAllByRole("tab");
    second.focus();
    fireEvent.keyDown(second, END);
    expect(document.activeElement).toBe(third);
    fireEvent.keyDown(third, HOME);
    expect(document.activeElement).toBe(first);
  });
});
