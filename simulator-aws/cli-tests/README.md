# simulator-aws CLI tests

Integration tests for the AWS simulator through the AWS CLI (`aws`). `TestMain`
builds the simulator into `.build/cli-tests/`, starts it on a free port, and
stops it when the suite ends; each test shells out to the real CLI through
`runCLI`, which returns the command's standard output and reports both streams
when the command fails.

`TestMain` installs the current AWS CLI v2 release into a temporary directory when `aws` is not on `PATH`.

## Running

```sh
cd simulator-aws/cli-tests
go test ./...
```

CI runs the suite through `make -C simulator-aws cli-test`, sharded by the
jobs in `.github/workflows/ci.yml`.

## CLI configuration

Each command runs with `AWS_ENDPOINT_URL` pointing at the simulator, static test credentials and a region — the environment a real AWS CLI user sets, with the simulator as the endpoint.
