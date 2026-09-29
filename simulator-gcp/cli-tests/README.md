# simulator-gcp CLI tests

Integration tests for the Google Cloud simulator through the Google Cloud CLI (`gcloud`) and `cbt`. `TestMain`
builds the simulator into `.build/cli-tests/`, starts it on a free port, and
stops it when the suite ends; each test shells out to the real CLI through
`runCLI`, which returns the command's standard output and reports both streams
when the command fails.

`TestMain` installs gcloud when it is not on `PATH` and builds `cbt`, retrying each download before believing a failure.

## Running

```sh
cd simulator-gcp/cli-tests
go test ./...
```

CI runs the suite through `make -C simulator-gcp cli-test`, sharded by the
jobs in `.github/workflows/ci.yml`.

## CLI configuration

Each command runs with an isolated `CLOUDSDK_CONFIG`, an access token the simulator issued, and `CLOUDSDK_API_ENDPOINT_OVERRIDES_*` pointing each API at the simulator.
