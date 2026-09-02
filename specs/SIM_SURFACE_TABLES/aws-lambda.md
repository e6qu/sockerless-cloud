# Sim surface — aws-lambda

Surface registered in `simulator-aws/lambda.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /2015-03-31/functions` | ✓ `simulator-aws/lambda.go:368::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}` | ✓ `simulator-aws/lambda.go:369::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}` | ✓ `simulator-aws/lambda.go:370::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/configuration` | ✓ `simulator-aws/lambda.go:371::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/invocations` | ✓ `simulator-aws/lambda.go:372::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions` | ✓ `simulator-aws/lambda.go:373::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/` | ✓ `simulator-aws/lambda.go:374::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:375::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:376::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2017-03-31/tags/{arn...}` | ✓ `simulator-aws/lambda.go:377::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/versions` | ✓ `simulator-aws/lambda.go:380::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/versions` | ✓ `simulator-aws/lambda.go:381::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/aliases` | ✓ `simulator-aws/lambda.go:382::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/aliases` | ✓ `simulator-aws/lambda.go:383::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:384::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:385::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}/aliases/{alias}` | ✓ `simulator-aws/lambda.go:386::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/functions/{name}/policy` | ✓ `simulator-aws/lambda.go:387::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/policy` | ✓ `simulator-aws/lambda.go:388::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/functions/{name}/policy/{statement}` | ✓ `simulator-aws/lambda.go:389::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:390::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:391::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:392::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2021-10-31/functions/{name}/url` | ✓ `simulator-aws/lambda.go:393::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-10-31/functions/{name}/urls` | ✓ `simulator-aws/lambda.go:394::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-03-31/event-source-mappings` | ✓ `simulator-aws/lambda.go:397::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/event-source-mappings` | ✓ `simulator-aws/lambda.go:398::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:399::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:400::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-03-31/event-source-mappings/{uuid}` | ✓ `simulator-aws/lambda.go:401::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2018-10-31/layers/{layer}/versions` | ✓ `simulator-aws/lambda.go:406::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions` | ✓ `simulator-aws/lambda.go:407::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions/{version}` | ✓ `simulator-aws/lambda.go:408::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2018-10-31/layers/{layer}/versions/{version}` | ✓ `simulator-aws/lambda.go:409::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers` | ✓ `simulator-aws/lambda.go:414::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2017-10-31/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:417::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-30/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:418::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2017-10-31/functions/{name}/concurrency` | ✓ `simulator-aws/lambda.go:419::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:32::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:33::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:34::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2019-09-25/functions/{name}/event-invoke-config` | ✓ `simulator-aws/lambda_extras2.go:35::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-25/functions/{name}/event-invoke-config/list` | ✓ `simulator-aws/lambda_extras2.go:36::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:39::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:40::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2019-09-30/functions/{name}/provisioned-concurrency` | ✓ `simulator-aws/lambda_extras2.go:41::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-04-22/code-signing-configs` | ✓ `simulator-aws/lambda_extras2.go:53::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs` | ✓ `simulator-aws/lambda_extras2.go:54::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:55::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:56::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-04-22/code-signing-configs/{arn...}` | ✓ `simulator-aws/lambda_extras2.go:57::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:60::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:61::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-06-30/functions/{name}/code-signing-config` | ✓ `simulator-aws/lambda_extras2.go:62::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2021-07-20/functions/{name}/runtime-management-config` | ✓ `simulator-aws/lambda_extras2.go:65::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2021-07-20/functions/{name}/runtime-management-config` | ✓ `simulator-aws/lambda_extras2.go:66::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2016-08-19/account-settings` | ✓ `simulator-aws/lambda_extras2.go:69::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2016-08-19/account-settings/` | ✓ `simulator-aws/lambda_extras2.go:70::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2024-08-31/functions/{name}/recursion-config` | ✓ `simulator-aws/lambda_extras2.go:73::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2024-08-31/functions/{name}/recursion-config` | ✓ `simulator-aws/lambda_extras2.go:74::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2018-10-31/layers/{layer}/versions/{version}/policy` | ✓ `simulator-aws/lambda_extras2.go:77::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2018-10-31/layers/{layer}/versions/{version}/policy` | ✓ `simulator-aws/lambda_extras2.go:78::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2018-10-31/layers/{layer}/versions/{version}/policy/{statement}` | ✓ `simulator-aws/lambda_extras2.go:79::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-03-31/functions/{name}/configuration` | ✓ `simulator-aws/lambda_extras3.go:48::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-03-31/functions/{name}/code` | ✓ `simulator-aws/lambda_extras3.go:49::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/functions/{name}/function-scaling-config` | ✓ `simulator-aws/lambda_extras3.go:52::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2025-11-30/functions/{name}/function-scaling-config` | ✓ `simulator-aws/lambda_extras3.go:53::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-04-22/code-signing-configs/{arn}/functions` | ✓ `simulator-aws/lambda_extras3.go:58::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-11-30/capacity-providers` | ✓ `simulator-aws/lambda_extras3.go:61::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers` | ✓ `simulator-aws/lambda_extras3.go:62::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:63::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:64::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2025-11-30/capacity-providers/{cpname}` | ✓ `simulator-aws/lambda_extras3.go:65::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-11-30/capacity-providers/{cpname}/function-versions` | ✓ `simulator-aws/lambda_extras3.go:66::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2014-11-13/functions/{name}/invoke-async` | ✓ `simulator-aws/lambda_extras3.go:69::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2021-11-15/functions/{name}/response-streaming-invocations` | ✓ `simulator-aws/lambda_extras3.go:70::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/functions/{name}/durable-executions` | ✓ `simulator-aws/lambda_extras3.go:76::lambdaResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}` | ✓ `simulator-aws/lambda_extras3.go:77::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-executions/{arn}/checkpoint` | ✓ `simulator-aws/lambda_extras3.go:78::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}/history` | ✓ `simulator-aws/lambda_extras3.go:79::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2025-12-01/durable-executions/{arn}/state` | ✓ `simulator-aws/lambda_extras3.go:80::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-executions/{arn}/stop` | ✓ `simulator-aws/lambda_extras3.go:81::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/succeed` | ✓ `simulator-aws/lambda_extras3.go:82::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/fail` | ✓ `simulator-aws/lambda_extras3.go:83::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2025-12-01/durable-execution-callbacks/{cbid}/heartbeat` | ✓ `simulator-aws/lambda_extras3.go:84::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
