# Simulator Persistence Specification

How the cloud simulators persist state across restarts via SQLite.

## Architecture

Every simulator keeps its resources in `Store[T]` instances with two
implementations:

- **MemoryStore[T]**: an in-memory `map[string]T`, the default.
- **SQLiteStore[T]**: one `(key TEXT, value BLOB)` table per store instance.

Source: `sim/state.go`, `sim/state_sqlite.go`, `sim/db.go`, `sim/index.go`,
`sim/store_scan.go`.

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `SIM_PERSIST` | `false` | Enable SQLite persistence (`true` or `1`) |
| `SIM_DATA_DIR` | `/tmp/sockerless-sim-{cloud}` | Directory for the database; resolved to an absolute path at startup |

When persistence is disabled every store is in memory and state is lost on
restart. When enabled, SQLite stores data at `{SIM_DATA_DIR}/simulator.db`, and
the state directory becomes the simulator's identity to the workload sweep: a
run collects what an earlier run over the same directory left behind, and a
concurrent suite's workloads under another directory are never touched.

## SQLite Configuration

- **Journal mode**: WAL, so reads proceed during writes.
- **Busy timeout**: 5000ms, waiting for a lock instead of failing.
- **Synchronous**: NORMAL. FULL fsynced the WAL on every commit and serialized
  every write behind that fsync; NORMAL still fsyncs at every checkpoint and is
  safe against a process crash, giving up only the case of the host losing
  power between a commit and its next checkpoint.
- **Driver**: `modernc.org/sqlite`, CGO-free.

Orderly shutdown drains every background worker the server started before it
checkpoints and closes the database, so no service can query durable state
after the database is closed.

## Schema

Each `Store[T]` instance maps to one SQLite table:

```sql
CREATE TABLE IF NOT EXISTS {table_name} (
    key   TEXT PRIMARY KEY,
    value BLOB
);
```

Values are JSON-serialized inside a persistence envelope. Resource structs
double as the cloud's wire shapes, so a field tagged `json:"-"` is hidden from
API responses; the envelope stores those exported fields in a sidecar so they
survive a restart, and a field tagged `persist:"-"` (a channel, a process
handle) is left out. Rows written before the envelope existed remain readable.

## Table Naming Convention

`{service}_{resource}` in lowercase — `ecs_clusters`, `gcs_objects`,
`acr_manifests`. The authoritative list is the set of `MakeStore` calls in each
simulator; every store a build creates joins the tracked set
(`sim.TrackedStores`), which is what the Azure cross-resource-group move
scans.

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
    Upsert(id string, fn func(*T))
    Generation() uint64
}
```

`Upsert` applies a read-modify-write under one lock, creating the row from the
zero value when it is absent, so a create-or-modify never races a concurrent
writer the way an `Update`-then-`Put` pair would.

`Generation` is a process-wide counter every store advances on a write that
changed it. `GenerationIndex` derives a keyed lookup from `List` and rebuilds
only when the generation moves, which is how every handler wrapper that decides
whether to claim a request avoids reading a whole store per request;
`scripts/check-store-scans.sh` holds request-path full-store reads at zero.

The factory selects the implementation:

```go
func MakeStore[T any](db *sql.DB, table string) Store[T]
// SQLiteStore when db != nil, MemoryStore when nil
```

## Workload Recovery

A persistent simulator's workloads are containers labelled with the state
directory's identity and the cloud resource they belong to. On restart a
service slice finds its containers through `sim.FindExistingContainers` by the
same labels it supplied at creation, adopts a running one with
`sim.AdoptContainer` so its exit is observed and recorded, and decides itself
whether an exited workload stays terminal or resumes. Shutdown with persistence
enabled leaves the containers running, since the next process will adopt them;
without persistence it removes them.
