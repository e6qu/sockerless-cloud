# Simulator Real-Execution Substrate

The implementation contract for VM and network resources that behave rather
than serialize. Simulator public APIs match the public cloud; the substrate is
an implementation detail behind the Amazon EC2, Compute Engine, Azure virtual
machine, VPC, firewall, NAT and load-balancer APIs, and adds no
simulator-only knobs to them. The substrate is the `realexec` module
([`realexec/README.md`](../realexec/README.md)).

## Scope

The substrate covers four resource families:

1. **VM instances** — Amazon EC2 instances, Compute Engine instances and Azure
   virtual machines.
2. **Network fabric** — VPCs and networks, subnets, route tables and routers,
   NICs and ENIs, public and elastic IPs, and NAT gateways, Cloud NAT and Azure
   NAT Gateway.
3. **Security policy** — AWS security groups, Google Cloud firewalls and Azure
   network security groups.
4. **Load balancing** — Elastic Load Balancing, Google Cloud forwarding-rule
   and backend-service load balancing, and Azure Load Balancer.

Container and FaaS surfaces stay on the container engine through
`sim.StartContainerSync`; Firecracker serves only VM-level APIs.

## Capability model

Every build carries the substrate, but a resource that needs it succeeds only
where the host has the capabilities:

- a Linux host;
- the Firecracker binary and jailer, and `/dev/kvm` usable by the simulator;
- permission to create network namespaces, bridges, tap and veth devices, and
  to assign addresses;
- permission to program routes through netlink;
- permission to install and remove `nftables` rules;
- permission to materialize load-balancer data-plane endpoints.

Capability checks are explicit and deterministic
(`realexec.DetectNetworkCapabilities`, `realexec.DetectFirecrackerCapabilities`);
they inspect the host and its binaries and create no permanent resource as a
probe. When a capability is missing, the handler fails with the cloud's error
shape for failed provisioning. There is no metadata fallback: a resource that
needs real execution either reaches the cloud's ready state for real or
fails.

## Substrate objects

| Object | Implementation | Cloud resources backed |
|---|---|---|
| Guest | Firecracker process and API socket, kernel, root filesystem, tap | EC2 instance, Compute Engine instance, Azure VM |
| Network | Linux network namespace with bridge and route table | VPC, Google Cloud network, Azure virtual network |
| Subnet | IPAM lease pool and bridge membership | subnet, subnetwork, Azure subnet |
| NIC | tap or veth, MAC, private IP leases, public IP bindings | ENI, Compute Engine network interface, Azure NIC |
| Security policy | nftables chains, sets and conntrack policy | security group, firewall, NSG |
| NAT | nftables SNAT or masquerade plus route programming | NAT gateway, Cloud NAT, Azure NAT Gateway |
| Load balancer | bound L4/L7 listener plus active probes | Elastic Load Balancing, Google Cloud load balancing, Azure Load Balancer |

Cloud handlers own request parsing, validation, idempotency, public error
shapes and response serialization; the substrate owns the host-side objects
and their cleanup.

## Lifecycle mapping

Provisioning states reflect real host-side progress:

- `pending` / `PROVISIONING` / `Creating`: the guest's resources are being
  prepared and the guest is not ready.
- `running` / `RUNNING` / `PowerState/running`: the Firecracker guest is
  alive, its network is attached, and it answers on its private address.
- stopped or deallocated: the guest process is stopped and its resources are
  kept or released as the cloud defines; a deallocated Azure machine keeps its
  disk.
- terminated or deleted: the guest process, sockets, taps, leases, nftables
  rules and proxy backends are removed.

Network ready states mean the Linux resources exist and are programmed;
health states mean probes observed real target behaviour; security states mean
the nftables rules are installed on the packet path.

## Metadata services

Each guest reaches its cloud's instance metadata at the provider's address —
`169.254.169.254`, and `metadata.google.internal` for Compute Engine — and the
handler resolves the guest's private source address to answer with that
instance's own fields: Amazon EC2 IMDS with the instance ID, AMI, addresses,
region, IAM profile and tags; the Compute Engine metadata server with the
project, zone, instance, interfaces, service accounts and custom metadata;
Azure IMDS with the VM, NIC, subscription, resource group, location and
network records. A static global fixture is not acceptable.

