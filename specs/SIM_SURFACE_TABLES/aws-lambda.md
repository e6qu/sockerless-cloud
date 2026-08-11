# Sim surface — aws-lambda

Surface registered in `simulator-aws/lambda.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /2015-03-31/functions` | ✓ `simulator-aws/lambda.go:368::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}` | ✓ `simulator-aws/lambda.go:369::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}` | ✓ `simulator-aws/lambda.go:370::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/configuration` | ✓ `simulator-aws/lambda.go:371::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/invocations` | ✓ `simulator-aws/lambda.go:372::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions` | ✓ `simulator-aws/lambda.go:373::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/` | ✓ `simulator-aws/lambda.go:374::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:375::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:376::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:377::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/versions` | ✓ `simulator-aws/lambda.go:380::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/versions` | ✓ `simulator-aws/lambda.go:381::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/aliases` | ✓ `simulator-aws/lambda.go:382::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/aliases` | ✓ `simulator-aws/lambda.go:383::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:384::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:385::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:386::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/policy` | ✓ `simulator-aws/lambda.go:387::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/policy` | ✓ `simulator-aws/lambda.go:388::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}/policy/{statement}` | ✓ `simulator-aws/lambda.go:389::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:390::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:391::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:392::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:393::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-10-31/functions/{name}/urls` | ✓ `simulator-aws/lambda.go:394::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/event-source-mappings` | ✓ `simulator-aws/lambda.go:397::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/event-source-mappings` | ✓ `simulator-aws/lambda.go:398::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:399::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:400::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:401::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2018-10-31/layers/{layer}/versions` | ✓ `simulator-aws/lambda.go:406::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions` | ✓ `simulator-aws/lambda.go:407::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions/{version}` | ✓ `simulator-aws/lambda.go:408::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2018-10-31/layers/{layer}/versions/{version}` | ✓ `simulator-aws/lambda.go:409::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers` | ✓ `simulator-aws/lambda.go:414::cloudTrailRecordedRESTDynamic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2017-10-31/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:417::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-30/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:418::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2017-10-31/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:419::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:32::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:33::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:34::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:35::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-25/functions/{name}/event-invoke-config/list` | ✓ `simulator-aws/lambda_extras2.go:36::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:39::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:40::cloudTrailRecordedRESTDynamic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:41::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-04-22/code-signing-configs` | ✓ `simulator-aws/lambda_extras2.go:53::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs` | ✓ `simulator-aws/lambda_extras2.go:54::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:55::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:56::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:57::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:60::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:61::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:62::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-07-20/functions/{name}/runtime-management-config` | ✓ `simulator-aws/lambda_extras2.go:65::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2021-07-20/functions/{name}/runtime-management-config` | ✓ `simulator-aws/lambda_extras2.go:66::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2016-08-19/account-settings` | ✓ `simulator-aws/lambda_extras2.go:69::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2016-08-19/account-settings/` | ✓ `simulator-aws/lambda_extras2.go:70::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2024-08-31/functions/{name}/recursion-config` | ✓ `simulator-aws/lambda_extras2.go:73::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2024-08-31/functions/{name}/recursion-config` | ✓ `simulator-aws/lambda_extras2.go:74::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2018-10-31/layers/{layer}/versions/{version}/policy` | ✓ `simulator-aws/lambda_extras2.go:77::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions/{version}/policy` | ✓ `simulator-aws/lambda_extras2.go:78::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2018-10-31/layers/{layer}/versions/{version}/policy/{statement}` | ✓ `simulator-aws/lambda_extras2.go:79::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/configuration` | ✓ `simulator-aws/lambda_extras3.go:48::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/code` | ✓ `simulator-aws/lambda_extras3.go:49::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/functions/{name}/function-scaling-config` | ✓ `simulator-aws/lambda_extras3.go:52::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2025-11-30/functions/{name}/function-scaling-config` | ✓ `simulator-aws/lambda_extras3.go:53::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs/{arn}/functions` | ✓ `simulator-aws/lambda_extras3.go:58::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-11-30/capacity-providers` | ✓ `simulator-aws/lambda_extras3.go:61::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers` | ✓ `simulator-aws/lambda_extras3.go:62::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:63::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:64::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:65::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers/{cpname}/function-versions` | ✓ `simulator-aws/lambda_extras3.go:66::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2014-11-13/functions/{name}/invoke-async` | ✓ `simulator-aws/lambda_extras3.go:69::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2021-11-15/functions/{name}/response-streaming-invocations` | ✓ `simulator-aws/lambda_extras3.go:70::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/functions/{name}/durable-executions` | ✓ `simulator-aws/lambda_extras3.go:76::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}` | ✓ `simulator-aws/lambda_extras3.go:77::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-executions/{arn}/checkpoint` | ✓ `simulator-aws/lambda_extras3.go:78::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}/history` | ✓ `simulator-aws/lambda_extras3.go:79::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}/state` | ✓ `simulator-aws/lambda_extras3.go:80::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-executions/{arn}/stop` | ✓ `simulator-aws/lambda_extras3.go:81::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/succeed` | ✓ `simulator-aws/lambda_extras3.go:82::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/fail` | ✓ `simulator-aws/lambda_extras3.go:83::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/heartbeat` | ✓ `simulator-aws/lambda_extras3.go:84::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
