# simulator-azure CLI tests

Integration tests for the Azure simulator through the Azure CLI (`az`). `TestMain`
builds the simulator into `.build/cli-tests/`, starts it on a free port, and
stops it when the suite ends; each test shells out to the real CLI through
`runCLI`, which returns the command's standard output and reports both streams
when the command fails.

The Azure CLI must be on `PATH`; `TestMain` fails naming it when it is not.

## Running

From a clone, run one family with `-run`:

<!-- doctest: run expect="(?m)^ok\\s+github\\.com/e6qu/sockerless-cloud/simulator-azure/cli-tests\\s" -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud/simulator-azure/cli-tests
go test -run '^TestPrivateDNS' ./...
```

Without `-run`, `go test ./...` runs every test. CI runs the suite through
`make -C simulator-azure cli-test`, split by `-run` patterns across the
`sim (azure cli …)` jobs in `.github/workflows/ci.yml`.

## CLI configuration

Each command runs with an isolated `AZURE_CONFIG_DIR`; a test addresses the simulator by URL (`az rest --url`) or through a registered cloud whose endpoints are the simulator.
