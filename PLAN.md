# PLAN

The simulators serve their declared surfaces: every vendored AWS model is
implemented or exempt, and every Google Cloud Discovery method and Azure
Swagger operation reaches a route that serves it or answers a declared 501
naming what is missing. The repository snapshot is `STATUS.md`; the current
work items are `DO_NEXT.md`; the decisions behind the code are
`WHAT_WE_DID.md`; open bugs are `BUGS.md`.

## Current work — extract the shared functionality into cloud-neutral packages

The three simulators each implement the same mechanisms for their own cloud.
The plan moves each mechanism into a cloud-neutral package under `sim/` (or
`realexec/` where it touches the host's network or microVMs), leaving each
cloud's wire shapes, error envelopes and naming in its own module and reaching
the shared code through hooks, as the framework already does. Each stage
lands with the simulators switched onto the shared package and the per-cloud
copies deleted.

1. **Workload host and launch helpers** — starting, observing and stopping a
   workload container, its grace, sandbox and log streaming.
2. **Identity** — JWT and OpenID Connect issuance and verification, and
   container-registry authentication.
3. **Database engine** — the managed-database engine lifecycle, readiness,
   credentials and snapshots.
4. **Network fabric and load balancing** — VPC networks, addressing, security
   policy and load-balancer data planes.
5. **Object storage** — prefix-keyed stores, payload files and conditional
   writes.
6. **List queries and key-value primitives** — paging, filters and keyed
   lookups.
7. **Messaging, delivery, cron and streams** — queues, topics, fan-out,
   schedules and ordered streams.
8. **Logs and key management** — log ingestion, retention and query, and key
   material with sealing.
9. **DNS** — zones, records and resolution inside workload networks.
10. **Startup plumbing** — configuration, engine readiness and the shared
    `main` sequence.

## Then — maintenance

Keep the vendored specifications in sync with upstream and serve what a
re-vendor adds, hold the measured floors, and close the open bugs in
`BUGS.md` as their blockers lift.
