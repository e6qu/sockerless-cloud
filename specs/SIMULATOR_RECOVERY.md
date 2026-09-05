# Simulator Recovery Specification

How a simulator started with `SIM_PERSIST` recovers its state and its running
workloads after a restart.

## State

Every resource lives in a `Store[T]` backed by SQLite at
`{SIM_DATA_DIR}/simulator.db` (see [`SIMULATOR_PERSISTENCE.md`](SIMULATOR_PERSISTENCE.md)).
Opening the database restores every resource; there is no load step. Internal
fields hidden from the wire (`json:"-"`) survive through the persistence
envelope, and fields that describe process coordination (`persist:"-"`) are
rebuilt rather than restored.

## Workloads

A workload is a container on the engine the simulator started against, and the
container carries the identity needed to find it again:

| Label | Value |
|-------|-------|
| `sockerless-sim` | `true` |
| `sockerless-sim-provider` | `aws`, `gcp` or `azure` |
| `sockerless-sim-run` | the run identifier of the process that started it |
| `sockerless-sim-state` | the state directory's identity, when persistence is enabled |
| service labels | the cloud resource the container belongs to (task ARN, execution name, site name, …), supplied by the service slice at creation |

Shutdown with persistence enabled leaves the containers running, and the
container reaper and the startup sweep are not started for a persistent run,
because the next process will adopt what this one leaves. Without persistence,
the detached reaper collects the run's containers once the simulator exits,
and the next simulator's startup sweep collects what a killed run left. Both
are scoped by `sockerless-sim-state`, so a concurrent suite under another state
directory is never touched.

## Recovery flow

On startup with persistence enabled, each service slice that runs workloads:

1. Reads its durable resources whose recorded state says a workload should
   exist (a RUNNING Amazon ECS task, a running Cloud Run execution, an Azure
   Container Apps replica, a database server's engine).
2. Calls `sim.FindExistingContainers` with the same labels it supplied at
   creation. The answer includes exited containers whose terminal result has
   not yet been reconciled into the resource, with their published ports.
3. For a running container, calls `sim.AdoptContainer`, which attaches the
   lifecycle observation — log streaming into the cloud's logging service, the
   exit watch, removal on exit — without restarting anything, and re-binds any
   recorded data-plane address.
4. For an exited container, records the exit the way the live watch would have
   (the task STOPPED with its exit code, the execution failed or succeeded) and
   removes the container; a workload the cloud would restart is started again
   with `sim.StartExistingContainer`, preserving its filesystem, mounts and
   port bindings.
5. Marks a resource whose container is gone entirely as stopped with the reason
   the cloud reports for a lost host.

`sim.RequireContainerRuntime` refuses each of these on an API-only process, so
a `SIM_RUNTIME=process` restart reports that it holds no engine rather than
silently marking every workload lost.

## Data planes

Managed-database instances (Amazon RDS, Cloud SQL, Azure Database for
PostgreSQL) recover the same way: the engine container is adopted, its listener
is re-bound to the address the resource records, and readiness is re-checked
against the engine itself before a connection is served.
