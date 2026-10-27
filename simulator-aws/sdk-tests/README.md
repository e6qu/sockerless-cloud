# simulator-aws SDK tests

Integration tests for the AWS simulator through the official AWS SDK for Go v2 (`github.com/aws/aws-sdk-go-v2`). `TestMain`
builds the simulator into `.build/sdk-tests/`, starts it on a free port, and
stops it when the suite ends; each test drives the real client against it.

One file covers one service family. The suite is a separate Go module with a
relative `replace` onto the simulator; it is never installed.

## Running

From a clone, run one service family with `-run`:

<!-- doctest: run expect="(?m)^ok\\s+github\\.com/e6qu/sockerless-cloud/simulator-aws/sdk-tests\\s" -->
```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud/simulator-aws/sdk-tests
go test -run '^TestKMS' ./...
```

Without `-run`, `go test ./...` runs every test. CI runs the suite through
`make -C simulator-aws sdk-test`, split by `-run` patterns across the
`sim (aws sdk …)` jobs in `.github/workflows/ci.yml`, each within a ten-minute
deadline. The tests of security groups and NAT gateways between neighbouring
instances run there with Amazon EC2 placed on microVMs
(`SIM_PLACEMENT=ec2=microvm`). A container engine (Docker or Podman) must be
reachable, because workloads run as real containers.

## Client configuration

Clients take the simulator as their endpoint and static test credentials, the same configuration a client of real AWS takes with a different endpoint. Hosts the SDK derives from a request — S3 Express One Zone and access-point hosts — resolve to the simulator rather than being overridden, so the client builds exactly the URL and signature it builds against AWS. No test branches on running against the simulator.
