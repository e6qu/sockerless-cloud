# simulator-gcp SDK tests

Integration tests for the Google Cloud simulator through the official Google Cloud client libraries for Go (`cloud.google.com/go`, `google.golang.org/api`). `TestMain`
builds the simulator into `.build/sdk-tests/`, starts it on a free port, and
stops it when the suite ends; each test drives the real client against it.

One file covers one service family. The suite is a separate Go module with a
relative `replace` onto the simulator; it is never installed.

## Running

From a clone, run one service family with `-run`:

<!-- doctest: run expect="(?m)^ok\\s+github\\.com/e6qu/sockerless-cloud/simulator-gcp/sdk-tests\\s" -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud/simulator-gcp/sdk-tests
go test -run '^TestPubSub' ./...
```

Without `-run`, `go test ./...` runs every test. CI runs the suite through
`make -C simulator-gcp sdk-test`, split by `-run` patterns across the
`sim (gcp sdk …)` jobs in `.github/workflows/ci.yml`. The firewall and Cloud
NAT tests between neighbouring instances run there with Compute Engine placed
on microVMs (`SIM_PLACEMENT=compute=microvm`). A container engine (Docker or Podman) must
be reachable, because workloads run as real containers.

## Client configuration

Clients take the simulator as their endpoint, over REST or gRPC as the library defaults, with credentials the simulator issues — the configuration a client of real Google Cloud takes with different coordinates. No test branches on running against the simulator.
