# Simulator Execution Model

This file describes how simulator workloads may execute. It is a guardrail for
implementation work, not a public cloud API surface.

## Current Container/FaaS Contract

Container and FaaS workloads in the AWS, GCP, and Azure simulators run through
real Docker/Podman containers, not as host processes.

The shared simulator module exposes:

- `StartContainer` / `StartContainerSync` for Docker-backed execution.
- `ContainerConfig.Architecture`, which every workload caller must set from the
  cloud-native resource shape.
- `InitDocker`, which fails loudly when Docker/Podman is unavailable.

The simulators must not silently fall back to host processes or in-memory
execution. A workload without a usable image/runtime path is an implementation
gap, not a reason to synthesize success.

### Host Dispatch Invariant

Production simulator handlers must not import `os/exec` or call `exec.Command`
for user workloads. Each cloud's SDK test suite includes
`host_dispatch_test.go` to enforce this for root simulator handlers.

Allowed process use is narrow:

- Test harnesses may run real CLIs (`aws`, `gcloud`, `az`, `terraform`,
  `docker`) through `os/exec`.
- Simulator infrastructure may invoke a required host tool only when the tool is
  part of the real implementation and the caller fails loudly if it is missing.
- GCP Cloud Build may run the Docker CLI for build-step execution because Cloud
  Build is itself a build service; this is an explicit allowlist entry.
- VM-level real execution may launch Firecracker and program Linux networking
  through the dedicated real-execution substrate described in
  [SIMULATOR_REAL_EXECUTION.md](SIMULATOR_REAL_EXECUTION.md).

No other simulator code may use host process execution.

## VM/Network Real-Execution Extension

The VM-level compute and networking surfaces are different from container/FaaS
workloads. EC2, GCE, and Azure VM instances boot real guests, and VPC, load
balancer, security, and NAT resources affect a real packet path.

That work was tracked by issues #332-#336 and is specified in
[SIMULATOR_REAL_EXECUTION.md](SIMULATOR_REAL_EXECUTION.md). The extension uses:

- Firecracker microVMs for VM instances.
- Linux network namespaces, bridges, tap/veth devices, and netlink-programmed
  routes for the cloud network fabric.
- `nftables` for security group, firewall, NSG, NAT, DNAT, and SNAT behavior.
- Real L4/L7 proxy processes or in-process listeners with active health checks
  for load balancers.

This is a narrow exception to the container/FaaS Docker-dispatch invariant. It
does not permit running VM user payloads as host processes, and it does not
permit metadata-only success.

## Failure Semantics

Real-execution dependencies are required when a resource has been migrated to
the real-execution substrate. Missing dependencies must fail loudly:

- no Firecracker binary or jailer,
- no `/dev/kvm`,
- no permission to create netns/bridges/tap devices,
- no permission to install `nftables` rules,
- no ability to materialize the required load-balancer data-plane endpoint.

The simulator must return the public cloud's error shape for the affected API
request where the cloud API has one. It must not return a successful resource
with fabricated state.

## Not In Scope For This Contract

- Changing public cloud API paths, headers, or response shapes.
- Adding simulator-specific request fields, headers, or environment variables to
  cloud API surfaces.
- Regressing issues #332-#336 back to metadata-only success after real guests,
  packet forwarding, enforcement, and health checks were implemented and covered
  by public clients.
