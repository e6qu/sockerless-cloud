/** Health check response from GET /internal/v1/healthz */
export interface HealthResponse {
  status: string;
  component: string;
  uptime_seconds: number;
}

/** Backend status from GET /internal/v1/status */
export interface StatusResponse {
  status: string;
  component: string;
  backend_type: string;
  instance_id: string;
  uptime_seconds: number;
  containers: number;
  active_resources: number;
  context: string;
}

/** Container summary from GET /internal/v1/containers/summary */
export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  created: string;
  pod_name?: string;
}

/** Latency percentiles in milliseconds */
export interface LatencyStats {
  p50: number;
  p95: number;
  p99: number;
}

/** Metrics snapshot from GET /internal/v1/metrics */
export interface MetricsResponse {
  requests: Record<string, number>;
  latency_ms: Record<string, LatencyStats>;
  goroutines: number;
  heap_alloc_mb: number;
  uptime_seconds?: number;
  containers?: number;
  active_resources?: number;
}

/** Resource registry entry from GET /internal/v1/resources */
export interface ResourceEntry {
  containerId: string;
  backend: string;
  resourceType: string;
  resourceId: string;
  instanceId: string;
  createdAt: string;
  cleanedUp: boolean;
  status?: string;
  metadata?: Record<string, string>;
}

/** Single health check result */
export interface CheckResult {
  name: string;
  status: string;
  detail?: string;
}

/** Check response from GET /internal/v1/check */
export interface CheckResponse {
  checks: CheckResult[];
}

/** Backend info from GET /internal/v1/info */
export interface BackendInfo {
  ID: string;
  Name: string;
  ServerVersion: string;
  Containers: number;
  ContainersRunning: number;
  ContainersStopped: number;
  Images: number;
  Driver: string;
  OperatingSystem: string;
  OSType: string;
  Architecture: string;
  NCPU: number;
  MemTotal: number;
}
