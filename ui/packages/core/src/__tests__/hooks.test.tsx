import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode } from "react";
import { ApiClient } from "../api/client.js";
import { ApiClientProvider } from "../hooks/api-context.js";
import { useHealth, useContainers, useStatus } from "../hooks/queries.js";

const mockFetch = vi.fn();
globalThis.fetch = mockFetch;

function jsonResponse(data: unknown) {
  return new Response(JSON.stringify(data), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const apiClient = new ApiClient("http://localhost:9100");
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ApiClientProvider client={apiClient}>{children}</ApiClientProvider>
    </QueryClientProvider>
  );
}

describe("Query hooks", () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it("useHealth fetches and returns data", async () => {
    const body = { status: "ok", component: "backend", uptime_seconds: 5 };
    mockFetch.mockResolvedValue(jsonResponse(body));

    const { result } = renderHook(() => useHealth(), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.status).toBe("ok");
  });

  it("useContainers fetches and returns list", async () => {
    const body = [{ id: "abc", name: "web", image: "nginx", state: "running", created: "2025-01-01T00:00:00Z" }];
    mockFetch.mockResolvedValue(jsonResponse(body));

    const { result } = renderHook(() => useContainers(), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(1);
  });

  it("useStatus re-fetches on interval", async () => {
    const body = { status: "ok", component: "backend", backend_type: "memory", instance_id: "x", uptime_seconds: 1, containers: 0, active_resources: 0, context: "" };
    mockFetch.mockResolvedValue(jsonResponse(body));

    const { result } = renderHook(() => useStatus(), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.backend_type).toBe("memory");
  });
});
