# Sockerless Cloud

Local, wire-faithful reimplementations of slices of AWS, Google Cloud and
Microsoft Azure: one Go binary per cloud. The clouds' own SDKs, CLIs and
Terraform providers talk to them unmodified; only the endpoint and the
credentials change. Behind the APIs the work is real:
- an EC2 instance boots;
- a Lambda function runs in its own execution environment;
- a Cloud SQL instance is a real PostgreSQL;
- a Cloud Run job runs one container per task;
- an Azure Functions app answers HTTP requests.

| Cloud | Simulator | Port | Validated against |
|---|---|---|---|
| AWS | [`simulator-aws`](simulator-aws/README.md) | 4566 | Smithy models, the AWS Service Reference, AWS guides |
| Google Cloud | [`simulator-gcp`](simulator-gcp/README.md) | 4567 (gRPC 4569) | API Discovery documents, the gRPC protocol definitions, Google Cloud documentation |
| Azure | [`simulator-azure`](simulator-azure/README.md) | 4568 | OpenAPI (Swagger) documents, Microsoft Learn pages |

## Install

Each release is one `vX.Y.Z` tag. The [GitHub Release](https://github.com/e6qu/sockerless-cloud/releases)
carries the binaries for linux and darwin, amd64 and arm64, each with its
console embedded, and multi-architecture container images:

<!-- x-release-please-start-version -->
<!-- doctest: background id=image ready="Listening on" -->
```sh
docker run --rm -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/e6qu/sockerless-simulator-aws:v3.2.0
```
<!-- x-release-please-end -->
<!-- doctest: stop id=image -->

The simulators are Go modules in subdirectories, which a `vX.Y.Z` tag can't
version. `go install` takes the newest commit with `@main`, or a release with
its release commit (`@<commit>`). Don't use `@latest`: it resolves to an old
bootstrap version the module proxy still serves.

<!-- doctest: run -->
```sh
go install github.com/e6qu/sockerless-cloud/simulator-aws@main
go install github.com/e6qu/sockerless-cloud/simulator-gcp@main
go install github.com/e6qu/sockerless-cloud/simulator-azure@main
export PATH="$(go env GOPATH)/bin:$PATH"
```

A simulator needs a Docker or Podman engine to run workloads.
`SIM_RUNTIME=process` serves the APIs alone.

## Quick start

<!-- doctest: background id=sims ready="Listening on" count=3 -->
```sh
simulator-aws & simulator-gcp & simulator-azure &
```

Each prints `Listening on` when its port answers. Then point a real client
at each one.

AWS takes the seeded `test` access key:

<!-- doctest: run expect="(?m) hello$" -->
```sh
export AWS_ENDPOINT_URL=http://localhost:4566 AWS_REGION=us-east-1 \
       AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
aws s3 mb s3://hello && aws s3 ls
```

Google Cloud takes the seeded bootstrap service account's key:

<!-- doctest: run expect="gs://hello/" -->
```sh
go run github.com/e6qu/sockerless-cloud/testutil/cmd/gcp-bootstrap-key@main -o bootstrap-key.json
gcloud auth activate-service-account --key-file=bootstrap-key.json
gcloud config set auth/token_host http://localhost:4567/token
gcloud config set api_endpoint_overrides/storage http://localhost:4567/
gcloud storage buckets create gs://hello --project=sockerless && gcloud storage ls --project=sockerless
```

Azure takes the seeded bootstrap application's client secret:

<!-- doctest: run expect="\"value\"" -->
```sh
AZ_TOKEN=$(curl -s -X POST http://localhost:4568/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token \
  -d grant_type=client_credentials -d client_id=test-client-id -d client_secret=test-client-secret \
  --data-urlencode scope=https://management.azure.com/.default | jq -r .access_token)
curl -s -H "Authorization: Bearer $AZ_TOKEN" \
  "http://localhost:4568/subscriptions/00000000-0000-0000-0000-000000000001/resourcegroups?api-version=2021-04-01"
```

[The tutorial](docs/tutorial.md) builds the same application stack on all
three with Terraform: a network with NAT, a virtual machine, a container
service, object storage, a database, a queue, a function and monitoring. CI
runs it, and this page's commands, as written.

## Documentation

| Read | For |
|---|---|
| [Tutorial](docs/tutorial.md) | One stack on three clouds, step by step |
| [Configuration](docs/configuration.md) | Every `SIM_*` variable, the consoles' sign-in, the local HTTPS gateway |
| [Identity and access](docs/iam.md) | How each simulator authenticates and authorizes requests: IAM, Google Cloud IAM, Microsoft Entra ID and Azure RBAC |
| [Instance metadata](docs/instance-metadata.md) | What workloads read from the EC2, Google Cloud and Azure metadata services |
| [Architecture](docs/architecture.md) | The modules, a request's path, state, workloads, networks, the shared framework |
| [Specifications](docs/specifications.md) | Where each cloud's API schemas come from, what is generated, how coverage is checked |
| [Maintaining](docs/maintaining.md) | Tests, hooks, CI, continuity files, pins, releases |
| [Contributing tutorial](docs/tutorial-contributing.md) | Adding an operation, from specification to merged pull request |
| [So, you want to build your own cloud](docs/so-you-want-to-build-a-cloud.md) | What a cloud is made of, and how this compares with LocalStack and Floci |
| [`sim/README.md`](sim/README.md) | Every package of the shared framework |
| [`BUGS.md`](BUGS.md), [`STATUS.md`](STATUS.md), [`PLAN.md`](PLAN.md) | Open bugs, current state, the roadmap |

## What it isn't

- **A production deployment.** The simulators run on one machine, for local
  development and CI. The point is that the same client code reaches both
  them and the cloud.
- **Multi-region.** Each simulator serves one home Region. A few services
  (Amazon RDS instances and their backup replication) honour the Region a
  request is signed for.
- **A copy of vendors' published data.** Price sheets and SKU catalogs belong
  to the vendor; a partial copy would be fabrication, so those surfaces answer
  with what this installation publishes, which is nothing.
- **An external identity provider.** Every credential is verified, but the
  identities are the ones each simulator's own IAM, Google Cloud IAM and
  Microsoft Entra ID slices issue.

## Licence

Copyright 2026 [Adrian Mârza](https://www.linkedin.com/in/adrian-m%C3%A2rza-52606512a),
and each contributor to the extent of their own contributions.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option) any
later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See
[`LICENSE`](LICENSE) and <https://www.gnu.org/licenses/agpl-3.0.html>
(`AGPL-3.0-or-later`).

The vendored specifications under [`specs/cloud-api/`](specs/cloud-api/README.md)
are third-party material under their own licences:

| Vendored | Upstream | Licence |
|---|---|---|
| [`specs/cloud-api/aws/`](specs/cloud-api/aws/SOURCES.md) | `aws/aws-sdk-go-v2` Smithy models; AWS guides; the Elastic Load Balancing hosted-zone table from `hashicorp/terraform-provider-aws`; the [AWS Service Reference](specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md) | Apache-2.0; CC-BY-SA-4.0; MPL-2.0; the Service Reference's public authorization data |
| [`specs/cloud-api/gcp/`](specs/cloud-api/gcp/SOURCES.md) | Google API Discovery documents; Google Cloud documentation | Apache-2.0; CC-BY-4.0 |
| [`specs/cloud-api/azure/`](specs/cloud-api/azure/SOURCES.md) | `Azure/azure-rest-api-specs`; Microsoft Learn sources; KEDA scaler docs | MIT; CC-BY-4.0; Apache-2.0 |

Each `SOURCES.md` row records the file's upstream path, licence, pin and fetch
time. The dependencies of the test suites and consoles carry their own
licences, recorded in their `go.mod` and `package.json` files.
