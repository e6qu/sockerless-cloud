# Sim surface — gcp-eventarc

Surface registered in `simulator-gcp/eventarc.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/locations/{location}/triggers` | ✓ `simulator-gcp/eventarc.go:118::handleGCPRegionalTriggerCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/triggers` | ✓ `simulator-gcp/eventarc.go:119::handleGCPRegionalTriggerList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:120::handleGCPRegionalTriggerGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:121::handleGCPRegionalTriggerPatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/triggers/{trigger}` | ✓ `simulator-gcp/eventarc.go:122::handleGCPRegionalTriggerDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/triggers/{triggerAction}` | ✓ `simulator-gcp/eventarc.go:123::handleEventarcTriggerIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channels` | ✓ `simulator-gcp/eventarc.go:124::handleEventarcCreateChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channels` | ✓ `simulator-gcp/eventarc.go:125::handleEventarcListChannels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:126::handleEventarcGetChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:127::handleEventarcPatchChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/channels/{channel}` | ✓ `simulator-gcp/eventarc.go:128::handleEventarcDeleteChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channels/{channelAction}` | ✓ `simulator-gcp/eventarc.go:129::handleEventarcChannelIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/providers` | ✓ `simulator-gcp/eventarc.go:130::handleEventarcListProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/providers/{provider}` | ✓ `simulator-gcp/eventarc.go:131::handleEventarcGetProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channelConnections` | ✓ `simulator-gcp/eventarc.go:132::handleEventarcCreateChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channelConnections` | ✓ `simulator-gcp/eventarc.go:133::handleEventarcListChannelConnections` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/channelConnections/{connection}` | ✓ `simulator-gcp/eventarc.go:134::handleEventarcGetChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/channelConnections/{connection}` | ✓ `simulator-gcp/eventarc.go:135::handleEventarcDeleteChannelConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/channelConnections/{connectionAction}` | ✓ `simulator-gcp/eventarc.go:136::handleEventarcChannelConnectionIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/enrollments` | ✓ `simulator-gcp/eventarc.go:139::handleEventarcCreateEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/enrollments` | ✓ `simulator-gcp/eventarc.go:140::handleEventarcListEnrollments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:141::handleEventarcGetEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:142::handleEventarcPatchEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/enrollments/{enrollment}` | ✓ `simulator-gcp/eventarc.go:143::handleEventarcDeleteEnrollment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/enrollments/{enrollmentAction}` | ✓ `simulator-gcp/eventarc.go:144::handleEventarcEnrollmentIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/messageBuses` | ✓ `simulator-gcp/eventarc.go:147::handleEventarcCreateMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/messageBuses` | ✓ `simulator-gcp/eventarc.go:148::handleEventarcListMessageBuses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:149::handleEventarcMessageBusGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:150::handleEventarcPatchMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/messageBuses/{bus}` | ✓ `simulator-gcp/eventarc.go:151::handleEventarcDeleteMessageBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/messageBuses/{busAction}` | ✓ `simulator-gcp/eventarc.go:152::handleEventarcMessageBusIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/pipelines` | ✓ `simulator-gcp/eventarc.go:155::handleEventarcCreatePipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/pipelines` | ✓ `simulator-gcp/eventarc.go:156::handleEventarcListPipelines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:157::handleEventarcGetPipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:158::handleEventarcPatchPipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/pipelines/{pipeline}` | ✓ `simulator-gcp/eventarc.go:159::handleEventarcDeletePipeline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/pipelines/{pipelineAction}` | ✓ `simulator-gcp/eventarc.go:160::handleEventarcPipelineIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/googleApiSources` | ✓ `simulator-gcp/eventarc.go:163::handleEventarcCreateGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleApiSources` | ✓ `simulator-gcp/eventarc.go:164::handleEventarcListGoogleAPISources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:165::handleEventarcGetGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:166::handleEventarcPatchGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/googleApiSources/{source}` | ✓ `simulator-gcp/eventarc.go:167::handleEventarcDeleteGoogleAPISource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/googleApiSources/{sourceAction}` | ✓ `simulator-gcp/eventarc.go:168::handleEventarcGoogleAPISourceIAMAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/googleChannelConfig` | ✓ `simulator-gcp/eventarc.go:171::handleEventarcGetGoogleChannelConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/googleChannelConfig` | ✓ `simulator-gcp/eventarc.go:172::handleEventarcUpdateGoogleChannelConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations` | ✓ `simulator-gcp/eventarc.go:175::handleEventarcListLocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}` | ✓ `simulator-gcp/eventarc.go:176::handleEventarcGetLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/operations` | ✓ `simulator-gcp/eventarc.go:177::handleEventarcListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/operations/{operation}` | ✓ `simulator-gcp/eventarc.go:178::handleEventarcDeleteOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
