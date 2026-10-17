# Simulator Execution Model

How simulator workloads execute. This is a guardrail for implementation work,
not a public cloud API surface.

## Modes

A simulator runs in one of three modes, which differ only in where workloads
run. The cloud APIs, the stores and the consoles are the same in each.

| Mode | What runs where | Host privilege |
|---|---|---|
| **Embedded** | One binary per cloud; every workload, managed engine and sidecar is a container on an engine network. VM instances are containers. | None. |
| **Embedded with node agents** | The same, plus node agents the binary starts as containers; an agent boots Firecracker microVMs for the services configured onto it. | None for the binary; the engine grants `/dev/kvm` to the agent. |
| **Distributed** | The control plane and the node agents as separate services on separate hosts, over the same backend interface. | None for the control plane. |

`SIM_RUNTIME=process` is the API-only mode: it serves the control planes and
places no workload.

## The no-privilege rule

The simulator process holds no host privilege in any mode: no capability, no
device, no privileged port, no network namespace of its own making, no
`nftables` rule, and no helper container that holds a capability or
`--privileged` on its behalf. The container engine is the only privileged thing the simulator talks
to, and it talks to it as an ordinary client. Whatever needs more lives in a
node agent an operator chose to deploy, and a node agent runs only in the two
modes that name it.

A workload container holds no capability either: no `CAP_NET_ADMIN`, no
`--privileged`, no engine socket, no host network and no host mount. Each
cloud product's sandbox profile applies to its containers.

## Placement

Where a workload runs is a property of the service, chosen in the simulator's
own configuration — never through a cloud API, a header, or an environment
variable a workload sees.

- `sim` defines one cloud-neutral **backend interface** for placing a
  workload: start, stop, adopt, exec, logs, published ports, network
  attachment. The **container backend** implements it against the engine; the
  **microVM backend** implements it against a node agent. A cloud's service
  maps its resources onto the interface and keeps its wire shapes, states and
  errors in its own module.
- **Compute services** — every service that runs user code — offer both
  backends, containers by default. **Managed engines** offer both where the
  cloud runs a real server process. **Control-plane services** place no
  workload.
- Configuration is a table of services, each naming its backend. A service
  may refine the choice on an attribute its cloud already distinguishes
  placements by (an Amazon ECS launch type, an Amazon RDS instance class).
- An AMI, a Compute Engine image and an Azure marketplace image name an OCI
  image. Each simulator publishes its own catalog, mapped to pinned images
  from the registries `scripts/base-images-for.sh` admits, and the image
  registration and import operations take an OCI reference.

## Container contract

Workloads on the container backend run as real containers on the Docker or
Podman engine the simulator started against, never as host processes.

The framework module `sim` provides:

- `StartContainerSyncContext` and `StartHTTPContainer` to start a workload
  container and stream its output into the cloud's log sink, and
  `AdoptContainer` / `StartExistingContainer` for recovery (see
  [SIMULATOR_RECOVERY.md](SIMULATOR_RECOVERY.md)).
- `ContainerConfig.Architecture`, which every workload caller sets from the
  cloud resource that declares it.
- `InitDocker`, which returns an error when no engine answers, and
  `RequireContainerRuntime`, which refuses workload execution on an API-only
  process.

A managed block volume — an Amazon EBS volume, a Compute Engine persistent
disk, an Azure managed disk — is an engine volume on the container backend;
only a node agent's guest sees it as a block device of its declared size.

A workload without a usable image or runtime path is an implementation gap,
not a reason to synthesize success.

### Host dispatch invariant

Production simulator handlers do not import `os/exec` or call `exec.Command`
for user workloads. Each cloud's SDK suite carries `host_dispatch_test.go`,
which walks the simulator's production source and fails on any host process
dispatch outside its allow list. The allow list is narrow, and each entry
names its reason:

- The container reaper in `sim/` removes the simulator's own containers
  through the docker CLI; it runs no workload.
- The build services run the docker CLI for their build steps
  (`sim/workload/build.go`, Cloud Build's `cloudbuild.go`, Azure Container
  Registry Tasks' `acr_tasks.go`), because those steps are docker invocations.
- `sim/workloadhost` asks the Podman machine for its default route to find the
  address workloads reach the host at; it runs no workload.

Test harnesses run the real CLIs (`aws`, `gcloud`, `az`, `terraform`,
`docker`) through `os/exec`; that is the client side, not the simulator.

## Networks

A VPC, a Google Cloud network or an Azure virtual network is an engine
network; a subnet is an address range inside it; every workload, container or
microVM, is a member with an engine-assigned address.

- The cloud's fixed addresses are **sidecars**: each network carries a
  link-local subnet, and one sidecar per network sits at the cloud's resolver
  and metadata addresses, forwarding DNS to the simulator's resolver and
  metadata requests to its handlers. The resolver attributes a query to the
  network its sidecar serves, never to anything inside the message.
- NAT gateways, Cloud NAT and Azure NAT Gateway are a sidecar proxy per
  network, whose address is the gateway's public address.
- Peering and private endpoints are a container on both networks.
- Load balancers are loopback listeners, TLS termination, probes and
  forwarding in `realexec/lbplane`.
- Security groups, network security groups and firewalls are enforced on every
  flow the simulator mediates — load balancers, published ports, NAT egress.
  Traffic between two containers on one network is not filtered, because
  filtering it needs a capability; that gap is a `BUGS.md` row, closed on
  node agents. A rule stored only in JSON and enforced nowhere is not
  implemented.

## Node agents

Node agents and microVMs are the subject of
[SIMULATOR_REAL_EXECUTION.md](SIMULATOR_REAL_EXECUTION.md).

## Failure semantics

A workload the configured backend cannot place fails loudly, with the public
cloud's error shape for the affected request where the cloud API has one, and
never with a successful resource carrying fabricated state: a service
configured for microVMs with no node agent reachable, an image the catalog
does not map, an engine that refuses a network.

## Out of scope for this contract

- Changing public cloud API paths, headers or response shapes.
- Adding simulator-specific request fields, headers or environment variables
  to cloud API surfaces.
