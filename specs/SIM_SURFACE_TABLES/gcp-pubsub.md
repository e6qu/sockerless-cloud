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
| `PUT /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:138::handlePSCreateTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:139::handlePSGetTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics` | ✓ `simulator-gcp/pubsub.go:140::handlePSListTopics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:141::handlePSDeleteTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/topics/{topicVerb}` | ✓ `simulator-gcp/pubsub.go:142::handlePSTopicVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:145::handlePSCreateSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:146::handlePSPatchSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:147::handlePSGetSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/subscriptions` | ✓ `simulator-gcp/pubsub.go:148::handlePSListSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/subscriptions/{sub}` | ✓ `simulator-gcp/pubsub.go:149::handlePSDeleteSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/subscriptions/{subVerb}` | ✓ `simulator-gcp/pubsub.go:150::handlePSSubscriptionVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/topics/{topic}` | ✓ `simulator-gcp/pubsub.go:156::handlePSPatchTopic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}/snapshots` | ✓ `simulator-gcp/pubsub.go:162::handlePSListTopicSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/topics/{topic}/subscriptions` | ✓ `simulator-gcp/pubsub.go:163::handlePSListTopicSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:170::handlePSCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:171::handlePSPatchSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:172::handlePSGetSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/snapshots` | ✓ `simulator-gcp/pubsub.go:173::handlePSListSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/snapshots/{snap}` | ✓ `simulator-gcp/pubsub.go:174::handlePSDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/snapshots/{snapVerb}` | ✓ `simulator-gcp/pubsub.go:175::handlePSSnapshotVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas` | ✓ `simulator-gcp/pubsub.go:182::handlePSCreateSchema` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas:validate` | ○ `simulator-gcp/pubsub.go:183::handlePSValidateSchema` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas:validateMessage` | ✓ `simulator-gcp/pubsub.go:184::handlePSValidateMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/schemas` | ✓ `simulator-gcp/pubsub.go:185::handlePSListSchemas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:186::handlePSGetSchemaOrVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:187::handlePSSchemaPostVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/schemas/{schemaVerb}` | ✓ `simulator-gcp/pubsub.go:188::handlePSDeleteSchemaOrRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
