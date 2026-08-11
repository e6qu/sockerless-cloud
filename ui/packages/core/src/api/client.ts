import type {
  HealthResponse,
  StatusResponse,
  ContainerSummary,
  MetricsResponse,
  ResourceEntry,
  CheckResponse,
  BackendInfo,
} from "./types.js";

/** Error thrown when the API returns a non-ok response. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public statusText: string,
    public body: string,
  ) {
    super(`API error ${status}: ${statusText}`);
    this.name = "ApiError";
  }
}

/** Lightweight API client using relative-path fetch. */
export class ApiClient {
  constructor(private baseUrl = "") {}

  private async request<T>(path: string): Promise<T> {
    const res = await fetch(`${this.baseUrl}${path}`);
    if (!res.ok) {
      const body = await res.text();
      throw new ApiError(res.status, res.statusText, body);
    }
    return res.json() as Promise<T>;
  }

  health(): Promise<HealthResponse> {
    return this.request("/internal/v1/healthz");
  }

  status(): Promise<StatusResponse> {
    return this.request("/internal/v1/status");
  }

  containers(): Promise<ContainerSummary[]> {
    return this.request("/internal/v1/containers/summary");
  }

  metrics(): Promise<MetricsResponse> {
    return this.request("/internal/v1/metrics");
  }

  resources(active?: boolean): Promise<ResourceEntry[]> {
    const qs = active ? "?active=true" : "";
    return this.request(`/internal/v1/resources${qs}`);
  }

  check(): Promise<CheckResponse> {
    return this.request("/internal/v1/check");
  }

  info(): Promise<BackendInfo> {
    return this.request("/internal/v1/info");
  }
}
