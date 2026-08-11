import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { MemoryRouter } from "react-router";
import { GcpNavDrawer } from "../console/GcpNavDrawer.js";

afterEach(cleanup);

function renderDrawer() {
  const triggerRef = createRef<HTMLButtonElement>();
  return render(
    <MemoryRouter>
      <button ref={triggerRef} type="button">trigger</button>
      <GcpNavDrawer open onClose={() => {}} triggerRef={triggerRef} />
    </MemoryRouter>,
  );
}

describe("GcpNavDrawer product search", () => {
  it("filters the catalog live to products matching the query", () => {
    renderDrawer();
    // Unfiltered, both a supported and an unsupported product are present.
    expect(screen.getByTestId("drawer-item-cloud-run")).toBeTruthy();
    expect(screen.getByTestId("drawer-item-compute-engine")).toBeTruthy();

    fireEvent.change(screen.getByTestId("nav-drawer-search"), { target: { value: "storage" } });

    // Cloud Storage matches; Cloud Run and Compute Engine drop out.
    expect(screen.getByTestId("drawer-item-cloud-storage")).toBeTruthy();
    expect(screen.queryByTestId("drawer-item-cloud-run")).toBeNull();
    expect(screen.queryByTestId("drawer-item-compute-engine")).toBeNull();
  });

  it("keeps unsupported products in the results and reports when nothing matches", () => {
    renderDrawer();
    fireEvent.change(screen.getByTestId("nav-drawer-search"), { target: { value: "kubernetes" } });
    // Kubernetes Engine is unsupported but still surfaces (honest catalog).
    expect(screen.getByTestId("drawer-item-kubernetes-engine")).toBeTruthy();

    fireEvent.change(screen.getByTestId("nav-drawer-search"), { target: { value: "zzz-nothing" } });
    expect(screen.getByTestId("nav-drawer-empty")).toBeTruthy();
  });
});
