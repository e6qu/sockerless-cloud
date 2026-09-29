# Simulator Execution Model

How simulator workloads may execute. This is a guardrail for implementation
work, not a public cloud API surface.

## Container and FaaS contract

Container and FaaS workloads in the AWS, Google Cloud and Azure simulators run
as real containers on the Docker or Podman engine the simulator started
against, never as host processes.

The framework module `sim` provides:

- `StartContainerSync` to start a workload container and stream its output
  into the cloud's log sink, and `AdoptContainer` / `StartExistingContainer`
  for recovery (see [SIMULATOR_RECOVERY.md](SIMULATOR_RECOVERY.md)).
- `ContainerConfig.Architecture`, which every workload caller sets from the
  cloud resource that declares it.
- `InitDocker`, which returns an error when no engine answers, and
  `RequireContainerRuntime`, which refuses workload execution on an API-only
  (`SIM_RUNTIME=process`) process.

A workload without a usable image or runtime path is an implementation gap,
not a reason to synthesize success.

### Host dispatch invariant

Production simulator handlers do not import `os/exec` or call `exec.Command`
for user workloads. Each cloud's SDK suite carries `host_dispatch_test.go`,
which walks the simulator's production source and fails on any host process
dispatch outside its allow list.

The allow list is narrow, and each entry names its reason:

- The container reaper in `sim/` removes the simulator's own containers
  through the docker CLI; it runs no workload.
- Cloud Build and Azure Container Registry Tasks run the docker CLI for their
  build steps, because those services are build services and the steps are
  docker invocations.
- The Azure simulator's `metadata.go` asks the Podman machine for its routing
  to find the host callback address; it runs no workload.
- VM-level real execution launches Firecracker and programs Linux networking
  through the real-execution substrate in
  [SIMULATOR_REAL_EXECUTION.md](SIMULATOR_REAL_EXECUTION.md).

Test harnesses run the real CLIs (`aws`, `gcloud`, `az`, `terraform`,
`docker`) through `os/exec`; that is the client side, not the simulator.

## VM and network real execution

VM-level compute and networking differ from container and FaaS workloads.
Amazon EC2, Compute Engine and Azure virtual machine instances boot real
guests, and VPC, load balancer, security and NAT resources shape a real packet
path, through:

- Firecracker microVMs for VM instances;
- Linux network namespaces, bridges, tap and veth devices, and
  netlink-programmed routes for the network fabric;
- `nftables` for security group, firewall, NSG, NAT, DNAT and SNAT behaviour;
- real L4/L7 proxies with active health checks for load balancers.

This does not permit running VM payloads as host processes, and it does not
permit metadata-only success.

## Failure semantics

A missing real-execution dependency fails loudly:

- no Firecracker binary or jailer;
- no `/dev/kvm`;
- no permission to create network namespaces, bridges or tap devices;
- no permission to install `nftables` rules;
- no way to materialize the required load-balancer data-plane endpoint.

The simulator answers with the public cloud's error shape for the affected
request where the cloud API has one, and never with a successful resource
carrying fabricated state.

## Out of scope for this contract

- Changing public cloud API paths, headers or response shapes.
- Adding simulator-specific request fields, headers or environment variables
  to cloud API surfaces.
