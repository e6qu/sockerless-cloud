# Sim surface — gcp-cloudresourcemanager

Surface registered in `simulator-gcp/cloudresourcemanager.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects` | ✓ `simulator-gcp/cloudresourcemanager.go:576::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects` | ✓ `simulator-gcp/cloudresourcemanager.go:607::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager.go:639::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager.go:661::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{projectAction}` | ✓ `simulator-gcp/cloudresourcemanager.go:687::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/folders/{folderAction}` | ✓ `simulator-gcp/cloudresourcemanager.go:740::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{org}` | ✓ `simulator-gcp/cloudresourcemanager.go:759::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations:search` | ✓ `simulator-gcp/cloudresourcemanager.go:767::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{orgAction}` | ✓ `simulator-gcp/cloudresourcemanager.go:793::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/liens` | ✓ `simulator-gcp/cloudresourcemanager.go:815::crmCreateLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/liens` | ✓ `simulator-gcp/cloudresourcemanager.go:816::crmListLiens` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/liens/{lien}` | ✓ `simulator-gcp/cloudresourcemanager.go:817::crmGetLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/liens/{lien}` | ✓ `simulator-gcp/cloudresourcemanager.go:818::crmDeleteLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/{operation}` | ✓ `simulator-gcp/cloudresourcemanager.go:823::crmGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/operations/{operation}` | ✓ `simulator-gcp/cloudresourcemanager.go:824::crmGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/billingInfo` | ✓ `simulator-gcp/cloudresourcemanager.go:831::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
The project store is shared between the v1 surface here and the v3 surface in `iam.go`: one `CRMProject` row keyed by project ID, whose `name` carries the real `projects/{projectNumber}` v3 resource name; every method resolves a `{project}` path segment by ID or number. A project the sim has never seen is a real 403 PERMISSION_DENIED (the API never discloses existence), a duplicate create is a real 409 ALREADY_EXISTS, and delete/undelete follow the real ACTIVE ⇄ DELETE_REQUESTED soft-delete state machine. The organization's pre-provisioned projects ("sockerless" — the deployment default, project number 123456789012 — and "test-project", the SDK/CLI/terraform harness project) are materialized at startup the way the AWS simulator materializes its management account. Tested by `simulator-gcp/sdk-tests/resourcemanager_projects_test.go` (apiv3 GAPIC + v1 legacy client + cloudbilling), `simulator-gcp/cli-tests/projects_test.go` (`gcloud projects create/list/describe/update/delete/undelete/get-iam-policy`), and `simulator-gcp/terraform-tests/main.tf` (`google_project` with `deletion_policy = "DELETE"`, riding `resource_manager_custom_endpoint` + `cloud_billing_custom_endpoint`).
<!-- HAND-WRITTEN END -->
