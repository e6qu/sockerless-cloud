# PLAN

The simulators serve their declared surfaces: every vendored AWS model is
implemented or exempt, and every Google Cloud Discovery method and Azure
Swagger operation reaches a route that serves it or answers a declared 501
naming what is missing. `STATUS.md` is the snapshot, `DO_NEXT.md` the work
items, `WHAT_WE_DID.md` the decisions behind the code and `BUGS.md` the open
bugs.

## Execution modes

A simulator runs in one of three modes. The modes differ only in where
workloads run; the cloud APIs, the stores and the consoles are the same.

- **Embedded.** One binary per cloud, talking to a Docker or Podman engine as
  an ordinary user. Everything that runs runs as a container on an engine
  network: the workloads, the managed engines (RDS, ElastiCache, Cloud SQL,
  Memorystore, Azure Database, Azure Cache) and the sidecars that stand in
  for a cloud's resolver, metadata and NAT addresses. Amazon EC2, Compute
  Engine and Azure virtual machine instances are containers. This is the
  default, and the mode CI and developer machines run.
- **Embedded with node agents.** The same binary and the same engine
  networks, plus one or more node agents that boot Firecracker microVMs. A
  node agent is a container on the engine network like any other member; it
  is the only thing that holds `/dev/kvm` and the tap devices its guests use.
  The binary starts an agent as a container with the engine granting
  `/dev/kvm` when a service is configured for microVMs; an operator opts in
  through configuration and nothing else changes.
- **Distributed.** The control plane and the node agents as separate services
  on separate hosts, over the same backend interface the embedded modes use,
  with networks spanning hosts. This is the eventual target; the interfaces
  below are shaped for it and nothing is built for it yet.

`SIM_RUNTIME=process` stays the API-only mode: it places no workload at all.

**The simulator process holds no host privilege, in any mode.** No capability,
no device, no privileged port, no network namespace, no `nftables` rule, no
helper container that holds a capability on the simulator's behalf. The
container engine is the only privileged thing the simulator talks to, as an
ordinary client. Whatever needs more lives in a node agent the operator chose
to deploy.

## Placement: backends per service

Where a workload runs is a property of the service, chosen in the simulator's
own configuration, never through a cloud API, a header or an environment
variable a workload sees.

- **Backend interface.** `sim` defines one cloud-neutral interface for
  placing a workload — start, stop, adopt, exec, logs, publish, attach to a
  network — with two implementations: the **container backend**, which talks
  to the engine directly, and the **microVM backend**, which talks to a node
  agent. Each cloud's service maps its resources onto the interface and keeps
  its wire shapes, states and errors in its own module. In distributed mode
  both implementations talk to agents on other hosts.
- **Service classes.**
  1. *Compute services* always offer both backends: anything that runs user
     code — Amazon EC2, ECS, AWS Lambda, AWS Batch, App Runner, Compute
     Engine, Cloud Run, Cloud Functions, Azure Virtual Machines, Container
     Apps, Container Instances, App Service, Azure Functions. The microVM
     form follows the cloud's own: a Lambda invocation or a Fargate task in
     its own microVM, an EC2 instance as a full guest.
  2. *Managed engines* offer both where the cloud runs a real server process:
     Amazon RDS and Aurora, ElastiCache, Cloud SQL, Memorystore, Azure
     Database and Azure Cache run the engine in a container by default, or
     inside a microVM when configured.
  3. *Control-plane services* — Amazon S3, SQS, DNS, IAM and the like — run
     inside the simulator process and place no workload.
- **Configuration** is a table of services, each naming its backend;
  containers are every service's default. A service whose cloud distinguishes
  placements by its own attributes — an ECS task's launch type, an RDS
  instance's class — may refine the choice on that attribute, so the mapping
  is faithful to the cloud's vocabulary and still the simulator's own.
- **Images for VM instances on containers.** An AMI, a Compute Engine image
  and an Azure marketplace image name an OCI image. Each simulator publishes
  its own catalog — Amazon Linux, Ubuntu and Debian mapped to pinned images
  from the registries `scripts/base-images-for.sh` admits — and
  `RegisterImage`, `ImportImage`, `images.insert` and the Azure image
  resources take an OCI reference. User data runs under a small init the
  simulator supplies; on a node agent the same AMI boots a root filesystem
  built from that image.

## Networks without privilege

- **A VPC, a Google Cloud network or an Azure virtual network is an engine
  network** whose address range is the cloud resource's own CIDR; a subnet is
  a range inside it; every workload, container or microVM, is a member whose
  engine-assigned address is the one its elastic network interface, NIC or
  instance reports, asked of the engine at attach. One network model replaces
  the two the simulators had (engine networks for some workloads, host
  namespaces for others). An engine refuses two live networks over one
  address range, so two live VPCs sharing a CIDR do not coexist on one
  engine: a recorded gap, lifted where agents on other hosts carry them.
