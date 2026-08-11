---
name: sim-state-machine-completeness
description: Audit sim resource types against their documented state machines. Real cloud resources transition through lifecycle states (creating → available → deleted → failed; active → soft-deleted → purged; READY → FAILING_OVER → READY). Sim implementations that store one flat row per name with no state field and "PUT overwrites, DELETE deletes" silently lose the transitions SDKs and providers depend on reading back. Distilled from BUG-1160 — the meta-shape behind issues #203 (KV versioning), #205 (KV soft-delete), #207 (RDS snapshot states), #209 (Cloud SQL backups, Memorystore failover, Pub/Sub Snapshot lifecycle). Use whenever adding or modifying a stateful sim resource type, and audit against every row in `specs/SIM_SURFACE_TABLES/` whose handler implements a lifecycle.
---

# Sim state-machine completeness invariant

## The class of bug

Real cloud resources are not flat key-value stores. They are state machines:

- **Azure KV secret**: PUT creates a new version (UUID); the secret is a versioned chain. DELETE moves into a soft-deleted state (recoverable for the retention window). RECOVER promotes back to active. PURGE removes permanently.
- **AWS RDS snapshot**: `creating → available → deleted` (success path); `creating → failed` (error path). `DescribeDBSnapshots` returns the snapshot in any state, including the intermediate one.
- **GCP Memorystore upgrade**: `READY → MAINTENANCE → READY` (success) or `READY → MAINTENANCE → FAILED`. SDKs poll the instance during the transition.
- **AWS Lambda function version**: `Pending → Active` or `Pending → Failed`. The `State` field is part of the GetFunction response shape.

Sim implementations historically store one row per resource ID with no state field. PUT overwrites the value verbatim. DELETE removes the row entirely. The resource lifecycle is collapsed into binary "exists / doesn't exist". SDKs that read `obj.State` get an empty string or zero value; providers that poll for `available` time out.

This is BUG-1160 in canonical form. It's the class of bug behind:

- **#203 (BUG-1149)** — KV PUT overwrites instead of creating a new version. The version chain is unmodelled.
- **#205 (BUG-1151)** — KV DELETE removes the secret instead of moving to soft-deleted state. `/deletedsecrets/...` recovery surface is unreachable because nothing ever enters it.
- **#207 (BUG-1153)** — RDS `CreateDBSnapshot` returns a snapshot with no `Status`; `DescribeDBSnapshots` shape lacks the state machine field.
- **#209 (BUG-1155)** — Cloud SQL backups have no state lifecycle; Memorystore `:failover` is a no-op because there's no failover-in-progress state to expose.

## When this skill applies

- Adding a new sim resource type with a lifecycle (creation that takes time, soft-delete, failover, snapshot, version chain).
- Editing a handler that creates / deletes / mutates a stateful resource.
- Reviewing a community-filed issue that names a state transition (`state`, `status`, `phase`, `lifecycleState`, `provisioningState`).
- Periodically — re-read this skill alongside `surface-table-completeness` to identify rows whose handler is a state machine but whose stored shape is flat.

## The rule

**Every sim resource type with a documented state machine MUST model the states explicitly in its stored shape.**

Specifically:

