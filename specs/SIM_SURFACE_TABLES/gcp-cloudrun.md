# Sim surface — gcp-cloudrun

Surface registered in `simulator-gcp/cloudrun.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:447::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:497::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:510::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:524::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:575::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}` | ? `simulator-gcp/cloudrun.go:624::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations` | ? `simulator-gcp/cloudrun.go:625::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations/{name}` | ? `simulator-gcp/cloudrun.go:626::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations` | ? `simulator-gcp/cloudrun.go:627::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ? `simulator-gcp/cloudrun.go:660::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions` | ? `simulator-gcp/cloudrun.go:661::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ? `simulator-gcp/cloudrun.go:662::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ? `simulator-gcp/cloudrun.go:663::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions` | ? `simulator-gcp/cloudrun.go:664::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ? `simulator-gcp/cloudrun.go:665::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}` | ? `simulator-gcp/cloudrun.go:688::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes` | ? `simulator-gcp/cloudrun.go:689::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes/{name}` | ? `simulator-gcp/cloudrun.go:690::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes` | ? `simulator-gcp/cloudrun.go:691::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ? `simulator-gcp/cloudrun.go:767::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ? `simulator-gcp/cloudrun.go:768::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ? `simulator-gcp/cloudrun.go:769::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ? `simulator-gcp/cloudrun.go:770::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/domainmappings` | ? `simulator-gcp/cloudrun.go:771::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ? `simulator-gcp/cloudrun.go:772::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings` | ? `simulator-gcp/cloudrun.go:773::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ? `simulator-gcp/cloudrun.go:774::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains` | ? `simulator-gcp/cloudrun.go:781::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/authorizeddomains` | ? `simulator-gcp/cloudrun.go:782::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/authorizeddomains` | ? `simulator-gcp/cloudrun.go:783::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:790::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:830::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:847::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:857::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:900::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}` | ✓ `simulator-gcp/cloudrun.go:923::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:383::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:412::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:424::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:447::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:494::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:519::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:554::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:584::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:596::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:619::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:671::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:193::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:242::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:277::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:333::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:355::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:384::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:402::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:427::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:445::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:479::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:497::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:533::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:105::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:148::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:203::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}` | ✓ `simulator-gcp/cloudruninstances.go:223::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:78::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}` | ✓ `simulator-gcp/cloudrunjobs.go:1002::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks` | ✓ `simulator-gcp/cloudrunjobs.go:1018::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:620::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:688::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:721::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:746::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrunjobs.go:783::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:833::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions` | ✓ `simulator-gcp/cloudrunjobs.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}` | ✓ `simulator-gcp/cloudrunjobs.go:878::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:923::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:978::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}` | ? `simulator-gcp/cloudrunservices.go:1019::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}/{path...}` | ? `simulator-gcp/cloudrunservices.go:1020::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:630::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:676::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:699::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:722::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:752::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:830::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions` | ✓ `simulator-gcp/cloudrunservices.go:843::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:863::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services/{serviceAction}` | ✓ `simulator-gcp/cloudrunservices.go:883::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/operations/{operation}` | ✓ `simulator-gcp/cloudrunservices.go:903::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/operations/{opAction}` | ? `simulator-gcp/cloudrunservices.go:949::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/operations/{opAction}` | ? `simulator-gcp/cloudrunservices.go:953::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:155::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:177::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:198::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:252::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:274::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions` | ✓ `simulator-gcp/cloudrunworkerpools.go:287::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:307::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:327::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:353::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:363::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->

## Discovery-revision mismatches

`GoogleCloudRunV2WorkerPoolScaling` is modelled with four members —
`scalingMode`, `minInstanceCount`, `maxInstanceCount` and
`manualInstanceCount` — while the pinned Cloud Run v2 Discovery document
(revision 20260807) and the published REST reference both declare only
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
