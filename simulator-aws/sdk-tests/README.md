# simulator-aws SDK tests

Integration tests for the AWS simulator through the official AWS SDK for Go v2 (`github.com/aws/aws-sdk-go-v2`). `TestMain`
builds the simulator into `.build/sdk-tests/`, starts it on a free port, and
stops it when the suite ends; each test drives the real client against it.

One file covers one service family. The suite is a separate Go module with a
relative `replace` onto the simulator; it is never installed.

## Running

```sh
cd simulator-aws/sdk-tests
go test ./...
```

CI runs the suite through `make -C simulator-aws sdk-test`, sharded by the
jobs in `.github/workflows/ci.yml`. A container engine (Docker or Podman) must
be reachable, because workloads run as real containers.

## Client configuration

Clients take the simulator as their endpoint and static test credentials, the same configuration a client of real AWS takes with a different endpoint. Hosts the SDK derives from a request — S3 Express One Zone and access-point hosts — resolve to the simulator rather than being overridden, so the client builds exactly the URL and signature it builds against AWS. No test branches on running against the simulator.
