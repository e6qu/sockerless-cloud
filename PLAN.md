# PLAN

The simulators serve their declared surfaces: every vendored AWS model is
implemented or exempt, and every Google Cloud Discovery method and Azure
Swagger operation reaches a route that serves it or answers a declared 501
naming what is missing. `STATUS.md` is the snapshot, `DO_NEXT.md` the work
items, `WHAT_WE_DID.md` the decisions behind the code and `BUGS.md` the open
bugs.

## Shared functionality in cloud-neutral packages

Each mechanism the three simulators implement alike moves into a cloud-neutral
package under `sim/` (or `realexec/` where it touches the host's network or
microVMs); each cloud's wire shapes, error envelopes and naming stay in its own
module and reach the shared code through hooks. A stage lands with the
simulators switched onto the shared package and the per-cloud copies deleted.

Landed:

1. Workload host and launch helpers — `sim/workloadhost`, `sim/workload`,
   `sim/bg`, `sim/archive`.
2. Identity — `sim/simjwt`, `sim/oidcfed`, and registry authentication in
   `sim` (`ociauth.go`, `registry_credential.go`).
3. Database engine — `sim/dbengine` with its `pgwire` and `mysqlwire` relays.
4. Network fabric and load balancing — `realexec/fabric`, `realexec/lbplane`.
5. Object storage — `sim.PrefixStore`, `sim/blobstore`, `sim/sparse`,
   `Server.Payloads`, `sim.KeyedLocks`.
6. List queries and key-value primitives — `sim/listq`, `sim/kvstore`.
7. Messaging, delivery, cron and streams — `sim/msgq`, `sim/delivery`,
   `sim/cron`, `sim/streamlog`.
8. Logs and key management — `sim/simkms` for key material with sealing,
   and `sim/logstore`, which Amazon CloudWatch Logs, Cloud Logging and Azure
   Monitor Logs keep their entries in, each with its service's retention.

Remaining:

9. DNS — zones, records and resolution inside workload networks.
10. Startup plumbing — configuration, engine readiness and the shared `main`
    sequence.

## Maintenance

Keep the vendored specifications in sync with upstream and serve what a
re-vendor adds, hold the measured floors, and close the open bugs in
`BUGS.md` as their blockers lift.