- **The cloud's fixed addresses are sidecars.** Each network carries a second,
  link-local subnet, and one sidecar per network sits at the cloud's resolver
  and metadata addresses — `169.254.169.253` and `169.254.169.254` for AWS,
  `169.254.169.254` and `metadata.google.internal` for Google Cloud,
  `168.63.129.16` and `169.254.169.254` for Azure — forwarding DNS to the
  simulator's resolver and metadata requests to its handlers. A workload
  reaches them because it shares the bridge. The resolver attributes a query
  to the network its sidecar serves, never to anything inside the message.
  Should an engine refuse a link-local subnet, the sidecar takes an ordinary
  address, `--dns` names it, and the metadata URL the cloud already hands a
  workload by environment (Amazon ECS does) carries it; a fixed metadata
  address the cloud offers no variable for is then a recorded gap.
- **NAT and egress.** A sidecar proxy per network replaces `nftables` SNAT;
  the NAT gateway's, Cloud NAT's or Azure NAT Gateway's public address is the
  sidecar's.
- **Peering and private endpoints** are a container on both networks.
- **Load balancers** keep `realexec/lbplane`: loopback listeners, TLS
  termination, probes and forwarding need no privilege.
- **Security groups, NSGs and firewalls** are enforced on every flow the
  simulator mediates — load balancers, published ports, NAT egress — and not
  between two containers on one network, because filtering their traffic
  needs a capability. That gap is a `BUGS.md` row, closed on node agents,
  where the agent filters at the guest's tap.

## Node agents

A node agent runs microVMs for the services configured onto it. It is a
container on the engine network, started by the binary with the engine
granting `/dev/kvm`, and inside its own namespace it owns the tap devices its
guests use and the bridge that joins them to its network interface, so a guest
is a network member with an address like any container. It boots Firecracker
guests from root filesystems built from the same OCI images the container
backend runs, serves the microVM backend's interface over its API, filters at
each tap what the guest's security group allows, and reports guest lifecycle
and logs. The `firecracker (microVM arithmetic)` CI job proves the agent, not
the host. In distributed mode the same agent runs on another host.

## Stages

1. **Remove the privileged machinery.** Delete the host-namespace fabric,
   the `nftables` rules, the realized-network DNAT and the in-process
   Firecracker launch, with their host tests, Makefile targets and CI job,
   the capability probes, and the `realexec_host` build tag; delete the two
   helper containers that held privilege on the simulator's behalf — the
   `CAP_NET_ADMIN` container that plumbed an Amazon ECS task's elastic
   network interface address and the `--privileged` container that attached
   a managed EBS volume's image to a loop device. The VPC engine network
   takes the VPC's CIDR and an awsvpc task or VPC Lambda invocation takes
   its interface address from the engine, which is what the helper did. A
   managed EBS volume on the container backend is an engine volume. The
   privilege a cloud lets a workload ask for — a container definition's
   `privileged`, a CodeBuild project's `privilegedMode`, an ACR task step's
   `privileged` — is not granted on the container backend: the workload runs
   without it, and what then fails is a `BUGS.md` row a node agent's guest
   closes, like the volume's size bound. `realexec` keeps `lbplane`, IPAM and the
   rule parsers. Amazon EC2, Compute Engine and Azure virtual machine
   instances refuse to launch until stage 3, with the cloud's provisioning
   error. One pull request, so the removal reviews as one change.
2. **Unprivileged networks.** The per-network sidecars for resolver and
   metadata addresses (closing BUG-3473 and retiring the per-record engine
   aliases), the NAT sidecar, peering, and security groups on mediated flows
   with the container-to-container gap filed.
3. **Placement interface and the container backend for VM instances.** The
   `sim` backend interface with the container implementation behind every
   compute service and managed engine, the per-service configuration table,
   the image catalogs, and EC2, Compute Engine and Azure virtual machine
   instances as containers with user data, metadata and lifecycle through the
   three client surfaces.
4. **Node agents and the microVM backend.** The agent image, its API, guest
   boot from OCI-built root filesystems, tap filtering, the microVM backend
   in `sim`, and each compute service and managed engine configurable onto
   it; the Firecracker CI job runs against the agent.
5. **Distributed.** Agents on other hosts and networks that span them, once
   the first four stages hold.

## Shared functionality in cloud-neutral packages

Each mechanism the three simulators implement alike lives in a cloud-neutral
package under `sim/` (or `realexec/` for the load-balancer data plane); each
cloud's wire shapes, error envelopes and naming stay in its own module and
reach the shared code through hooks. Landed: workload host and launch helpers
(`sim/workloadhost`, `sim/workload`, `sim/bg`, `sim/archive`); identity
(`sim/simjwt`, `sim/oidcfed`, registry authentication); the database engine
(`sim/dbengine` with `pgwire` and `mysqlwire`); the load-balancer data plane
(`realexec/lbplane`); object storage (`sim.PrefixStore`, `sim/blobstore`,
`sim/sparse`, `Server.Payloads`, `sim.KeyedLocks`); list queries and
key-value primitives (`sim/listq`, `sim/kvstore`); messaging, delivery, cron
and streams (`sim/msgq`, `sim/delivery`, `sim/cron`, `sim/streamlog`); keys
and logs (`sim/simkms`, `sim/logstore`); DNS (`sim/dnsserver`); and startup
(`sim.Main`, `sim.Program`). The placement backends of stage 3 join them.

## Maintenance

Keep the vendored specifications in sync with upstream and serve what a
re-vendor adds, hold the measured floors, and close the open bugs in
`BUGS.md` as their blockers lift.
