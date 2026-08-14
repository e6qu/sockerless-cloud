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
| `POST /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:402::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:452::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:465::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:483::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:530::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:582::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:583::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations/{name}` | ✓ `simulator-gcp/cloudrun.go:584::getConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/configurations` | ✓ `simulator-gcp/cloudrun.go:585::listConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:623::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:624::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:625::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:626::getRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/revisions` | ✓ `simulator-gcp/cloudrun.go:627::listRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}` | ✓ `simulator-gcp/cloudrun.go:628::deleteRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:656::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:657::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes/{name}` | ✓ `simulator-gcp/cloudrun.go:658::getRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/routes` | ✓ `simulator-gcp/cloudrun.go:659::listRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:738::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:739::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:740::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:741::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:742::createDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:743::getDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/domainmappings` | ✓ `simulator-gcp/cloudrun.go:744::listDomainMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}` | ✓ `simulator-gcp/cloudrun.go:745::deleteDomainMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:754::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:755::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/authorizeddomains` | ✓ `simulator-gcp/cloudrun.go:756::listAuthorizedDomains` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:763::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:803::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{namespace}/services` | ✓ `simulator-gcp/cloudrun.go:820::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:833::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{namespace}/services/{name}` | ✓ `simulator-gcp/cloudrun.go:872::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}` | ✓ `simulator-gcp/cloudrun.go:895::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:351::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:379::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:391::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:414::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:456::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:481::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:517::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:546::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:558::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:581::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}` | ✓ `simulator-gcp/cloudrun_v1_instances_workerpools.go:628::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:194::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:242::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:277::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:328::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:350::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:381::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:399::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:424::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:442::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:478::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:496::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrun_v1_jobs.go:533::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:70::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:96::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/instances` | ✓ `simulator-gcp/cloudruninstances.go:118::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:139::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/instances/{instance}` | ✓ `simulator-gcp/cloudruninstances.go:188::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}` | ✓ `simulator-gcp/cloudruninstances.go:205::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:580::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:645::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/cloudrunjobs.go:678::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:703::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/cloudrunjobs.go:737::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:784::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions` | ✓ `simulator-gcp/cloudrunjobs.go:800::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}` | ✓ `simulator-gcp/cloudrunjobs.go:826::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/cloudrunjobs.go:844::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}` | ✓ `simulator-gcp/cloudrunjobs.go:895::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}` | ✓ `simulator-gcp/cloudrunjobs.go:916::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks` | ✓ `simulator-gcp/cloudrunjobs.go:932::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
| `POST /v2/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:920::waitOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/operations/{opAction}` | ✓ `simulator-gcp/cloudrunservices.go:924::waitOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}` | ✓ `simulator-gcp/cloudrunservices.go:990::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2-services-invoke/{project}/{location}/{service}/{path...}` | ✓ `simulator-gcp/cloudrunservices.go:991::invokeService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:154::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudrunworkerpools.go:176::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:197::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:245::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:265::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions` | ✓ `simulator-gcp/cloudrunworkerpools.go:278::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}` | ✓ `simulator-gcp/cloudrunworkerpools.go:298::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:316::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}` | ✓ `simulator-gcp/cloudrunworkerpools.go:343::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}` | ✓ `simulator-gcp/cloudrunworkerpools.go:353::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

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
