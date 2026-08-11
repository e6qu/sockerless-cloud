import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StorageAccountCreateForm } from "../pages/StorageAccountsPage.js";
import type { Subscription } from "../api.js";

const SUBSCRIPTIONS: Subscription[] = [
  { id: "/subscriptions/sub-1", subscriptionId: "sub-1", displayName: "Simulator", state: "Enabled" },
];

function form(props: Partial<Parameters<typeof StorageAccountCreateForm>[0]> = {}) {
  return (
    <StorageAccountCreateForm
      subscriptions={SUBSCRIPTIONS}
      busy={false}
      onCreate={() => {}}
      onDismiss={() => {}}
      {...props}
    />
  );
}

const submitButton = () => screen.getByTestId("storage-create-submit") as HTMLButtonElement;

describe("StorageAccountCreateForm", () => {
  afterEach(cleanup);

  it("preselects the only subscription and keeps Create disabled until the name is valid", () => {
    render(form());
    expect((screen.getByTestId("storage-create-subscription") as HTMLSelectElement).value).toBe("sub-1");
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "ab" } });
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "mystorageacct1" } });
    expect(submitButton().disabled).toBe(false);
  });

  it("rejects a name with uppercase letters or separators, matching real Azure Storage naming rules", () => {
    render(form());
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "My-Storage" } });
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "toolongtoolongtoolongtoolong" } });
    expect(submitButton().disabled).toBe(true);
  });

  it("submits the full create input the form collected", () => {
    const onCreate = vi.fn();
    render(form({ onCreate }));
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "mystorageacct1" } });
    fireEvent.change(screen.getByTestId("storage-create-rg"), { target: { value: "my-rg" } });
    fireEvent.change(screen.getByTestId("storage-create-location"), { target: { value: "westus2" } });
    fireEvent.change(screen.getByTestId("storage-create-sku"), { target: { value: "Standard_ZRS" } });
    fireEvent.change(screen.getByTestId("storage-create-kind"), { target: { value: "BlobStorage" } });
    fireEvent.submit(screen.getByTestId("storage-create-form"));
    expect(onCreate).toHaveBeenCalledWith({
      subscriptionId: "sub-1",
      resourceGroup: "my-rg",
      name: "mystorageacct1",
      location: "westus2",
      skuName: "Standard_ZRS",
      kind: "BlobStorage",
    });
  });

  it("disables Create while a create is in flight", () => {
    render(form({ busy: true }));
    fireEvent.change(screen.getByTestId("storage-create-name"), { target: { value: "mystorageacct1" } });
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
