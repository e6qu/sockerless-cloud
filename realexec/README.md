# simulator-realexec

Shared **real-execution substrate** for the cloud simulators: a Go library
(no binary) that gives the AWS / GCP / Azure sims actual Linux networking
and microVM primitives when a simulated resource has to *behave*, not just
*serialize*. EC2 instances with NAT, VPC routing, security-group filtering,
load-balancer data planes — anything where a real client expects packets to
flow — runs through this module instead of being faked with metadata.

It exists because of the project's no-fakes rule: a `google_compute_router_nat`
or an `aws_lb` either forwards real traffic or honestly reports that the host
can't do it. There is no synthetic middle ground.

## What it provides

| File | Surface | Purpose |
|---|---|---|
| `capabilities.go` | `DetectNetworkCapabilities()`, `DetectFirecrackerCapabilities()`, `CapabilityReport.Require()` → `*ErrMissingCapability` | Probes the host: Linux, `ip`/`nft` binaries, `CAP_NET_ADMIN`/`CAP_SYS_ADMIN` (and KVM + `firecracker`/`jailer` for microVMs). The typed error is the contract callers branch on. |
| `network.go` | `Host`, `NetworkSpec`, attach/detach, `PacketRule`, bridge ingress filters | Real network namespaces, bridges, and veth pairs with nftables packet rules — the VPC/VNet/subnet substrate. |
| `ipam.go` | `IPAM` | Deterministic per-CIDR address allocation (skips network/broadcast addresses). |
| `public_ip.go` | `ReserveAWSPublicIPv4`, `ReserveGCPPublicIPv4`, `ReserveAzurePublicIPv4` | Per-cloud "public" IPv4 pools so simulated elastic/external IPs are stable and collision-free across a run. |
| `loadbalancer.go` | `ProbeTarget`, `StartTCPProxy` | Health probes + a real TCP proxy — the data plane behind simulated ELB/NLB, GCP load balancing, and Azure LB front ends. |
| `firecracker.go` | `FirecrackerVMConfig`, launch/teardown | Boots actual Firecracker microVMs (KVM-gated) for instance shapes where a process or container isn't faithful enough. Downloads pinned assets (default `v1.15.1`) on first use. |
| `cleanup.go` | `CleanupStack` | LIFO teardown so a failed mid-flight setup never leaks namespaces, veths, nft tables, or VMs. |
| `runner.go` | `Runner` | Privileged-command execution with uniform error wrapping (`commandError`). |

## How it's used

Each simulator vendors the module by path (it is **not** published):

```
// simulator-{aws,gcp,azure}/go.mod
require github.com/sockerless/simulator-realexec v0.0.0
replace github.com/sockerless/simulator-realexec => ../realexec
```

Consumers: `simulator-aws` (`ec2_realexec.go`, `elbv2_dataplane.go`, NAT
tests), `simulator-gcp` (`compute_realexec.go`, `compute_loadbalancing.go`),
`simulator-azure` (compute/network). Simulator Docker builds `COPY
realexec/` alongside the sim source so the replace directive
resolves in-image.

The usage contract has two sides:

- **Handlers** call `realexec.DetectNetworkCapabilities().Require()` before
  touching the substrate. On a host that can't do real execution the
  simulator returns **503 "missing real-execution host capabilities"** —
  an honest refusal, never a silent fake (HTTP 500 stays reserved for
  panics).
- **Tests** gate on the same probe and `t.Skip` when it errors. That is why
  the GCP/Azure compute+network suites and the AWS NAT/ELBv2 data-plane
  tests run for real on the Linux CI runners (ubuntu + sudo + iproute2 +
  nftables, the `tf (gcp)`/`tf (azure)` jobs) and skip on macOS, where
  network namespaces don't exist.

## Host requirements

Linux with `ip` (iproute2) and `nft` (nftables) in `PATH`, and
`CAP_NET_ADMIN` + `CAP_SYS_ADMIN` (effectively: root or equivalent file
caps). Firecracker paths additionally need `/dev/kvm` plus the
`firecracker` and `jailer` binaries. `DetectNetworkCapabilities`
deliberately does **not** require KVM — plain networking must work on
KVM-less CI hosts.

## Build & test

Standard library Makefile per
[`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md):

```bash
make -C realexec build   # compile (library only, no binary)
make -C realexec test    # unit tests; Linux-only paths skip elsewhere
make -C realexec lint    # go vet + gofmt
```

`host_linux_test.go` carries the tests that need a real Linux host; like the
simulator suites, they skip when the capability probe fails.

## See also

- [`simulator-aws/README.md`](../simulator-aws/README.md), [`simulator-gcp/README.md`](../simulator-gcp/README.md), [`simulator-azure/README.md`](../simulator-azure/README.md) — the consumers.
- `specs/CLOUD_RESOURCE_MAPPING.md` — which cloud resources map onto which substrate primitive.
- `docs/RUNNERS.md` — runner hurdles where real execution (or its absence on a host) shaped the design.
