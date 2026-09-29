# simulator-gcp SDK tests

Integration tests for the Google Cloud simulator through the official Google Cloud client libraries for Go (`cloud.google.com/go`, `google.golang.org/api`). `TestMain`
builds the simulator into `.build/sdk-tests/`, starts it on a free port, and
stops it when the suite ends; each test drives the real client against it.

One file covers one service family. The suite is a separate Go module with a
relative `replace` onto the simulator; it is never installed.

## Running

```sh
cd simulator-gcp/sdk-tests
go test ./...
```

CI runs the suite through `make -C simulator-gcp sdk-test`, sharded by the
jobs in `.github/workflows/ci.yml`. A container engine (Docker or Podman) must
be reachable, because workloads run as real containers.

## Client configuration

Clients take the simulator as their endpoint, over REST or gRPC as the library defaults, with credentials the simulator issues — the configuration a client of real Google Cloud takes with different coordinates. No test branches on running against the simulator.
