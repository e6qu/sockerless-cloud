# Node Agents and MicroVMs

The implementation contract for the one place a simulator runs anything that
needs more than an ordinary container engine client: a **node agent**, which
boots Firecracker microVMs for the services configured onto it. Simulator
public APIs match the public cloud; the agent is an implementation detail
behind the compute services and managed engines, and adds no simulator-only
knobs to them. [SIMULATOR_EXECUTION.md](SIMULATOR_EXECUTION.md) places it in
the execution model.

## Scope

A node agent serves the **microVM backend** of the placement interface for:

1. **VM instances** — Amazon EC2 instances, Compute Engine instances and Azure
   virtual machines, each a full guest.
2. **Container and function workloads configured for microVMs** — an Amazon
   ECS task, an AWS Lambda invocation, a Cloud Run instance, a Container App
   replica, in its own guest, as the cloud's own isolation does.
3. **Managed engines configured for microVMs** — an Amazon RDS, Cloud SQL or
   Azure Database engine process inside a guest.

Everything not configured onto an agent runs on the container backend. The
agent never runs a host process on the simulator's behalf.

## Where the agent runs

- In **embedded with node agents**, the binary starts an agent as a container
  on the engine network when configuration names a service for microVMs. The
  engine grants the container `/dev/kvm`; the binary holds nothing. Without
  `/dev/kvm` or the Firecracker binaries the agent refuses to start, and the
  services configured onto it fail their placements with the cloud's
  provisioning error.
- In **distributed**, the same agent image runs on another host and the
  control plane reaches it over its API.

The agent is a container like any other network member: it has an
engine-assigned address on each network it serves, and inside its own
namespace it owns the tap devices its guests use and the bridge that joins
them to its interface, so a guest is a network member with an address like
any container. Nothing it does reaches outside its namespace.

## Agent API

The agent serves the backend interface over HTTP on its network address:
create, start, stop, adopt and delete a guest; attach it to a network with a
leased address; exec in it; stream its console and logs; report its state.
Requests are authenticated with a credential the simulator mints for the
agent at start; a workload cannot reach the agent's API, which listens only
on the address the control plane uses.

## Guests

- A guest boots a pinned Firecracker release over the virtio-MMIO transport
  from a root filesystem the agent builds from the same OCI image the
  container backend would run — an AMI, a Compute Engine image, an Azure
  marketplace image, an ECS container image, a Lambda runtime image or an
  engine image — so a service's workload is the same artifact on either
  backend.
- A guest's disk outlives its process: a stopped machine can be generalized
  and captured, a deallocated one keeps its disk, a deleted one discards it.
- Provisioning states reflect real progress: `pending` / `PROVISIONING` /
  `Creating` while the guest is prepared, `running` / `RUNNING` /
  `PowerState/running` once it is alive, attached and answering on its
  address, stopped or deallocated when its process is stopped, terminated or
  deleted when its process, tap and lease are removed.
- The guest reaches its cloud's resolver and metadata at the cloud's
  addresses through the same per-network sidecars every container uses.

## Security enforcement

At each guest's tap the agent filters what the guest's security group,
firewall or network security group allows: direction, protocol, port ranges,
CIDR sources and destinations, group and tag references where the cloud
supports them, Google Cloud and Azure priority ordering, and stateful return
traffic where the cloud is stateful. A rule update reprograms the tap. This is
the one place in the simulators where packet filtering exists; the container
backend enforces the same rules on the flows it mediates and records the rest
as a gap.

## Test contract

Each public API path a node agent serves ships with official SDK coverage,
vendor CLI coverage where the CLI exposes the path, and Terraform coverage
where the provider exposes the resource, run against an agent. The tests prove
behaviour, not metadata: a real guest lifecycle, per-instance metadata,
reachable and blocked paths, and the same workload answering on either
backend. Tests that need an agent skip only for what a run cannot be given —
a kernel other than Linux or no `/dev/kvm` — the one skip `AGENTS.md`
allows; a missing command fails the test, naming what to install.

`make firecracker-test` is the agent's own proof: it starts an agent on a host
with `/dev/kvm`, boots a guest from an OCI-built root filesystem, runs the
repository's `testdata/eval-arithmetic` inside it, and checks that teardown
leaves no process, tap or disk behind. The job that runs it is the only CI job
that needs KVM.
