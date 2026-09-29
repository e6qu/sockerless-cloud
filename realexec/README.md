# realexec

The **real-execution substrate** for the cloud simulators: a Go library (no
binary) that gives the AWS, Google Cloud and Azure simulators real Linux
networking and microVM primitives where a simulated resource has to *behave*,
not just *serialize*. Amazon EC2 instances with NAT, VPC routing,
security-group filtering, load-balancer data planes — anything where a real
client expects packets to flow — run through this module.

It exists because of the project's no-fakes rule: a `google_compute_router_nat`
or an `aws_lb` either forwards real traffic or reports that the host cannot.
The contract it implements is
[`specs/SIMULATOR_REAL_EXECUTION.md`](../specs/SIMULATOR_REAL_EXECUTION.md).

## What it provides

| File | Surface | Purpose |
|---|---|---|
| `capabilities.go` | `DetectNetworkCapabilities()`, `DetectFirecrackerCapabilities()`, `CapabilityReport.Require()` → `*ErrMissingCapability` | Probes the host: Linux, the `ip` and `nft` binaries, `CAP_NET_ADMIN`/`CAP_SYS_ADMIN`, and KVM plus `firecracker`/`jailer` for microVMs. The typed error is the contract callers branch on. |
| `network.go` | `Host`, `NetworkSpec`, attach and detach, `PacketRule`, bridge ingress filters, staged ingress filters, SNAT | Network namespaces, bridges and veth pairs with nftables packet rules — the VPC, VNet and subnet substrate. |
| `ipam.go` | `IPAM`, `NewIPAMWithReserved`, `HostReservation` | Deterministic per-CIDR address allocation that keeps each cloud's reserved addresses out of reach: Amazon VPC and Azure Virtual Network the first four and the last, Google Cloud VPC the first two and the last two. `SubnetSpec.Reserved` carries the reservation into a subnet. |
| `rules.go` | `PortRange`, `ExpandRules`, `PrioritizedRule`, `FlattenByPriority` | Translates a security group, firewall rule or network security group into `PacketRule`s: parses port specifications (rejecting what the cloud would), crosses sources with ports, and orders prioritized rules for first-match evaluation (a deny before an allow of equal priority). |
| `fabric/` | `fabric.Fabric[K]`, `LinuxName`, `DeriveMAC`, `FirstHostGateway`, `ReconcileLiveness` | One cloud's realized fabric: networks, subnets, namespace and tap interfaces and Firecracker machines keyed by resource ID, with per-network teardown locks and per-machine start locks; ingress filters for both interface kinds; source NAT and reserved public addresses owned by the resource that programmed them and released with it. |
| `public_ip.go` | `ReserveAWSPublicIPv4`, `ReserveGCPPublicIPv4`, `ReserveAzurePublicIPv4` | Per-cloud public IPv4 pools, stable and collision-free across a run. |
| `lbplane/forward.go`, `lbplane/upgrade.go` | `Forward`, `Upstream`, `SendError`, `ErrClientWentAway`, `IsUpgradeRequest`, `TunnelUpgradedResponse`, `Hostname` | The HTTP forward every load-balancer and ingress data plane shares: never follows a target's redirect, tunnels a 101 upgrade, drops hop-by-hop headers, keeps the escaped path, and tells a client that left from a target that failed. |
| `lbplane/probe.go` | `ProbeHTTP`, `ProbeTCP`, `ParseStatusMatcher`, `StatusRange` | Health probes graded by the cloud's own matcher (`"200,202-299"`), Host header and body match; a probe never follows a redirect. |
| `lbplane/health.go` | `HealthTracker[K]`, `HealthPolicy`, `SweepEvery` | Background health checking on each check's own interval and thresholds; data planes route from the recorded verdicts (ELBv2 target health, Compute Engine backend health, Azure Load Balancer and Application Gateway probes). |
| `lbplane/tcpproxy.go`, `lbplane/tls.go`, `lbplane/hosts.go` | `StartTCPProxy`, `StartTLSProxy`, `StartHTTPSServer`, `ListenerTLSConfig`, `ListenerCertificate`, `HostLeases` | Layer-4 and TLS-terminating listeners with SNI certificate selection, and the stable per-load-balancer loopback host every listener of one load balancer binds. |
| `firecracker.go`, `guestexec.go` | `FirecrackerVMConfig`, `StartFirecrackerVM`, `GuestExecCapabilities` | Boots Firecracker microVMs (KVM-gated) over the default virtio-MMIO transport, downloading pinned assets on first use, and runs commands inside a guest. |
| `capture.go`, `mirror.go`, `packetfilter.go`, `pcap.go` | `Capture`, `Mirror`, `CaptureFilter`, `PcapWriter` | Packet capture and traffic mirroring on the substrate's interfaces, written as pcap — the data plane behind Google Cloud Packet Mirroring and Azure Network Watcher packet captures. |
| `mark.go` | `WithMark`, `MarkFrom` | Phase marks carried on the context, so concurrent starts sharing one network each report their own timings. |
| `cleanup.go` | `CleanupStack` | LIFO teardown, so a failed setup never leaks namespaces, veths, nft tables or VMs. |
| `runner.go` | `Runner` | Privileged-command execution with uniform error wrapping. |

## How it is used

Each simulator requires the module as
`github.com/e6qu/sockerless-cloud/realexec`, pinned by pseudo-version like the
other support modules; during development the repository-root `go.work`
resolves it to this directory. There is no `replace` directive, so
`go install` of a simulator works. A change here lands in two pushes — push,
then pin the pushed commit in every requiring module — as
[`AGENTS.md`](../AGENTS.md) describes, and
`scripts/check-support-module-pins.sh` fails when a pin lags the tree.

The consumers are `simulator-aws` (`ec2_realexec.go`, `elbv2_dataplane.go`,
the NAT and security-group paths), `simulator-gcp` (`compute_realexec.go`,
`compute_loadbalancing.go`) and `simulator-azure` (compute and network).

The usage contract has two sides:

- **Handlers** call `realexec.DetectNetworkCapabilities().Require()` before
  touching the substrate. On a host that cannot do real execution the
  simulator answers **503 "missing real-execution host capabilities"** — a
  refusal, never a fake (HTTP 500 stays reserved for panics).
- **Tests** that need the host carry the `realexec_host` build tag and gate on
  the same probe: a missing command fails the test, a capability the kernel
  cannot provide skips it — the one skip [`AGENTS.md`](../AGENTS.md) allows.
  macOS has no network namespaces.

## Host requirements

Linux with `ip` (iproute2) and `nft` (nftables) on `PATH`, and
`CAP_NET_ADMIN` + `CAP_SYS_ADMIN` (in effect root or equivalent file
capabilities). Firecracker paths also need `/dev/kvm` and the `firecracker`
and `jailer` binaries. `DetectNetworkCapabilities` does not require KVM, so
plain networking works on KVM-less CI hosts.

## Build and test

The standard library Makefile per
[`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md):

```bash
make -C realexec build   # compile check (library, no binary)
make -C realexec test    # unit tests; the host-capability tests run on Linux
make -C realexec lint
```

`host_linux_test.go` carries the tests that need a real Linux host. The
repository-root `make realexec-network-test` and `make firecracker-test`
exercise the substrate end to end.

## See also

- [`simulator-aws/README.md`](../simulator-aws/README.md), [`simulator-gcp/README.md`](../simulator-gcp/README.md), [`simulator-azure/README.md`](../simulator-azure/README.md) — the consumers.
- [`specs/SIMULATOR_EXECUTION.md`](../specs/SIMULATOR_EXECUTION.md) — where the substrate sits in the execution model.