1. The stored struct has a `State` (or `Status` / `Phase` / `LifecycleState` / `ProvisioningState` — match the cloud provider's field name) field.
2. Every transition the documentation calls out has a code path that sets the new state, atomically with whatever side effect drives the transition (creating bytes, marking deleted, scheduling purge).
3. The state field is read back by every shape-emitting handler — `Get`, `Describe`, `List`, the LRO operation that polled for completion.
4. State transitions that real cloud reports as "in progress" briefly (provisioning, restoring, deleting) may be modelled as either (a) two-shot — transient state visible for one read, then settled, or (b) immediate-settle — the operation completes inline but the state field reflects the canonical settled value. Pick one explicitly per resource type; document the choice in the surface table.

## How to apply

### When implementing a new stateful handler

1. Read the cloud provider's REST docs for the resource type. Identify every state in the lifecycle (look for `status`, `state`, `phase`, `lifecycleState`, `provisioningState`).
2. Add the corresponding field to the stored struct. Use the provider's canonical capitalisation (`Available` vs `available` matters — AWS uses lowercase strings, Azure uses `Succeeded`/`Failed`/`Creating` capitalisation).
3. Implement transitions in the operation handler. `CreateSnapshot` writes `Status: "creating"` then `Status: "available"` (either inline-settle or via a second `Get`-reads-and-promotes pattern).
4. Soft-delete: don't `store.Delete(key)`. Instead, mark the entry with `DeletedAt: now` + add to a `deletedStore`. `Recover` moves it back. `Purge` actually deletes.
5. Add an sdk-test that drives the full lifecycle: create → assert state `creating`/`available` → delete → assert in deleted-state → recover → assert active → purge → assert gone.
6. Update the surface table — add a `state-machine verified` notation in the notes column.

### When auditing a PR

For every changed handler in `simulator-<cloud>/*.go`:

- If the handler creates / deletes / mutates a resource that the cloud docs describe as a state machine: confirm the stored struct has the State field and the handler writes it on the transition.
- If the handler is a `Get` / `Describe` / `List` for such a resource: confirm the response includes the State field.
- Cross-reference against the surface table — `state-machine verified` rows must have the audit done; ✗ rows must be paired with a deferral pointer.

### Refused shortcuts

- *"The state will always be 'available' immediately because sim is instant."* — Even then, the field has to exist + be emitted. The SDK reads it.
- *"DELETE is fine because real consumers don't soft-delete."* — Real Azure KV ALWAYS soft-deletes. Real RDS snapshots can be in `deleting` for minutes. Modelling the state explicitly is the contract, not an optimisation.
- *"Adding the State field breaks existing serialisation."* — Then add it with an omitempty tag + backfill on Get. The cost of one persistence migration is less than one community-filed reopen.

## Worked example — Azure KV soft-delete (Phase 178 Stage D commit 15, BUG-1151)

**State machine**:

```
active → (DELETE) → deleted (recoverable for `softDeleteRetentionInDays`)
                     ↓ (POST /deletedsecrets/{name}/recover)
                     active
                     ↓ (DELETE /deletedsecrets/{name})
                     purged (no recovery possible)
```

**Stored shape**:

```go
type KVSecret struct {
    Name            string
    Vault           string
    Versions        []KVSecretVersion   // BUG-1149's chain
    DeletedAt       *time.Time          // nil while active
    ScheduledPurge  *time.Time          // set when DeletedAt is set
    RecoveryID      string              // UUID stamped at DELETE
}
```

**Transitions**:

- `PUT /secrets/{name}` → append to `Versions`; `DeletedAt = nil`. If a row exists with `DeletedAt != nil`, real KV returns `409 Conflict`; sim does the same.
- `DELETE /secrets/{name}` → set `DeletedAt = now`, `ScheduledPurge = now + 90d`, `RecoveryID = uuid`. The row stays in the primary store (NOT moved to a separate store; that's a different design). All `Get /secrets/{name}` after this return 404. `/deletedsecrets/{name}` GET returns the row.
- `POST /deletedsecrets/{name}/recover` → `DeletedAt = nil`, `ScheduledPurge = nil`, `RecoveryID = ""`. Row reappears in `/secrets/{name}` reads.
- `DELETE /deletedsecrets/{name}` → `store.Delete(key)` — actual removal.

**Surface-table row**:

| Op | sim handler | sdk-test | tf-test | state-machine verified | notes |
|---|---|---|---|---|---|
| `DELETE /secrets/{name}` | ✓ keyvault.go::handleKVSoftDelete | ✓ TestKV_SoftDelete_FullCycle | ✗ (deferred under BUG-1147) | ✓ active → deleted; lifecycle in keyvault.go::KVSecret.DeletedAt |

The `state-machine verified ✓` cell points at the field that carries the state + names the transition. This is the load-bearing artifact — without it, the next developer modifying the handler can lose the state field and the regression ships silently.

## Companion skills

- `surface-table-completeness` — every stateful row in a surface table needs a state-machine assertion. This skill is the per-row audit; surface-table-completeness is the per-table audit.
- `reopen-postmortem` — reopens stemming from collapsed state machines (the user observed a state transition the sim didn't emit) cite this skill as the root-cause.
- `sim-handler-checklist` — the pre-write checklist for new handlers; when the handler is stateful, this skill's rule applies.
