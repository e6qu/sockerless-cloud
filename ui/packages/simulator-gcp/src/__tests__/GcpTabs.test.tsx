import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { GcpTabs } from "../console/GcpTabs.js";

afterEach(cleanup);

function renderTabs() {
  return render(
    <GcpTabs
      label="Job detail"
      tabs={[
        { id: "details", label: "Details", content: <p>Details content</p> },
        { id: "executions", label: "Executions", content: <p>Executions content</p> },
        { id: "logs", label: "Logs", content: <p>Logs content</p> },
      ]}
    />,
  );
}

describe("GcpTabs", () => {
  it("follows the WAI-ARIA tabs pattern: a tablist of tabs owning one labelled tabpanel", () => {
    renderTabs();
    const tablist = screen.getByRole("tablist", { name: "Job detail" });
    expect(tablist).toBeDefined();
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual(["Details", "Executions", "Logs"]);
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(tabs[1].getAttribute("aria-selected")).toBe("false");
    const panel = screen.getByRole("tabpanel");
    expect(panel.getAttribute("aria-labelledby")).toBe(tabs[0].id);
    expect(screen.getByText("Details content")).toBeDefined();
  });

  it("defaults to the first tab and renders only the active panel's content", () => {
    renderTabs();
    expect(screen.getByText("Details content")).toBeDefined();
    expect(screen.queryByText("Executions content")).toBeNull();
  });

  it("switches panels on click and keeps a roving tabindex", () => {
    renderTabs();
    const [details, executions] = screen.getAllByRole("tab");
    expect(details.getAttribute("tabindex")).toBe("0");
    expect(executions.getAttribute("tabindex")).toBe("-1");

    fireEvent.click(executions);

    expect(screen.getByText("Executions content")).toBeDefined();
    expect(screen.queryByText("Details content")).toBeNull();
    expect(executions.getAttribute("aria-selected")).toBe("true");
    expect(executions.getAttribute("tabindex")).toBe("0");
    expect(details.getAttribute("aria-selected")).toBe("false");
    expect(details.getAttribute("tabindex")).toBe("-1");
  });

  it("moves and activates with ArrowRight/ArrowLeft, wrapping at the ends", () => {
    renderTabs();
    const [details, executions, logs] = screen.getAllByRole("tab");

    fireEvent.keyDown(details, { key: "ArrowRight" });
    expect(screen.getByText("Executions content")).toBeDefined();
    expect(document.activeElement).toBe(executions);

    fireEvent.keyDown(executions, { key: "ArrowRight" });
    expect(screen.getByText("Logs content")).toBeDefined();
    expect(document.activeElement).toBe(logs);

    // Wraps from the last tab back to the first.
    fireEvent.keyDown(logs, { key: "ArrowRight" });
    expect(screen.getByText("Details content")).toBeDefined();
    expect(document.activeElement).toBe(details);

    // Wraps from the first tab back to the last going left.
    fireEvent.keyDown(details, { key: "ArrowLeft" });
    expect(screen.getByText("Logs content")).toBeDefined();
    expect(document.activeElement).toBe(logs);
  });

  it("jumps to the first/last tab with Home/End", () => {
    renderTabs();
    const [details, , logs] = screen.getAllByRole("tab");

    fireEvent.keyDown(details, { key: "End" });
    expect(screen.getByText("Logs content")).toBeDefined();
    expect(document.activeElement).toBe(logs);

    fireEvent.keyDown(logs, { key: "Home" });
    expect(screen.getByText("Details content")).toBeDefined();
    expect(document.activeElement).toBe(details);
  });
});
