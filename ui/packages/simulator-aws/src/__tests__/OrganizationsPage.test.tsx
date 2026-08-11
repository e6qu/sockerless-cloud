import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
  CreateAccountStatusPanel,
  OrganizationNotInUsePanel,
  orgAccountStatusKind,
} from "../pages/OrganizationsPage.js";
import type { OrgCreateAccountStatus } from "../api.js";

/**
 * The AWS Organizations page's stateful renderings: the asynchronous
 * CreateAccount request as it moves through IN_PROGRESS → SUCCEEDED/FAILED,
 * and the AWSOrganizationsNotInUseException state that offers Create an
 * organization. Both are presentational — the live wire flow is proven against
 * the real simulator API in the Shauth relying-party suite.
 */

const baseStatus: OrgCreateAccountStatus = {
  id: "car-1234567890abcdef1234567890abcdef",
  accountName: "Sandbox",
  state: "IN_PROGRESS",
  requestedTimestamp: 1_753_000_000,
  completedTimestamp: 0,
  accountId: "",
  failureReason: "",
};

describe("CreateAccountStatusPanel", () => {
  // Testing Library only auto-cleans between tests when the runner exposes
  // globals; this suite runs without them, so clean up explicitly.
  afterEach(cleanup);

  it("renders an in-progress request as pending, with the request id and no account id", () => {
    render(<CreateAccountStatusPanel status={baseStatus} />);
    const panel = screen.getByTestId("org-create-status");
    expect(panel.textContent).toContain(baseStatus.id);
    expect(panel.textContent).toContain("Sandbox");
    expect(panel.querySelector('[class*="status-in-progress"]')?.textContent).toContain("IN_PROGRESS");
    expect(screen.queryByTestId("org-created-account-id")).toBeNull();
    expect(screen.getByRole("status").textContent).toContain("creating the account");
  });

  it("renders a succeeded request with the created account's id", () => {
    render(
      <CreateAccountStatusPanel
        status={{
          ...baseStatus,
          state: "SUCCEEDED",
          completedTimestamp: 1_753_000_100,
          accountId: "123456789013",
        }}
      />,
    );
    const panel = screen.getByTestId("org-create-status");
    expect(panel.querySelector('[class*="status-success"]')?.textContent).toContain("SUCCEEDED");
    expect(screen.getByTestId("org-created-account-id").textContent).toBe("123456789013");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders a failed request with the service's failure reason", () => {
    render(
      <CreateAccountStatusPanel
        status={{
          ...baseStatus,
          state: "FAILED",
          completedTimestamp: 1_753_000_100,
          failureReason: "EMAIL_ALREADY_EXISTS",
        }}
      />,
    );
    const panel = screen.getByTestId("org-create-status");
    expect(panel.querySelector('[class*="status-error"]')?.textContent).toContain("FAILED");
    const alert = screen.getByTestId("org-error");
    expect(alert.textContent).toContain("could not be created");
    expect(alert.textContent).toContain("EMAIL_ALREADY_EXISTS");
  });
});

describe("OrganizationNotInUsePanel", () => {
  afterEach(cleanup);

  it("offers Create an organization as the primary action", () => {
    let created = 0;
    render(<OrganizationNotInUsePanel onCreate={() => (created += 1)} isPending={false} />);
    const create = screen.getByTestId("org-create-organization");
    expect(create.textContent).toBe("Create an organization");
    fireEvent.click(create);
    expect(created).toBe(1);
    expect(screen.queryByTestId("org-error")).toBeNull();
  });

  it("disables the action while creating and surfaces a creation failure", () => {
    render(
      <OrganizationNotInUsePanel
        onCreate={() => {}}
        isPending
        errorMessage="CreateOrganization: AccessDeniedException: not authorized"
      />,
    );
    expect((screen.getByTestId("org-create-organization") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("org-error").textContent).toContain("AccessDeniedException");
  });
});

describe("orgAccountStatusKind", () => {
  it("maps the documented account statuses to their glyphs", () => {
    expect(orgAccountStatusKind("ACTIVE")).toBe("success");
    expect(orgAccountStatusKind("SUSPENDED")).toBe("error");
    expect(orgAccountStatusKind("PENDING_CLOSURE")).toBe("warning");
  });
});
