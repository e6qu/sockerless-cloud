import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SubscriptionCreateForm } from "../pages/SubscriptionsPage.js";

function form(props: Partial<Parameters<typeof SubscriptionCreateForm>[0]> = {}) {
  return (
    <SubscriptionCreateForm
      busy={false}
      provisioningState=""
      onCreate={() => {}}
      onDismiss={() => {}}
      {...props}
    />
  );
}

const submitButton = () => screen.getByTestId("subs-create-submit") as HTMLButtonElement;

describe("SubscriptionCreateForm", () => {
  // The vitest config runs without testing-library's auto-cleanup globals, so
  // unmount between tests to keep screen queries scoped to one render.
  afterEach(cleanup);

  it("keeps Create disabled until the subscription has a name", () => {
    render(form());
    expect(submitButton().disabled).toBe(true);
    fireEvent.change(screen.getByTestId("subs-create-name"), { target: { value: "Team Subscription" } });
    expect(submitButton().disabled).toBe(false);
  });

  it("submits the name with the billing scope the operator chose", () => {
    const onCreate = vi.fn();
    render(form({ onCreate }));
    fireEvent.change(screen.getByTestId("subs-create-name"), { target: { value: "Team Subscription" } });
    fireEvent.change(screen.getByTestId("subs-create-scope"), {
      target: { value: "/providers/Microsoft.Billing/billingAccounts/acct/enrollmentAccounts/enr" },
    });
    fireEvent.submit(screen.getByTestId("subs-create-form"));
    expect(onCreate).toHaveBeenCalledWith(
      "Team Subscription",
      "/providers/Microsoft.Billing/billingAccounts/acct/enrollmentAccounts/enr",
    );
  });

  it("requires a billing scope — a cleared scope disables Create", () => {
    const onCreate = vi.fn();
    render(form({ onCreate }));
    fireEvent.change(screen.getByTestId("subs-create-name"), { target: { value: "Team Subscription" } });
    fireEvent.change(screen.getByTestId("subs-create-scope"), { target: { value: "" } });
    expect(submitButton().disabled).toBe(true);
    fireEvent.submit(screen.getByTestId("subs-create-form"));
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("reports the live provisioning state while the subscription materializes", () => {
    render(form({ busy: true, provisioningState: "Accepted" }));
    expect(screen.getByTestId("subs-provisioning").textContent).toContain("Accepted");
    expect(submitButton().disabled).toBe(true);
  });
});
