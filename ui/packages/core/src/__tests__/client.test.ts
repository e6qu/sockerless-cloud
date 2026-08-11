import { describe, it, expect, vi, beforeEach } from "vitest";
import { ApiClient, ApiError } from "../api/client.js";

const mockFetch = vi.fn();
globalThis.fetch = mockFetch;

function jsonResponse(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    statusText: status === 200 ? "OK" : "Error",
    headers: { "Content-Type": "application/json" },
  });
}

describe("ApiClient", () => {
  let client: ApiClient;

  beforeEach(() => {
    client = new ApiClient("http://localhost:9100");
    mockFetch.mockReset();
  });

  it("health() GETs correct path and returns parsed JSON", async () => {
    const body = { status: "ok", component: "backend", uptime_seconds: 42 };
    mockFetch.mockResolvedValueOnce(jsonResponse(body));

    const result = await client.health();

    expect(mockFetch).toHaveBeenCalledWith("http://localhost:9100/internal/v1/healthz");
    expect(result).toEqual(body);
  });

  it("status() returns StatusResponse", async () => {
    const body = {
      status: "ok",
      component: "backend",
      backend_type: "memory",
      instance_id: "abc",
      uptime_seconds: 10,
      containers: 3,
      active_resources: 1,
      context: "",
    };
    mockFetch.mockResolvedValueOnce(jsonResponse(body));

    const result = await client.status();

    expect(result.backend_type).toBe("memory");
    expect(result.containers).toBe(3);
  });

  it("containers() returns array", async () => {
    const body = [
      { id: "abc123", name: "web", image: "nginx", state: "running", created: "2025-01-01T00:00:00Z" },
    ];
    mockFetch.mockResolvedValueOnce(jsonResponse(body));

    const result = await client.containers();

    expect(result).toHaveLength(1);
    expect(result[0].name).toBe("web");
  });

  it("metrics() returns MetricsResponse", async () => {
    const body = {
      requests: { "GET /internal/v1/healthz": 5 },
      latency_ms: { "GET /internal/v1/healthz": { p50: 1, p95: 2, p99: 3 } },
      goroutines: 10,
      heap_alloc_mb: 2.5,
    };
    mockFetch.mockResolvedValueOnce(jsonResponse(body));

    const result = await client.metrics();

    expect(result.goroutines).toBe(10);
    expect(result.heap_alloc_mb).toBe(2.5);
  });

  it("resources(true) appends ?active=true", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse([]));

    await client.resources(true);

    expect(mockFetch).toHaveBeenCalledWith("http://localhost:9100/internal/v1/resources?active=true");
  });

  it("non-ok response throws ApiError", async () => {
    mockFetch.mockResolvedValueOnce(
      new Response("not found", { status: 404, statusText: "Not Found" }),
    );

    await expect(client.health()).rejects.toThrow(ApiError);
    try {
      mockFetch.mockResolvedValueOnce(
        new Response("server error", { status: 500, statusText: "Internal Server Error" }),
      );
      await client.health();
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as ApiError).status).toBe(500);
    }
  });
});
