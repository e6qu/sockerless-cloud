# simulator-azure CLI tests

Integration tests for the Azure simulator through the Azure CLI (`az`). `TestMain`
builds the simulator into `.build/cli-tests/`, starts it on a free port, and
stops it when the suite ends; each test shells out to the real CLI through
`runCLI`, which returns the command's standard output and reports both streams
when the command fails.

The Azure CLI must be on `PATH`; `TestMain` fails naming it when it is not.

## Running

```sh
cd simulator-azure/cli-tests
go test ./...
```

CI runs the suite through `make -C simulator-azure cli-test`, sharded by the
jobs in `.github/workflows/ci.yml`.

## CLI configuration

Each command runs with an isolated `AZURE_CONFIG_DIR`; a test addresses the simulator by URL (`az rest --url`) or through a registered cloud whose endpoints are the simulator.
