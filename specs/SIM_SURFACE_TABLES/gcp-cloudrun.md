# Sim surface — gcp-cloudrun

Surface registered in `simulator-gcp/cloudrun.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:443::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:476::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:489::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:503::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:536::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:585::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:586::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:587::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:588::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:621::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:622::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:623::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:624::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:625::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:626::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:649::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:650::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:651::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:652::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:728::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:729::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:730::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:731::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:732::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:733::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:734::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:735::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:742::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:743::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:744::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:751::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:778::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:795::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:805::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:834::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}` | ✓ `simulator-gcp/cloudrun.go:857::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{projectsId}/locations/{locationsId}/services/{servicesId}/revisions/{revisionsId}/{operationId}` | ✓ `simulator-gcp/cloudrun_export.go:61::exportStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{projectsId}/locations/{locationsId}/jobs/{jobsId}/executions/{executionsId}/{operationId}` | ✓ `simulator-gcp/cloudrun_export.go:62::exportStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}` | ✓ `simulator-gcp/cloudrun_export.go:67::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/{resourceId}` | ○ `simulator-gcp/cloudrun_export.go:75::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v2/projects/{project}/locations/{location}` | ✓ `simulator-gcp/cloudrun_export.go:89::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:382::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:413::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:425::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:439::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:483::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:507::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:529::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:561::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:573::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:587::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:628::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:183::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:230::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:242::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:255::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:309::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:360::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:378::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:391::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:409::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:443::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:461::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:493::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:105::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:110::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:118::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:167::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}` | ✓ `simulator-gcp/cloudruninstances.go:190::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:77::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:1027::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}` | ✓ `simulator-gcp/cloudrunjobs.go:1047::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks` | ✓ `simulator-gcp/cloudrunjobs.go:1063::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:716::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:774::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:815::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrunjobs.go:838::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:887::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions` | ✓ `simulator-gcp/cloudrunjobs.go:903::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}` | ✓ `simulator-gcp/cloudrunjobs.go:932::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:976::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:1037::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions` | ✓ `simulator-gcp/cloudrunservices.go:1050::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:1057::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services/{serviceAction}` | ✓ `simulator-gcp/cloudrunservices.go:1067::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:1130::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:1134::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:893::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:930::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:957::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:965::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:989::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:159::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:188::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:193::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:201::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:245::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:270::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions` | ✓ `simulator-gcp/cloudrunworkerpools.go:283::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:290::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:300::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:326::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:336::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

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
