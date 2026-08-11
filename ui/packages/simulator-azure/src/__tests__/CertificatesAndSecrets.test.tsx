import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CertificatesAndSecrets, EXPIRY_OPTIONS } from "../pages/CertificatesAndSecrets.js";
import type { ClientSecretMetadata, MintedClientSecret } from "../api.js";

const stored: ClientSecretMetadata = {
  keyId: "11111111-1111-1111-1111-111111111111",
  displayName: "ci secret",
  hint: "abc",
  startDateTime: "2026-01-01T00:00:00Z",
  endDateTime: "2028-01-01T00:00:00Z",
};

const minted: MintedClientSecret = {
  keyId: "22222222-2222-2222-2222-222222222222",
  displayName: "portal secret",
  hint: "Qx7",
  startDateTime: "2026-07-01T00:00:00Z",
  endDateTime: "2027-01-01T00:00:00Z",
  secretText: "Qx7SuperSecretValueMintedOnce",
};

function blade(props: Partial<Parameters<typeof CertificatesAndSecrets>[0]> = {}) {
  return (
    <CertificatesAndSecrets
      credentials={[stored]}
      minted={null}
      busy={false}
      onCreate={() => {}}
      onRemove={() => {}}
      {...props}
    />
  );
}

describe("CertificatesAndSecrets", () => {
  // The vitest config runs without testing-library's auto-cleanup globals, so
  // unmount between tests to keep screen queries scoped to one render.
  afterEach(cleanup);

  it("lists stored secrets as hint-masked metadata, never a value", () => {
    render(blade());
    const hint = screen.getByTestId("entra-secret-hint");
    expect(hint.textContent).toContain("abc");
    expect(hint.textContent).not.toContain("SuperSecret");
    expect(screen.queryByTestId("entra-secret-value")).toBeNull();
  });

  it("shows a freshly minted secret's full value exactly once, with the one-time warning", () => {
    render(blade({ credentials: [stored, minted], minted }));
    // The minted row carries the full value; the stored row stays masked.
    expect(screen.getByTestId("entra-secret-value").textContent).toBe(minted.secretText);
    expect(screen.getByTestId("entra-secret-notice").textContent).toContain(
      "You won’t be able to retrieve it after you leave this blade",
    );
    expect(screen.getByTestId("entra-secret-hint").textContent).toContain("abc");
  });

  it("drops the value once the minted secret is gone — leaving the blade forgets it", () => {
    const { rerender } = render(blade({ credentials: [stored, minted], minted }));
    expect(screen.getByTestId("entra-secret-value")).not.toBeNull();
    // The page discards the minted material on navigation; the same
    // credential renders as metadata only.
    rerender(blade({ credentials: [stored, minted], minted: null }));
    expect(screen.queryByTestId("entra-secret-value")).toBeNull();
    expect(screen.queryByTestId("entra-secret-notice")).toBeNull();
    const hints = screen.getAllByTestId("entra-secret-hint").map((node) => node.textContent ?? "");
    expect(hints.some((hint) => hint.startsWith("Qx7"))).toBe(true);
    expect(hints.join(" ")).not.toContain(minted.secretText);
  });

  it("submits a new client secret with the chosen description and expiry", () => {
    const onCreate = vi.fn();
    render(blade({ onCreate }));
    fireEvent.click(screen.getByTestId("entra-new-client-secret"));
    fireEvent.change(screen.getByTestId("entra-secret-description"), { target: { value: "deploy key" } });
    fireEvent.change(screen.getByTestId("entra-secret-expiry"), { target: { value: "90" } });
    fireEvent.submit(screen.getByTestId("entra-secret-form"));

    expect(onCreate).toHaveBeenCalledTimes(1);
    const [description, endDateTime] = onCreate.mock.calls[0] as [string, string];
    expect(description).toBe("deploy key");
    const expected = Date.now() + 90 * 24 * 60 * 60 * 1000;
    expect(Math.abs(new Date(endDateTime).getTime() - expected)).toBeLessThan(60_000);
  });

  it("offers the real blade's expiry choices with 180 days recommended", () => {
    render(blade());
    fireEvent.click(screen.getByTestId("entra-new-client-secret"));
    const select = screen.getByTestId("entra-secret-expiry");
    expect(select.querySelectorAll("option")).toHaveLength(EXPIRY_OPTIONS.length);
    expect((select as HTMLSelectElement).value).toBe("180");
  });

  it("requests removal of a stored secret by its keyId", () => {
    const onRemove = vi.fn();
    render(blade({ onRemove }));
    fireEvent.click(screen.getByTestId("entra-secret-delete"));
    expect(onRemove).toHaveBeenCalledWith(stored.keyId);
  });
});
