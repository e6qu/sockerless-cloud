# Simulator Recovery & Re-sync Specification

How simulators and backends recover state after restart and re-sync with running processes.

## Simulator Recovery

### On Restart (with persistence)

1. **SQLite state restored**: All resource metadata (tasks, functions, VPCs, etc.) is automatically available via `SQLiteStore` — no explicit load step needed
2. **PID scan**: `ProcessTracker.LiveProcesses()` scans `{SIM_DATA_DIR}/pids/` for live processes
3. **Process re-attachment**: For each live PID:
   - Create `ProcessHandle` with cancel capability (via `os.FindProcess` + signal)
   - Re-insert into the service's `sync.Map` (e.g., `ecsProcessHandles`)
   - Update resource state to RUNNING if it was stuck in PENDING
4. **Dead process cleanup**: For each dead PID:
   - Delete `.pid` file
   - Update resource state to STOPPED with appropriate exit code

### Container/Task State Machine

```
PROVISIONING → PENDING → RUNNING → STOPPED
                           ↑          ↓
                     (restart recovers here)
```

On restart, tasks found in PENDING or RUNNING whose PID is dead transition to STOPPED.

### Process Identification

Each simulator uses a specific ID for process tracking:

| Cloud | Service | Process ID | Example |
|-------|---------|-----------|---------|
| AWS | ECS | Task ID (UUID from ARN) | `a584da01-5bfb-4262-87e7-0a1af33fd402` |
| AWS | Lambda | Request ID | `req-abc123` |
| GCP | Cloud Run | Execution name | `exec-abc123` |
| Azure | ACA | Execution ID | `exec-def456` |

### Metadata Conventions

Each running task/execution stores metadata in its `Store` entry:

```json
{
  "taskId": "a584da01...",
  "clusterArn": "arn:aws:ecs:...",
  "lastStatus": "RUNNING",
  "containers": [{
    "name": "main",
    "image": "nginx:alpine",
    "lastStatus": "RUNNING"
  }],
  "startedAt": "2026-04-05T12:00:00Z",
  "stoppedAt": "",
  "processId": 12345,
  "exitCode": null
}
```

The `processId` field links the cloud resource to the OS process. On recovery, this is how the simulator matches PID files to resources.

## Backend Recovery & Re-sync

### On Startup

Source: `backends/core/recovery.go`

1. **Load registry**: Read `sockerless-registry.json` (atomic JSON file)
2. **Scan cloud**: Call `ScanOrphanedResources()` to find Sockerless-managed resources tagged with `sockerless-managed=true` and `sockerless-instance={id}`
3. **Merge**: Register any discovered orphans not in registry
4. **Reconstruct**: Build in-memory `Container` state from registry entries

### Periodic Re-sync

Source: `backends/core/recovery.go` → `StartPeriodicResync()`

Backends that implement the `Resyncer` interface get periodic state reconciliation:

```go
type Resyncer interface {
    SyncResources(ctx context.Context, registry *ResourceRegistry) error
}
```

Configured via `SOCKERLESS_RESYNC_INTERVAL` (default 5m, 0 to disable).

Each sync cycle:
1. List all active registry entries
2. Query cloud for current status of each resource
3. Mark stopped/deleted resources as cleaned up
4. Update container state in memory (running → exited)

### ECS Re-sync (Reference Implementation)

Source: `backends/ecs/recovery.go` → `SyncResources()`

1. Collect task ARNs from active registry entries
2. Batch `ecs.DescribeTasks` (up to 100 per call)
3. For each task:
   - `RUNNING` → keep active
   - `STOPPED` / `DEPROVISIONING` → mark cleaned up in registry
   - Not found → mark cleaned up (task was deleted externally)

### Tagging Convention

All Sockerless-managed cloud resources are tagged for discovery:

| Cloud | Tag Key | Tag Value |
|-------|---------|-----------|
| AWS | `sockerless-managed` | `true` |
| AWS | `sockerless-instance` | `{hostname}` |
| AWS | `sockerless-container-id` | `{container_id}` |
| AWS | `sockerless-backend` | `ecs` or `lambda` |
| GCP | `sockerless_managed` | `true` |
| GCP | `sockerless_instance` | `{hostname}` |
| Azure | Tags on resource | Same pattern |

The instance ID (`hostname` by default) ensures a backend only recovers its own resources, not those from another backend instance.

### Registry Entry Format

```json
{
  "containerId": "4ac9d62cb421...",
  "backend": "ecs",
  "resourceType": "task",
  "resourceId": "arn:aws:ecs:eu-west-1:123:task/sockerless-live/abc123",
  "instanceId": "my-laptop.local",
  "createdAt": "2026-04-05T12:00:00Z",
  "cleanedUp": false,
  "status": "active",
  "metadata": {
    "image": "nginx:alpine",
    "name": "/my-container",
    "taskArn": "arn:aws:ecs:..."
  }
}
```

## Simulator ↔ Backend Reconnection

When both simulator and backend restart:

1. **Simulator starts first**: SQLite restores all resources. PID scan finds live processes.
2. **Backend starts**: Loads registry, calls `ScanOrphanedResources()` against simulator.
3. **Simulator responds**: Returns all Sockerless-tagged resources (tasks, functions, jobs).
4. **Backend reconciles**: Merges discovered resources with its registry, reconstructs containers.
5. **Periodic sync**: Every 5 minutes, backend queries simulator for current state and updates.

This ensures that even if the backend restarts while the simulator is running containers, the backend re-discovers them and can manage them.
