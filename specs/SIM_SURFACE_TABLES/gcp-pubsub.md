# Sim surface — gcp-pubsub

Surface registered in `simulator-gcp/pubsub.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `PUT /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:122::handlePSCreateTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:123::handlePSGetTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics` | ✓ `simulator-gcp/pubsub.go:124::handlePSListTopics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:125::handlePSDeleteTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/topics/{topicVerb}` | ✓ `simulator-gcp/pubsub.go:126::handlePSTopicVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:129::handlePSCreateSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:130::handlePSPatchSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:131::handlePSGetSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/subscriptions` | ✓ `simulator-gcp/pubsub.go:132::handlePSListSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:133::handlePSDeleteSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/subscriptions/{subVerb}` | ✓ `simulator-gcp/pubsub.go:134::handlePSSubscriptionVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:140::handlePSPatchTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}/snapshots` | ✓ `simulator-gcp/pubsub.go:146::handlePSListTopicSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}/subscriptions` | ✓ `simulator-gcp/pubsub.go:147::handlePSListTopicSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:154::handlePSCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:155::handlePSPatchSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:156::handlePSGetSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/snapshots` | ✓ `simulator-gcp/pubsub.go:157::handlePSListSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:158::handlePSDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/snapshots/{snapVerb}` | ✓ `simulator-gcp/pubsub.go:159::handlePSSnapshotVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas` | ✓ `simulator-gcp/pubsub.go:166::handlePSCreateSchema` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas:validate` | ○ `simulator-gcp/pubsub.go:167::handlePSValidateSchema` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas:validateMessage` | ✓ `simulator-gcp/pubsub.go:168::handlePSValidateMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/schemas` | ✓ `simulator-gcp/pubsub.go:169::handlePSListSchemas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:170::handlePSGetSchemaOrVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:171::handlePSSchemaPostVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:172::handlePSDeleteSchemaOrRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
