# simulator-azure SDK tests

Integration tests for the Azure simulator through the official Azure SDK for Go (`github.com/Azure/azure-sdk-for-go/sdk`). `TestMain`
builds the simulator into `.build/sdk-tests/`, starts it on a free port, and
stops it when the suite ends; each test drives the real client against it.

One file covers one service family. The suite is a separate Go module with a
relative `replace` onto the simulator; it is never installed.

## Running

From a clone, run one service family with `-run`:

<!-- doctest: run expect="(?m)^ok\\s+github\\.com/e6qu/sockerless-cloud/simulator-azure/sdk-tests\\s" -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud/simulator-azure/sdk-tests
go test -run '^TestKeyVault' ./...
```

Without `-run`, `go test ./...` runs every test. CI runs the suite through
`make -C simulator-azure sdk-test`, split by `-run` patterns across the
`sim (azure sdk …)` jobs in `.github/workflows/ci.yml`. The network security
group and NAT gateway tests between neighbouring machines run there with
virtual machines placed on microVMs. A container engine (Docker or Podman) must
be reachable, because workloads run as real containers.

## Client configuration

Clients take a cloud configuration whose Azure Resource Manager and Microsoft Entra endpoints are the simulator, and credentials the simulator's Microsoft Entra slice issues — the configuration a client of a sovereign Azure cloud takes, with different coordinates. Microsoft's Cosmos DB emulator runs once for the suite, started from `TestMain` when `-run` selects a Cosmos DB differential test. No test branches on running against the simulator.
