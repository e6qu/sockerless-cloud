# Sim surface — aws-amplify

Surface registered in `simulator-aws/amplify.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /apps` | ✓ `simulator-aws/amplify.go:463::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps` | ✓ `simulator-aws/amplify.go:464::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}` | ✓ `simulator-aws/amplify.go:465::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}` | ✓ `simulator-aws/amplify.go:466::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}` | ✓ `simulator-aws/amplify.go:467::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/branches` | ✓ `simulator-aws/amplify.go:469::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/branches` | ✓ `simulator-aws/amplify.go:470::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/branches/{name}` | ✓ `simulator-aws/amplify.go:471::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/branches/{name}` | ✓ `simulator-aws/amplify.go:472::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}/branches/{name}` | ✓ `simulator-aws/amplify.go:473::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/webhooks` | ✓ `simulator-aws/amplify.go:475::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/webhooks` | ✓ `simulator-aws/amplify.go:476::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /webhooks/{webhookId}` | ✓ `simulator-aws/amplify.go:477::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /webhooks/{webhookId}` | ✓ `simulator-aws/amplify.go:478::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /webhooks/{webhookId}` | ✓ `simulator-aws/amplify.go:479::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/branches/{name}/jobs` | ✓ `simulator-aws/amplify.go:481::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/branches/{name}/jobs` | ✓ `simulator-aws/amplify.go:482::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/branches/{name}/jobs/{jobId}` | ✓ `simulator-aws/amplify.go:483::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}/branches/{name}/jobs/{jobId}/stop` | ✓ `simulator-aws/amplify.go:484::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}/branches/{name}/jobs/{jobId}` | ✓ `simulator-aws/amplify.go:485::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/branches/{name}/jobs/{jobId}/artifacts` | ✓ `simulator-aws/amplify.go:486::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /artifacts/{artifactId}` | ✓ `simulator-aws/amplify.go:487::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/accesslogs` | ✓ `simulator-aws/amplify.go:488::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/branches/{name}/deployments` | ✓ `simulator-aws/amplify.go:492::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/branches/{name}/deployments/start` | ✓ `simulator-aws/amplify.go:493::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tags/{arn...}` | ✓ `simulator-aws/amplify.go:495::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /tags/{arn...}` | ✓ `simulator-aws/amplify.go:496::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /tags/{arn...}` | ✓ `simulator-aws/amplify.go:497::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/domains` | ✓ `simulator-aws/amplify_domains.go:119::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/domains` | ✓ `simulator-aws/amplify_domains.go:120::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/domains/{domainName}` | ✓ `simulator-aws/amplify_domains.go:121::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/domains/{domainName}` | ✓ `simulator-aws/amplify_domains.go:122::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}/domains/{domainName}` | ✓ `simulator-aws/amplify_domains.go:123::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apps/{appId}/backendenvironments` | ✓ `simulator-aws/amplify_domains.go:125::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/backendenvironments` | ✓ `simulator-aws/amplify_domains.go:126::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apps/{appId}/backendenvironments/{environmentName}` | ✓ `simulator-aws/amplify_domains.go:127::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apps/{appId}/backendenvironments/{environmentName}` | ✓ `simulator-aws/amplify_domains.go:128::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Beyond the registered control-plane ops, Amplify execution is real:

- **Builds** (`amplify_build.go`): StartJob with a build-shaped job type (RELEASE/RETRY/WEB_HOOK) requires a clonable HTTP(S) Git repository, uses the app's encrypted connected-repository credential, resolves a branch/app build specification or the checked-in `amplify.yml`, and executes backend, frontend, and test pre/build/post phases inside the managed multi-language image. Monorepo `applications`/`appRoot`/`buildPath`, build-spec environment values with app/branch precedence, persistent declared cache paths, and artifact collection all drive the real job. Numeric job IDs, real PENDING/RUNNING step transitions, new RETRY executions with copied commit metadata, and container/artifact-derived terminal states match the service lifecycle. Hosted build ZIPs are linked from the BUILD step; declared end-to-end test files alone appear in ListArtifacts/GetArtifactUrl, while the aggregate test bundle and configuration URL live on the BUILD step and all objects are removed with the job. Unsupported repositories and missing or invalid build specifications fail before job creation; manual deployments accept and publish the uploaded ZIP through the real CreateDeployment/StartDeployment flow.
- **Hosting data plane** (`amplify_dataplane.go`, host-addressed WrapHandler — not a mux route): serves each branch's active deployment (latest SUCCEED job's artifact zip / fileMap) on `{branch}.{appId}.amplifyapp.com`, the deterministic per-app `{hash}.cloudfront.net` host the subdomain dnsRecords advertise (no CloudFront control-plane object — real Amplify's distribution is internal), and verified custom domains. Custom rules (200/301/302/404/404-200, `<*>` wildcards), basicAuthCredentials (base64 `user:pass`), and an associated AWS WAF WebACL's default action or IP-set rule are enforced; real blocked and allowed requests populate GetSampledRequests. No deployment ⇒ 404.
- **SSR / WEB_COMPUTE** (`amplify_compute.go`): bundles whose root carries `deploy-manifest.json` (Amplify Hosting deployment spec, version 1) route Static targets from the bundle's `static/` directory and proxy Compute targets to a long-lived node container per branch active-deployment (entrypoint under `compute/{name}/`, PORT=3000, lazily started on first request, replaced on new deploys, stopped on branch/app delete). ImageOptimization targets are 501 (the sim has no image-optimization service).
- **Domain verification** (`amplify_domains.go`): AMPLIFY_MANAGED associations start PENDING_VERIFICATION and flip AVAILABLE (subdomains Verified) only when the advertised certificate-verification CNAME exists in a sim Route 53 hosted zone covering the domain — evaluated at read time, so terraform's `wait_for_verification` polling converges and a domain with no hosted zone stays PENDING_VERIFICATION. CUSTOM-certificate associations settle immediately (no DNS challenge to wait on).

Tests: unit (buildSpec/manifest/rule/host-matcher/verification tables in `simulator-aws/amplify_*_test.go`), SDK e2e (`sdk-tests/amplify_hosting_test.go` and `sdk-tests/amplify_test.go` — hosting, SSR, private authenticated Git, Python and Node.js monorepo phases, cache restoration, hosted and end-to-end test artifacts, retry, AWS WAF blocking/sampling, and Route 53 verification), CLI hosting/build/artifact/WAF flows (`cli-tests/amplify_test.go`), Terraform zone+verification-record+WAF-association graph (`terraform-tests/main.tf`), and authenticated Chromium app/branch lifecycle (`ui/e2e/shauth-rps.mjs`). Build/SSR/hosting e2e need Docker (they ride the always-Docker sdk-tests suite); the unit suite stays Docker-free.
<!-- HAND-WRITTEN END -->
