# simulator-aws-terraform-tests

Integration tests that run `terraform apply` and `terraform destroy` against the AWS simulator. Verifies that the simulator implements enough of the AWS API surface for the Terraform AWS provider to provision and tear down resources.

## Running

```sh
cd simulator-aws/terraform-tests
go test -v ./...
```

To run the same provider flow through the optional Caddy HTTPS gateway:

```sh
cd simulator-aws
make terraform-https-test
```

The HTTPS target uses Caddy's `https://localhost:<ephemeral-port>` single-simulator route so the test does not depend on wildcard `.localhost` DNS support. It still uses Caddy TLS and passes the generated root CA to Terraform through `SSL_CERT_FILE`. On macOS the Make target runs the same test inside the shared Linux simulator test image so the real provider honors that CA file.

The root test harness (`helpers_test.go`) and the subpackage harness (`internal/tfsim`) handle simulator binary selection, port allocation, server startup, Terraform init/apply/destroy, and shutdown. No external cloud services are required.

## Prerequisites

- Go 1.23+
- `terraform` CLI installed and on `PATH`
- The `simulator-aws/` parent module. `make terraform-test` and `make terraform-https-test` build it once and pass the real binary to every Terraform package.
- `caddy` installed and on `PATH` for `make terraform-https-test`

## How it works

1. The Make target builds the AWS simulator binary once and passes it to every Terraform package through `SOCKERLESS_AWS_SIMULATOR_BINARY`
2. Each package starts an isolated simulator process on a free port
3. `terraform init` downloads the AWS provider
4. `terraform apply -auto-approve` provisions resources against the simulator
5. Test assertions verify the Terraform state
6. `terraform destroy -auto-approve` tears down resources

When `SOCKERLESS_TF_HTTPS_GATEWAY=1`, both the root package and the RDS/ElastiCache subpackages start Caddy with isolated state, trust its generated root CA through `SSL_CERT_FILE`, and point Terraform at `https://localhost:<ephemeral-port>`.