## Load balancers

Control-plane APIs create and mutate load balancers, target groups, backend
services, forwarding rules and listeners. They start no simulator-private
listener port as a side effect unless the cloud's control plane does so as part
of provisioning. The data plane is reachable through the cloud-shaped endpoint
the control plane advertises — DNS name, VIP, scheme, port and path — and the
plumbing beneath (the simulator mux, Docker networking, namespaces or bound
listeners) never leaks into API responses or client configuration.

- L4 listeners proxy TCP streams to healthy backends.
- L7 listeners proxy HTTP and apply the cloud's listener, rule or URL-map
  behaviour.
- Health probes test each backend with the configured protocol, port,
  thresholds and path, on the target group's own interval.
- Target-health APIs return the probe result.

If the host cannot materialize the advertised endpoint, provisioning or
data-plane dispatch fails with the cloud's error shape.

## Security enforcement

Security groups, firewalls and NSGs compile into nftables chains and sets on
the relevant NIC or network namespace, honouring direction, protocol, port
ranges, CIDR sources and destinations, security-group and tag references where
the cloud supports them, Google Cloud and Azure priority ordering, and stateful
return traffic where the cloud is stateful. A rule update recompiles the
affected packet path; a rule stored only in JSON is not implemented.

## IPAM and routing

IP allocation is lease-based per subnet, never derived from a store's length
or a counter that can collide after a deletion or a restart. Every private IP,
public IP, route and NAT mapping the API exposes corresponds to a host-side
binding: an address on a tap, veth, NAT or proxy object, a route in the
namespace, or an installed SNAT or DNAT rule.

## Test contract

Each public API path on the substrate ships with official SDK coverage, vendor
CLI coverage where the CLI exposes the path, and Terraform coverage where the
provider exposes the resource. The tests prove behaviour, not metadata: VM
tests observe a real guest lifecycle and per-instance metadata; network tests
verify non-colliding leases and reachable and blocked paths; security tests
prove that denied packets drop and allowed packets pass; load-balancer tests
register reachable and unreachable targets and verify proxying and health
transitions. Tests gate on the host capability probe, the one skip `AGENTS.md`
allows, so Linux CI runs them and macOS does not.

Two CI targets guard the substrate itself. `make firecracker-test` installs a
pinned Firecracker release, requires `/dev/kvm`, boots a Firecracker CI Linux
guest, copies the repository's `testdata/eval-arithmetic` Go source and the
configured Go toolchain into its root filesystem, and runs `go test`,
`go build` and several arithmetic executions inside the microVM.
`make realexec-network-test` creates a dedicated network namespace holding
subnet bridges and gateways, guest namespaces, veth NICs, leased addresses,
routed egress, SNAT state, routes and nftables tables; verifies gateway,
namespace-to-namespace and egress reachability with real packets; and verifies
that cleanup removes every host artifact.

## What each cloud runs on the substrate

| Family | AWS | Google Cloud | Azure |
|---|---|---|---|
| VM lifecycle | `RunInstances` boots a Firecracker guest; lifecycle APIs act on the guest and its tap. | Instances boot Firecracker guests; lifecycle APIs act on the guest and its tap. | Virtual machines boot Firecracker guests; lifecycle APIs act on the guest and its tap. |
| Guest metadata | IMDS through a `169.254.169.254` DNAT, instance-specific. | `169.254.169.254`, `metadata.google.internal` and `metadata`, instance-specific. | IMDS through a `169.254.169.254` DNAT, VM-, NIC- and subnet-specific. |
| Network fabric | VPCs, subnets, ENIs, elastic IPs, NAT gateways, route tables and Auto Scaling ENIs. | Networks, subnetworks, instance NICs, regional addresses, routers, Cloud NAT and forwarding-rule public IPs. | Virtual networks, subnets, NIC addressing, public IPs, NAT gateway subnet programming and route tables. |
| Security policy | Security-group ingress on attached ENI packet paths. | Firewall ingress by priority, tags and source ranges. | Subnet and NIC NSGs by priority and service tags. |
| Load balancing | Target health from real TCP/HTTP probes; data-plane requests route to healthy targets. | Backend services, unmanaged instance groups, URL maps, forwarding rules, probes and proxying. | Frontend IP dispatch, backend pools, probes and proxying. |
