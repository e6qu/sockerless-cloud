import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ACRRegistryCreateForm } from "../pages/ACRRegistriesPage.js";
import type { Subscription } from "../api.js";

const SUBSCRIPTIONS: Subscription[] = [
  { id: "/subscriptions/sub-1", subscriptionId: "sub-1", displayName: "Simulator", state: "Enabled" },
];

function form(props: Partial<Parameters<typeof ACRRegistryCreateForm>[0]> = {}) {
  return (
    <ACRRegistryCreateForm subscriptions={SUBSCRIPTIONS} busy={false} onCreate={() => {}} onDismiss={() => {}} {...props} />
  );
}

const submitButton = () => screen.getByTestId("acr-create-submit") as HTMLButtonElement;

describe("ACRRegistryCreateForm", () => {
  afterEach(cleanup);

  it("preselects the only subscription and keeps Create disabled until the name is valid", () => {
    render(form());
    expect((screen.getByTestId("acr-create-subscription") as HTMLSelectElement).value).toBe("sub-1");
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "abcd" } });
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "myregistry1" } });
    expect(submitButton().disabled).toBe(false);
  });

  it("rejects a name with hyphens or underscores, matching real ACR naming rules", () => {
    render(form());
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "my-registry" } });
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "my_registry" } });
    expect(submitButton().disabled).toBe(true);
  });

  it("submits the full create input the form collected", () => {
    const onCreate = vi.fn();
    render(form({ onCreate }));
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "myregistry1" } });
    fireEvent.change(screen.getByTestId("acr-create-rg"), { target: { value: "my-rg" } });
    fireEvent.change(screen.getByTestId("acr-create-location"), { target: { value: "westus2" } });
    fireEvent.change(screen.getByTestId("acr-create-sku"), { target: { value: "Premium" } });
    fireEvent.submit(screen.getByTestId("acr-create-form"));
    expect(onCreate).toHaveBeenCalledWith({
      subscriptionId: "sub-1",
      resourceGroup: "my-rg",
      name: "myregistry1",
      location: "westus2",
      skuName: "Premium",
    });
  });

  it("disables Create while a create is in flight", () => {
    render(form({ busy: true }));
    fireEvent.change(screen.getByTestId("acr-create-name"), { target: { value: "myregistry1" } });
    expect(submitButton().disabled).toBe(true);
    expect(submitButton().textContent).toContain("Creating");
  });

  it("calls onDismiss from Cancel", () => {
    const onDismiss = vi.fn();
    render(form({ onDismiss }));
    fireEvent.click(screen.getByText("Cancel"));
    expect(onDismiss).toHaveBeenCalled();
  });
});
