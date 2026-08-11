import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { BackendInfoCard } from "../components/BackendInfoCard.js";
import type { StatusResponse } from "../api/types.js";

afterEach(cleanup);

describe("BackendInfoCard", () => {
  it("renders backend type and instance ID", () => {
    const status: StatusResponse = {
      status: "ok",
      component: "backend",
      backend_type: "ecs-fargate",
      instance_id: "abc-123",
      uptime_seconds: 100,
      containers: 5,
      active_resources: 2,
      context: "",
    };
    const { container } = render(<BackendInfoCard status={status} />);
    expect(container.textContent).toContain("ecs-fargate");
    expect(container.textContent).toContain("abc-123");
  });

  it("renders context when present", () => {
    const status: StatusResponse = {
      status: "ok",
      component: "backend",
      backend_type: "memory",
      instance_id: "xyz-789",
      uptime_seconds: 42,
      containers: 0,
      active_resources: 0,
      context: "us-east-1",
    };
    const { container } = render(<BackendInfoCard status={status} />);
    expect(container.textContent).toContain("us-east-1");
    // Field labels are lowercase monospace in the operator-tool theme.
    expect(container.textContent?.toLowerCase()).toContain("context");
  });
});
