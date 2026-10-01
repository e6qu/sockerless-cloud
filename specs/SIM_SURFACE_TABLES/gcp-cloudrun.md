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
| `POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:439::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:489::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:502::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:516::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:567::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:616::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:617::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:618::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:619::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:652::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:653::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:654::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:655::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:656::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:657::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:680::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:681::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:682::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:683::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:759::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:760::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:761::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:762::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:763::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:764::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:765::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:766::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:773::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:774::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/authorizeddomains` | ○ `simulator-gcp/cloudrun.go:775::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:782::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:822::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:839::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:892::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}` | ✓ `simulator-gcp/cloudrun.go:915::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{projectsId}/locations/{locationsId}/services/{servicesId}/revisions/{revisionsId}/{operationId}` | ✓ `simulator-gcp/cloudrun_export.go:61::exportStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{projectsId}/locations/{locationsId}/jobs/{jobsId}/executions/{executionsId}/{operationId}` | ✓ `simulator-gcp/cloudrun_export.go:62::exportStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}` | ✓ `simulator-gcp/cloudrun_export.go:67::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/{resourceId}` | ○ `simulator-gcp/cloudrun_export.go:75::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v2/projects/{project}/locations/{location}` | ✓ `simulator-gcp/cloudrun_export.go:89::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:178::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:227::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:239::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:262::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:318::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:340::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:369::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:387::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:412::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:430::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:464::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:482::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:518::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:105::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:148::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:203::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}` | ✓ `simulator-gcp/cloudruninstances.go:223::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:78::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:1008::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:1063::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}` | ✓ `simulator-gcp/cloudrunjobs.go:1087::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks` | ✓ `simulator-gcp/cloudrunjobs.go:1103::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:706::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:774::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:832::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrunjobs.go:869::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:918::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions` | ✓ `simulator-gcp/cloudrunjobs.go:934::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}` | ✓ `simulator-gcp/cloudrunjobs.go:963::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:661::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:707::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services` | ✓ `simulator-gcp/cloudrunservices.go:734::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:757::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/services/{service}` | ✓ `simulator-gcp/cloudrunservices.go:787::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:865::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/services/{service}/revisions` | ✓ `simulator-gcp/cloudrunservices.go:878::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunservices.go:898::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/services/{serviceAction}` | ✓ `simulator-gcp/cloudrunservices.go:918::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/operations/{operation}` | ✓ `simulator-gcp/cloudrunservices.go:941::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:989::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:993::operationVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
