# simulator-aws Terraform tests

Integration tests that run the `hashicorp/aws` provider against the AWS
simulator: `terraform init`, `apply`, assertions on the state and outputs, a
`plan` that expects no changes where a stack checks it, and `destroy`. The root
package applies `main.tf`; each subdirectory is a stack of its own, and CI
assigns every package to one `tf (aws …)` shard in `.github/workflows/ci.yml`.

## Running

From the root of a clone, run one stack, directly and through the HTTPS
gateway:

<!-- doctest: run -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud
make simulator-aws/terraform-test TERRAFORM_TEST_PACKAGES=./tutorial
```

<!-- doctest: run -->
```sh
make simulator-aws/terraform-https-test TERRAFORM_TEST_PACKAGES=./tutorial
```

Without `TERRAFORM_TEST_PACKAGES`, each target runs every package; CI runs
one package per `tf (aws …)` job.

The targets build the simulator once and pass it to every package through
`SOCKERLESS_AWS_SIMULATOR_BINARY`; `go test ./...` in this directory builds it
itself. Each package starts its own simulator on a free port, through
`helpers_test.go` in the root package and `internal/tfsim` in the stacks.

You need `terraform` on `PATH`, a container engine, and for the HTTPS target
`caddy` on `PATH` (or `CADDY=/path/to/caddy`).

## The HTTPS gateway

With `SOCKERLESS_TF_HTTPS_GATEWAY=1`, which the HTTPS target sets, each package
starts Caddy with its own state, trusts Caddy's root through `SSL_CERT_FILE`,
and points the provider at `https://localhost:<port>`, the Caddyfile's
single-simulator route, so no wildcard `.localhost` resolution is needed. On
macOS the target runs inside the Linux test image, where the provider honours
`SSL_CERT_FILE`.
