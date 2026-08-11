import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi, beforeEach } from "vitest";
import { Modal } from "../components/Modal.js";

beforeEach(() => {
  // jsdom <dialog> ships .open + .close + .showModal in modern versions,
  // but the shim is recent — guarantee the surface we touch.
  if (!HTMLDialogElement.prototype.showModal) {
    HTMLDialogElement.prototype.showModal = function () {
      this.open = true;
    };
  }
  if (!HTMLDialogElement.prototype.close) {
    HTMLDialogElement.prototype.close = function () {
      this.open = false;
      this.dispatchEvent(new Event("close"));
    };
  }
});

describe("Modal", () => {
  test("renders title and children when open", () => {
    render(
      <Modal open onClose={() => {}} title="Container abc">
        <p>body content</p>
      </Modal>,
    );
    expect(screen.getByText("Container abc")).toBeInTheDocument();
    expect(screen.getByText("body content")).toBeInTheDocument();
  });

  test("close button fires onClose", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <Modal open onClose={onClose} title="title">
        body
      </Modal>,
    );
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  test("does not render content when open=false", () => {
    render(
      <Modal open={false} onClose={() => {}} title="hidden">
        body
      </Modal>,
    );
    // The dialog element exists but its `open` attribute should be false,
    // so the body is hidden by the platform.
    const dialog = screen.getByText("hidden").closest("dialog");
    expect(dialog?.hasAttribute("open")).toBe(false);
  });
});
