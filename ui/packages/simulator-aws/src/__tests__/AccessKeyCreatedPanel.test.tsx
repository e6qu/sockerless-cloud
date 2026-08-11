import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AccessKeyCreatedPanel } from "../pages/IAMUserSecurityCredentialsPage.js";

/**
 * The one-time disclosure of a minted IAM access key. The secret exists only in
 * the CreateAccessKey response, so this panel is the operator's single chance
 * to read it — it must warn about that, guard the secret behind Show, and hand
 * over the exact AWS CLI usage including the simulator endpoint.
 */
describe("AccessKeyCreatedPanel", () => {
  // Testing Library only auto-cleans between tests when the runner exposes
  // globals; this suite runs without them, so clean up explicitly.
  afterEach(cleanup);

  const props = {
    accessKeyId: "AKIAEXAMPLEKEYID0000",
    secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    endpoint: "http://localhost:4566",
  };

  it("states the one-time semantics of the secret", () => {
    render(<AccessKeyCreatedPanel {...props} />);
    expect(
      screen.getByText(/only time that the secret access key can be viewed or downloaded/i),
    ).toBeTruthy();
    expect(screen.getByText(/cannot recover it later/i)).toBeTruthy();
  });

  it("masks the secret until Show is pressed, and hides it again", () => {
    render(<AccessKeyCreatedPanel {...props} />);
    const secret = screen.getByTestId("iam-secret-access-key");
    expect(secret.textContent).not.toContain(props.secretAccessKey);
    expect(secret.textContent).toMatch(/^\*+$/);

    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(screen.getByTestId("iam-secret-access-key").textContent).toBe(props.secretAccessKey);

    fireEvent.click(screen.getByRole("button", { name: "Hide" }));
    expect(screen.getByTestId("iam-secret-access-key").textContent).not.toContain(props.secretAccessKey);
  });

  it("shows the access key ID with a copy control for each credential part", () => {
    render(<AccessKeyCreatedPanel {...props} />);
    expect(screen.getByTestId("iam-access-key-id").textContent).toBe(props.accessKeyId);
    expect(screen.getByRole("button", { name: "Copy access key ID" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy secret access key" })).toBeTruthy();
  });

  it("renders the exact aws configure values and the endpoint-scoped verification command", () => {
    render(<AccessKeyCreatedPanel {...props} />);
    const usage = screen.getByTestId("iam-cli-usage").textContent ?? "";
    expect(usage).toContain("aws configure");
    expect(usage).toContain(`AWS Access Key ID [None]: ${props.accessKeyId}`);
    expect(usage).toContain(`AWS Secret Access Key [None]: ${props.secretAccessKey}`);
    expect(usage).toContain("Default region name [None]: us-east-1");

    expect(
      screen.getByText(`aws sts get-caller-identity --endpoint-url ${props.endpoint}`),
    ).toBeTruthy();
  });
});
