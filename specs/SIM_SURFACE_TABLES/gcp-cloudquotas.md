# Sim surface — gcp-cloudquotas

Surface registered in `simulator-gcp/cloudquotas.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/locations/{location}/quotaPreferences` | ✓ `simulator-gcp/cloudquotas.go:135::handleCreateQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/quotaPreferences` | ✓ `simulator-gcp/cloudquotas.go:136::handleListQuotaPreferences` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/quotaPreferences/{preference}` | ✓ `simulator-gcp/cloudquotas.go:137::handleGetQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/quotaPreferences/{preference}` | ✓ `simulator-gcp/cloudquotas.go:138::handlePatchQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/services/{service}/quotaInfos` | ✓ `simulator-gcp/cloudquotas.go:139::handleListQuotaInfos` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/services/{service}/quotaInfos/{quotaInfo}` | ✓ `simulator-gcp/cloudquotas.go:140::handleGetQuotaInfo` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1beta/projects/{project}/locations/{location}/quotaPreferences` | ✓ `simulator-gcp/cloudquotas.go:142::handleCreateQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/quotaPreferences` | ✓ `simulator-gcp/cloudquotas.go:143::handleListQuotaPreferences` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/quotaPreferences/{preference}` | ✓ `simulator-gcp/cloudquotas.go:144::handleGetQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1beta/projects/{project}/locations/{location}/quotaPreferences/{preference}` | ✓ `simulator-gcp/cloudquotas.go:145::handlePatchQuotaPreference` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/services/{service}/quotaInfos` | ✓ `simulator-gcp/cloudquotas.go:146::handleListQuotaInfos` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/services/{service}/quotaInfos/{quotaInfo}` | ✓ `simulator-gcp/cloudquotas.go:147::handleGetQuotaInfo` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:149::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:150::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:151::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:152::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:153::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:154::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/quotaAdjusterSettings` | 501 `simulator-gcp/cloudquotas.go:155::cloudQuotaAdjusterUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/quotaAdjusterSettings` | 501 `simulator-gcp/cloudquotas.go:156::cloudQuotaAdjusterUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/gdcZones/{rest...}` | 501 `simulator-gcp/cloudquotas.go:157::cloudQuotaGDCUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:159::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1beta/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:160::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1beta/folders/{folder}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:161::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:162::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1beta/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:163::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1beta/organizations/{organization}/locations/{location}/{rest...}` | 501 `simulator-gcp/cloudquotas.go:164::cloudQuotaFolderOrOrganizationUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/quotaAdjusterSettings` | 501 `simulator-gcp/cloudquotas.go:165::cloudQuotaAdjusterUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1beta/projects/{project}/locations/{location}/quotaAdjusterSettings` | 501 `simulator-gcp/cloudquotas.go:166::cloudQuotaAdjusterUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1beta/projects/{project}/locations/{location}/gdcZones/{rest...}` | 501 `simulator-gcp/cloudquotas.go:167::cloudQuotaGDCUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
