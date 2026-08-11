# Sim surface — gcp-cloudrun

Surface registered in `simulator-gcp/cloudrun.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:381::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:431::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:444::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:462::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:509::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:561::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:562::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:563::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:564::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:602::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:603::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:604::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:605::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:606::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:607::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:635::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:636::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:637::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:638::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:717::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:718::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:719::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:720::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:721::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:722::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:723::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:724::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:733::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:734::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:735::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:742::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:782::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:799::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:812::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:851::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}` | ✓ `simulator-gcp/cloudrun.go:874::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:70::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:96::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:118::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:139::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:188::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}` | ✓ `simulator-gcp/cloudruninstances.go:205::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:540::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:605::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:638::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:663::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrunjobs.go:696::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:900::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions` | ✓ `simulator-gcp/cloudrunjobs.go:916::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}` | ✓ `simulator-gcp/cloudrunjobs.go:942::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:989::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:1040::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}` | ✓ `simulator-gcp/cloudrunjobs.go:1061::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks` | ✓ `simulator-gcp/cloudrunjobs.go:1077::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:625::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:670::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:693::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:716::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:743::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:816::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions` | ✓ `simulator-gcp/cloudrunservices.go:829::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services/{serviceAction}` | ✓ `simulator-gcp/cloudrunservices.go:867::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/operations/{operation}` | ✓ `simulator-gcp/cloudrunservices.go:888::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:900::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}` | ✓ `simulator-gcp/cloudrunservices.go:985::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}/{path...}` | ✓ `simulator-gcp/cloudrunservices.go:986::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:116::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:143::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:165::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:186::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:234::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions` | ✓ `simulator-gcp/cloudrunworkerpools.go:267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:287::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:305::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:332::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:342::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->

## Discovery-revision mismatches

`GoogleCloudRunV2WorkerPoolScaling` is modelled with four members —
`scalingMode`, `minInstanceCount`, `maxInstanceCount` and
`manualInstanceCount` — while the pinned Cloud Run v2 Discovery document
(revision 20260603) and the published REST reference both declare only
`manualInstanceCount`. The three automatic-scaling members are real: the
official `hashicorp/google` provider's `google_cloud_run_v2_worker_pool`
resource exposes `scaling.scaling_mode` (`AUTOMATIC`/`MANUAL`),
`scaling.min_instance_count` and `scaling.max_instance_count`, and sends them
on the v2 wire under those camelCase names; `gcloud beta run worker-pools
deploy --min-instances/--max-instances` reaches the same members. Refreshing
the pin does not close the gap — Discovery revision 20260713, the newest
served by `run.googleapis.com/$discovery/rest?version=v2`, still publishes the
single member, and the `manual_instance_count` field number (6) in the
published `google.cloud.run.v2` protos shows the automatic-scaling members
occupy the unpublished 1–5 range. The simulator therefore models what the
official clients speak, and the runtime spec-validator's `unknown-field`
findings for these three members are allowlisted in
`simulator-gcp/spec-violation-allowlist.txt` until Google publishes them.

## Collapsed-port host disambiguation

Real Google Cloud serves `/v1/projects/{p}/locations/{l}/instances/…` from two
different hosts: Cloud Run Admin v1 (`run.googleapis.com`) exposes the
instances IAM triple there, and Memorystore for Redis
(`redis.googleapis.com`) exposes the Redis instance lifecycle. The
single-origin simulator resolves the owner in
`simulator-gcp/endpoint_hosts.go`: the request `Host` names the service when
the client resolves a real Google host, and otherwise the AIP-136 custom
method in the URI does — the IAM triple
(`getIamPolicy`/`setIamPolicy`/`testIamPermissions`) belongs to Cloud Run
alone and `export`/`failover`/`import`/`upgrade`/`rescheduleMaintenance` to
Memorystore alone.

<!-- HAND-WRITTEN END -->
