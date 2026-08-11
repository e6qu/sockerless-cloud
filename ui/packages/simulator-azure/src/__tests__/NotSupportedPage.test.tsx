import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it } from "vitest";
import { NotSupportedPage } from "../pages/NotSupportedPage.js";

describe("NotSupportedPage", () => {
  // The vitest config runs without testing-library's auto-cleanup globals, so
  // each render must be unmounted between tests to keep screen queries
  // scoped to one render.
  afterEach(cleanup);

  // Azure Kubernetes Service is genuinely absent from the simulator — no
  // Microsoft.ContainerService routes at all — which is what this page is for.
  it("names the real Azure service and states plainly that the simulator does not implement it", () => {
    render(
      <MemoryRouter initialEntries={["/ui/not-supported/azure-kubernetes-service"]}>
        <NotSupportedPage />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("not-supported-message").textContent).toContain(
      "Azure Kubernetes Service is not implemented by the Sockerless simulator.",
    );
    const essentials = screen.getByRole("region", { name: "Essentials" });
    expect(essentials.textContent).toContain("Azure Kubernetes Service (AKS) cluster");
    expect(essentials.textContent).toContain("Not supported");
  });

  it("falls back to a generic label for a route the catalog doesn't recognise", () => {
    render(
      <MemoryRouter initialEntries={["/ui/not-supported/something-unknown"]}>
        <NotSupportedPage />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("not-supported-message").textContent).toContain(
      "This service is not implemented by the Sockerless simulator.",
    );
  });
});
