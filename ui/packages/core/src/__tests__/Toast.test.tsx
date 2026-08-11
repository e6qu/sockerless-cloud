import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ToastProvider, useReportError, useToast } from "../components/Toast.js";

function PushTwo() {
  const { push } = useToast();
  return (
    <button
      onClick={() => {
        push({ tone: "info", title: "First" });
        push({ tone: "error", title: "Second", body: "boom" });
      }}
    >
      go
    </button>
  );
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
});

describe("ToastProvider", () => {
  test("renders pushed toasts and dismisses via close button", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(
      <ToastProvider>
        <PushTwo />
      </ToastProvider>,
    );
    await user.click(screen.getByText("go"));

    expect(screen.getByText("First")).toBeInTheDocument();
    expect(screen.getByText("Second")).toBeInTheDocument();
    expect(screen.getByText("boom")).toBeInTheDocument();

    const closes = screen.getAllByLabelText("Dismiss notification");
    await user.click(closes[0]);
    expect(screen.queryByText("First")).not.toBeInTheDocument();
    expect(screen.getByText("Second")).toBeInTheDocument();
  });

  test("auto-dismisses after the configured duration", async () => {
    function PushOne() {
      const { push } = useToast();
      return <button onClick={() => push({ tone: "info", title: "Tick", duration: 200 })}>go</button>;
    }
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(
      <ToastProvider>
        <PushOne />
      </ToastProvider>,
    );
    await user.click(screen.getByText("go"));
    expect(screen.getByText("Tick")).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(screen.queryByText("Tick")).not.toBeInTheDocument();
  });

  test("useReportError pushes an error toast with the message", async () => {
    function ReportButton() {
      const report = useReportError();
      return <button onClick={() => report(new Error("kaboom"), "Save failed")}>fail</button>;
    }
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(
      <ToastProvider>
        <ReportButton />
      </ToastProvider>,
    );
    await user.click(screen.getByText("fail"));
    expect(screen.getByText("Save failed")).toBeInTheDocument();
    expect(screen.getByText("kaboom")).toBeInTheDocument();
  });
});
