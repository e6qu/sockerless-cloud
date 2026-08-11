import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { InlineError } from "../components/InlineError.js";

describe("InlineError", () => {
  test("renders title + Error message + action", () => {
    render(
      <InlineError
        title="Failed to load containers"
        detail={new Error("ECONNREFUSED")}
        action={<button>Retry</button>}
      />,
    );
    expect(screen.getByText("Failed to load containers")).toBeInTheDocument();
    expect(screen.getByText("ECONNREFUSED")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  test("string detail renders verbatim", () => {
    render(<InlineError title="Whoops" detail="something specific" />);
    expect(screen.getByText("something specific")).toBeInTheDocument();
  });

  test("no detail still renders title", () => {
    render(<InlineError title="bare" />);
    expect(screen.getByText("bare")).toBeInTheDocument();
  });
});
