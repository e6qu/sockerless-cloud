# Simulator Persistence Specification

How cloud simulators persist state across restarts via SQLite.

## Architecture

Each simulator (AWS, GCP, Azure) uses a generic `Store[T]` interface with two implementations:

- **MemoryStore[T]**: In-memory `map[string]T` (default, for tests and ephemeral use)
- **SQLiteStore[T]**: Persistent `(key TEXT, value BLOB)` table per store instance

Source: `simulator-{aws,gcp,azure}/shared/state.go`, `state_sqlite.go`, `db.go`

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `SIM_PERSIST` | `false` | Enable SQLite persistence (`true` or `1`) |
| `SIM_DATA_DIR` | `/tmp/sockerless-sim-{cloud}` | Directory for database and PID files |

When persistence is disabled, all stores are in-memory and state is lost on restart. When enabled, SQLite stores data at `{SIM_DATA_DIR}/simulator.db`.

## SQLite Configuration

- **Journal mode**: WAL (concurrent reads during writes)
- **Busy timeout**: 5000ms (wait for lock instead of failing)
- **Synchronous**: NORMAL (safe with WAL, better performance)
- **Driver**: `modernc.org/sqlite` (CGO-free, cross-platform)

## Schema

Each `Store[T]` instance maps to one SQLite table:

```sql
CREATE TABLE IF NOT EXISTS {table_name} (
    key   TEXT PRIMARY KEY,
    value BLOB
);
```

Values are JSON-serialized. The generic KV schema avoids per-type migrations — adding a field to a Go struct automatically persists via JSON.

## Table Naming Convention

`{service}_{resource}` in lowercase:

### AWS (32 tables)
| Table | Go Type | Service |
|-------|---------|---------|
| `ecs_clusters` | `ECSCluster` | ECS |
| `ecs_task_definitions` | `ECSTaskDefinition` | ECS |
| `ecs_tasks` | `ECSTask` | ECS |
| `lambda_functions` | `LambdaFunction` | Lambda |
| `ecr_repositories` | `ECRRepository` | ECR |
| `ecr_images` | `ECRImageDetail` | ECR |
| `s3_buckets` | `S3Bucket` | S3 |
| `s3_objects` | `S3Object` | S3 |
| `cw_log_groups` | `CWLogGroup` | CloudWatch |
| `cw_log_streams` | `CWLogStream` | CloudWatch |
| `cw_log_events` | `[]CWLogEvent` | CloudWatch |
| `ec2_vpcs` | `EC2Vpc` | EC2 |
| `ec2_subnets` | `EC2Subnet` | EC2 |
| `ec2_security_groups` | `EC2SecurityGroup` | EC2 |
| `iam_roles` | `IAMRole` | IAM |
| ... | ... | ... |

### GCP (21 tables)
`crj_jobs`, `crj_executions`, `gcf_functions`, `logging_entries`, `dns_zones`, `dns_record_sets`, `gcs_buckets`, `gcs_objects`, `ar_repos`, `ar_docker_images`, `ar_manifests`, `ar_blobs`, `compute_networks`, `iam_service_accounts`, `operations`, `service_usage`, `vpc_connectors`, ...

### Azure (27 tables)
`aca_jobs`, `aca_executions`, `aca_environments`, `acr_registries`, `acr_manifests`, `storage_accounts`, `file_shares`, `azf_sites`, `monitor_logs`, `dns_zones`, `network_vnets`, `network_nsgs`, `resource_groups`, ...

## Store Interface

```go
type Store[T any] interface {
    Get(id string) (T, bool)
    Put(id string, item T)
    Delete(id string) bool
    List() []T
    Filter(fn func(T) bool) []T
    Len() int
    Update(id string, fn func(*T)) bool
}
```

Factory function selects implementation based on database availability:

```go
func MakeStore[T any](db *sql.DB, table string) Store[T]
// Returns SQLiteStore when db != nil, MemoryStore when nil
```

## Process Tracking

Running processes (ECS tasks, Lambda invocations, Cloud Run executions) are tracked via PID files for recovery after restart.

Source: `simulator-{aws,gcp,azure}/shared/process_tracker.go`

### PID File Layout

```
{SIM_DATA_DIR}/pids/
├── {taskID-1}.pid    # contains: 12345
├── {taskID-2}.pid    # contains: 12346
└── {execID-1}.pid    # contains: 12347
```

### Recovery Flow

On simulator restart with persistence enabled:

1. Open SQLite database (all resource state restored automatically)
2. Scan PID directory for `.pid` files
3. For each PID file, check if process is alive (`kill -0 $pid`)
4. Live processes: re-attach to `sync.Map` for monitoring
5. Dead processes: clean up PID file, update resource state to STOPPED

### Naming Convention

PID files use the same ID as the cloud resource:
- AWS ECS: task ID (UUID portion of ARN)
- AWS Lambda: request ID
- GCP Cloud Run: execution name
- Azure ACA: execution ID
