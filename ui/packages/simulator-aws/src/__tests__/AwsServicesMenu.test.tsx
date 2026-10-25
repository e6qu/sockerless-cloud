import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ServicesMenu, ServicesMenuTrigger } from "../console/AwsShell.js";

/**
 * The open/close, focus-trap, and layout behaviour of the Services menu is
 * proven in the browser suite. This suite pins the disclosure contract on the
 * trigger and the catalog the panel lists.
 */

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <MemoryRouter>
      <ServicesMenuTrigger open={open} onToggle={() => setOpen((current) => !current)} />
      <ServicesMenu open={open} onClose={() => setOpen(false)} />
    </MemoryRouter>
  );
}

describe("ServicesMenu", () => {
  it("starts closed, with aria-haspopup and aria-expanded=false on the trigger", () => {
    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "Services" });
    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens a labelled dialog listing every service when nothing was visited", () => {
    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "Services" });
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("dialog", { name: "Services" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "All services" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Lambda" }).getAttribute("href")).toBe("/ui/lambda");
  });

  it("names a service with no console page in its accessible name", () => {
    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    const link = screen.getByRole("link", { name: "Elastic Kubernetes Service, no page in this console" });
    expect(link.getAttribute("href")).toBe("/ui/not-supported/eks");
  });

  it("narrows to one category", () => {
    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    fireEvent.click(screen.getByRole("link", { name: "Storage" }));
    expect(screen.getByRole("heading", { name: "Storage" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "S3" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Lambda" })).toBeNull();
  });

  it("narrows to matching services as the search field changes", () => {
    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Services" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "Search services" }), { target: { value: "queue" } });
    expect(screen.getByRole("link", { name: "Simple Queue Service" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Lambda" })).toBeNull();
  });
});
