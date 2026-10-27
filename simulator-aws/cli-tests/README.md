# simulator-aws CLI tests

Integration tests for the AWS simulator through the AWS CLI (`aws`). `TestMain`
builds the simulator into `.build/cli-tests/`, starts it on a free port, and
stops it when the suite ends; each test shells out to the real CLI through
`runCLI`, which returns the command's standard output and reports both streams
when the command fails.

`TestMain` installs the current AWS CLI v2 release into a temporary directory when `aws` is not on `PATH`.

## Running

From a clone, run one service family with `-run`:

<!-- doctest: run expect="(?m)^ok\\s+github\\.com/e6qu/sockerless-cloud/simulator-aws/cli-tests\\s" -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud/simulator-aws/cli-tests
go test -run '^TestKMSCLI' ./...
```

Without `-run`, `go test ./...` runs every test. CI runs the suite through
`make -C simulator-aws cli-test`, split by `-run` patterns across the
`sim (aws cli …)` jobs in `.github/workflows/ci.yml`.

## CLI configuration

Each command runs with `AWS_ENDPOINT_URL` pointing at the simulator, static test credentials and a region — the environment a real AWS CLI user sets, with the simulator as the endpoint.
