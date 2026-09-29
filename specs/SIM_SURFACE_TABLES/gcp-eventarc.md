# Sim surface — gcp-eventarc

Surface registered in `simulator-gcp/eventarc.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/locations/{location}/triggers` | ✓ `simulator-gcp/eventarc.go:123::handleGCPRegionalTriggerCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/triggers` | ✓ `simulator-gcp/eventarc.go:124::handleGCPRegionalTriggerList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:125::handleGCPRegionalTriggerGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:126::handleGCPRegionalTriggerPatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:127::handleGCPRegionalTriggerDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/triggers/{triggerAction}` | ✓ `simulator-gcp/eventarc.go:128::handleEventarcTriggerIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channels` | ✓ `simulator-gcp/eventarc.go:129::handleEventarcCreateChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channels` | ✓ `simulator-gcp/eventarc.go:130::handleEventarcListChannels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:131::handleEventarcGetChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:132::handleEventarcPatchChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:133::handleEventarcDeleteChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channels/{channelAction}` | ✓ `simulator-gcp/eventarc.go:134::handleEventarcChannelIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/providers` | ○ `simulator-gcp/eventarc.go:135::handleEventarcListProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/providers/{provider}` | ○ `simulator-gcp/eventarc.go:136::handleEventarcGetProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channelConnections` | ✓ `simulator-gcp/eventarc.go:137::handleEventarcCreateChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channelConnections` | ✓ `simulator-gcp/eventarc.go:138::handleEventarcListChannelConnections` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channelConnections/{connection}` | ✓ `simulator-gcp/eventarc.go:139::handleEventarcGetChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/channelConnections/{connection}` | ✓ `simulator-gcp/eventarc.go:140::handleEventarcDeleteChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channelConnections/{connectionAction}` | ✓ `simulator-gcp/eventarc.go:141::handleEventarcChannelConnectionIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/enrollments` | ✓ `simulator-gcp/eventarc.go:144::handleEventarcCreateEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/enrollments` | ✓ `simulator-gcp/eventarc.go:145::handleEventarcListEnrollments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:146::handleEventarcGetEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:147::handleEventarcPatchEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:148::handleEventarcDeleteEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/enrollments/{enrollmentAction}` | ✓ `simulator-gcp/eventarc.go:149::handleEventarcEnrollmentIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/messageBuses` | ✓ `simulator-gcp/eventarc.go:152::handleEventarcCreateMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/messageBuses` | ✓ `simulator-gcp/eventarc.go:153::handleEventarcListMessageBuses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:154::handleEventarcMessageBusGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:155::handleEventarcPatchMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:156::handleEventarcDeleteMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/messageBuses/{busAction}` | ✓ `simulator-gcp/eventarc.go:157::handleEventarcMessageBusIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/pipelines` | ✓ `simulator-gcp/eventarc.go:160::handleEventarcCreatePipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/pipelines` | ✓ `simulator-gcp/eventarc.go:161::handleEventarcListPipelines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:162::handleEventarcGetPipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:163::handleEventarcPatchPipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:164::handleEventarcDeletePipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/pipelines/{pipelineAction}` | ✓ `simulator-gcp/eventarc.go:165::handleEventarcPipelineIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/googleApiSources` | ✓ `simulator-gcp/eventarc.go:168::handleEventarcCreateGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleApiSources` | ✓ `simulator-gcp/eventarc.go:169::handleEventarcListGoogleAPISources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:170::handleEventarcGetGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:171::handleEventarcPatchGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:172::handleEventarcDeleteGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/googleApiSources/{sourceAction}` | ✓ `simulator-gcp/eventarc.go:173::handleEventarcGoogleAPISourceIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleChannelConfig` | ✓ `simulator-gcp/eventarc.go:176::handleEventarcGetGoogleChannelConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/googleChannelConfig` | ✓ `simulator-gcp/eventarc.go:177::handleEventarcUpdateGoogleChannelConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations` | ○ `simulator-gcp/eventarc.go:180::handleEventarcListLocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}` | ○ `simulator-gcp/eventarc.go:181::handleEventarcGetLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/operations` | ✓ `simulator-gcp/eventarc.go:182::handleEventarcListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/operations/{operation}` | ✓ `simulator-gcp/eventarc.go:183::handleEventarcDeleteOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
