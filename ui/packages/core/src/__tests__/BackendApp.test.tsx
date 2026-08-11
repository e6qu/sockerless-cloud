import { describe, it, expect, vi, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiClient } from "../api/client.js";
import { ApiClientProvider } from "../hooks/api-context.js";
import { BackendApp } from "../components/BackendApp.js";

const mockFetch = vi.fn();
globalThis.fetch = mockFetch;

function jsonResponse(data: unknown) {
  return new Response(JSON.stringify(data), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  mockFetch.mockReset();
});

function renderApp(title: string) {
  window.history.pushState({}, "", "/ui/");
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const apiClient = new ApiClient();
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiClientProvider client={apiClient}>
        <BackendApp title={title} />
      </ApiClientProvider>
    </QueryClientProvider>,
  );
}

describe("BackendApp", () => {
  it("renders the title in the sidebar", () => {
    mockFetch.mockResolvedValue(jsonResponse({ status: "ok", component: "backend", uptime_seconds: 0 }));
    const { container } = renderApp("ECS Backend");
    expect(container.textContent).toContain("ECS Backend");
  });

  it("renders nav links", () => {
    mockFetch.mockResolvedValue(jsonResponse({ status: "ok", component: "backend", uptime_seconds: 0 }));
    const { container } = renderApp("Lambda Backend");
    const links = container.querySelectorAll("a");
    const labels = Array.from(links).map((l) => l.textContent);
    expect(labels).toContain("Overview");
    expect(labels).toContain("Containers");
    expect(labels).toContain("Resources");
    expect(labels).toContain("Metrics");
  });
});
