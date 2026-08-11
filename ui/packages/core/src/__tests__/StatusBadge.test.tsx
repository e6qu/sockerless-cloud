import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatusBadge } from "../components/StatusBadge";

describe("StatusBadge", () => {
  it("renders waiting with the warn token (deployment-review runs)", () => {
    render(<StatusBadge status="waiting" />);
    const badge = screen.getByText("waiting");
    expect(badge.style.color).toBe("var(--color-status-warn)");
    expect(badge.style.background).toBe("var(--color-status-warn-soft)");
  });

  it("renders unknown statuses with the neutral fallback", () => {
    render(<StatusBadge status="somenewstate" />);
    const badge = screen.getByText("somenewstate");
    expect(badge.style.color).toBe("var(--color-fg-muted)");
  });
});
