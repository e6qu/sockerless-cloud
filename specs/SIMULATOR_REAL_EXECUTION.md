# Simulator Real-Execution Substrate

Issues #332-#338 tracked the move from metadata-only VM/network resources to
real execution. Issue #338 was the comparison/meta issue that kept the
simulator scope aligned with real cloud behavior while the work was in flight.
This document is the implementation contract and completion audit for that
program.

The first rule is unchanged: simulator public APIs must match the public cloud.
The substrate is an implementation detail behind EC2, GCE, Azure VM, VPC,
firewall, NAT, and load-balancer APIs. It must not add simulator-only knobs to
those public APIs.

## Scope

The real-execution substrate covers four resource families:

1. VM instances: AWS EC2 instances, GCP Compute Engine instances, and Azure VMs.
2. Network fabric: VPCs/networks, subnets, route tables/routers, NICs/ENIs,
   public/elastic IPs, and NAT gateways/Cloud NAT/Azure NAT.
3. Security policy: AWS security groups, GCP firewalls, and Azure NSGs.
4. Load balancing: AWS ELBv2, GCP forwarding-rule/backend-service load
   balancing, and Azure Load Balancer.

Container/FaaS surfaces remain Docker/Podman-backed through
`StartContainerSync`. Firecracker is only for VM-level APIs.

## Capability Model

A simulator build can include the real-execution substrate on every platform,
but a migrated resource may only succeed when the host has the required
capabilities.

Required capabilities:

- Linux host.
- Firecracker binary and jailer, or an explicitly chosen compatible launcher.
- `/dev/kvm` usable by the simulator process.
- Permission to create network namespaces.
- Permission to create bridges, tap/veth devices, and assign addresses.
- Permission to program routes through netlink.
- Permission to install and remove `nftables` rules.
- Permission to materialize load-balancer data-plane endpoints through the
  required listener, gateway, DNS, namespace, and proxy primitives.

Capability checks must be explicit and deterministic. They may inspect the host
and required binaries, but they must not create permanent resources as a probe.
When a capability is missing for a migrated API path, the handler fails loudly
with the real cloud error shape used for failed provisioning.

There is no metadata fallback. A resource that requires real execution either
creates the real execution object and reaches the cloud's ready state, or it
fails.

## Substrate Objects

The substrate should expose cloud-neutral implementation objects below the
cloud-specific handlers:

| Object | Implementation | Cloud resources backed |
|---|---|---|
| Guest | Firecracker process/API socket, kernel, rootfs/overlay, vsock/tap | EC2 instance, GCE instance, Azure VM |
| Network | Linux netns plus bridge and route table | VPC, GCP network, Azure virtual network |
| Subnet | IPAM pool plus bridge membership policy | subnet/subnetwork/Azure subnet |
| NIC | tap/veth, MAC, private IP leases, public IP bindings | ENI, GCE networkInterface, Azure NIC |
| Security policy | nftables chains/sets and conntrack policy | security group, firewall, NSG |
| NAT | nftables SNAT/masquerade plus route programming | NAT gateway, Cloud NAT, Azure NAT Gateway |
| Load balancer | bound L4/L7 listener plus active probes | ELBv2, GCP LB, Azure LB |

Cloud handlers remain responsible for request parsing, validation, idempotency,
public error shapes, and response serialization. The substrate is responsible
for the host-side objects and cleanup.

## Lifecycle Mapping

Provisioning states must reflect actual host-side progress:

- instance `pending` / `PROVISIONING` / `Creating`: guest resources are being
  prepared but the guest is not ready.
- instance `running` / `RUNNING` / `PowerState/running`: Firecracker guest is
  alive, network is attached, and metadata service is reachable from the guest.
- stopped/deallocated: guest process is stopped and resources are retained or
  released according to the cloud's semantics.
- terminated/deleted: guest process, sockets, taps, leases, nftables rules, and
  proxy backends are removed.

The same rule applies to networking:

- VPC/subnet/NIC/public IP/NAT/LB ready states mean the corresponding Linux
  resources exist and are programmed.
- Health states mean probes have observed real target behavior.
- Security states mean nftables rules are installed on the packet path.

## Metadata Services

Instance metadata services must be per-instance once a VM API path is migrated:

- AWS IMDS at `169.254.169.254` reflects the EC2 instance ID, AMI, private IP,
  public IP, region, IAM profile, and tags for that guest.
- GCP metadata server reflects the GCE project, zone, instance name, ID, network
  interfaces, service accounts, and custom metadata for that guest.
- Azure IMDS reflects the Azure VM, NIC, subscription, resource group, location,
  compute shape, and network records for that guest.

Static global metadata fixtures are not acceptable for migrated VM paths.

## Load Balancers

Load-balancer control-plane APIs create and mutate load-balancer, target-group,
backend-service, forwarding-rule, and listener configuration. They must not
start simulator-private listener ports as a side effect unless the real cloud
control plane does that as part of provisioning.

The data plane must be reachable through the cloud-shaped endpoint the control
plane advertises: DNS name, VIP, scheme, port, and path. The local realization
may use the simulator mux, Caddy, Docker networking, Linux namespaces, or bound
listeners underneath, but that plumbing must not leak into public cloud API
responses or client configuration.

- L4 listeners proxy TCP streams to healthy backends.
- L7 listeners proxy HTTP and apply the cloud's listener/rule/url-map behavior.
- Health probes actively test each backend with the configured protocol, port,
  thresholds, and path when applicable.
- Target health APIs return the probe result, not a hardcoded healthy state.

If the host cannot materialize the required advertised data-plane endpoint,
provisioning or data-plane dispatch fails loudly with the cloud's error shape.

## Security Enforcement

Security groups, firewalls, and NSGs compile into nftables chains/sets on the
relevant NIC or network namespace.

The implementation must honor:

- direction,
- protocol,
- port ranges,
- CIDR sources/destinations,
- security-group/tag references where the cloud supports them,
- GCP/Azure priority ordering,
- stateful return traffic where the cloud semantics are stateful.

Rule updates must recompile the affected packet path. A rule that is only stored
in JSON is not implemented.

## IPAM And Routing

IP allocation must be lease-based per subnet. It must not use `len(store)` or
other counter-derived values that can collide after deletion or restart.

Every private IP, public IP, route, and NAT mapping exposed through the public
API must correspond to a host-side binding:

- address assigned to a tap/veth or NAT/proxy object,
- route programmed in the namespace,
- SNAT/DNAT rule installed where applicable.

## Test Contract

Each migrated public API path must ship with public-client coverage:

- official SDK,
- vendor CLI where the public CLI exposes the path,
- Terraform provider where the provider exposes the resource.

The tests must prove real behavior, not only metadata:

- VM tests observe a real guest lifecycle and per-instance metadata.
- Network tests verify non-colliding IP leases and reachable/blocked paths.
- Security tests prove denied packets are dropped and allowed packets pass.
- Load-balancer tests register reachable and unreachable targets and verify
  proxying plus health transitions.

No test may replace the substrate with mocks or synthetic responses.

CI also includes a Firecracker microVM arithmetic smoke target. It installs a
pinned official Firecracker release, requires `/dev/kvm`, boots an official
Firecracker CI Linux guest, copies the repo's real `eval-arithmetic` Go source
and configured Go toolchain into the guest rootfs, then runs `go test`,
`go build`, and multiple arithmetic executions inside the microVM. This is the
minimum CI guard for guest execution.

CI also runs the real host-network substrate target. It requires Linux, real
host networking tools, nftables, `/dev/kvm`, and sufficient privileges. The
target creates a dedicated network namespace containing subnet bridges and
gateways, guest network namespaces, veth NICs, lease-based addresses, routed
egress, SNAT state, routes, and nftables tables; verifies gateway,
namespace-to-namespace, and egress reachability with real packets; and verifies
cleanup removes the host artifacts. This is the minimum CI guard for the
network/NIC/NAT substrate itself; cloud VM/security/LB tests are added to the
same real-execution path as each public API is migrated.

## Implementation Order

1. Host capability detection and cleanup scaffolding landed without changing
   public resource behavior.
2. The first real network/IPAM/NIC substrate landed without changing public
   resource behavior; each network now owns a Linux network namespace with its
   bridge and gateway inside it.
3. Public VPC/network/subnet/NIC/public-IP/NAT route paths for AWS, GCP, and
   Azure were migrated onto the substrate and fail loudly when host networking
   capabilities are unavailable.
4. AWS security-group ingress rules and AWS ELBv2 health/data-plane paths were
   migrated onto the packet path: security groups compile to nftables on ENI
   veth peers, target health uses real probes, and ELBv2 data-plane requests
   route by load-balancer DNS host to healthy targets without binding listener
   ports from the Query Protocol control plane.
5. GCP/Azure security enforcement was migrated onto that packet path.
6. The remaining GCP/Azure load-balancer proxying and health checks were
   migrated onto that packet path.
7. AWS EC2, GCP Compute Engine, and Azure VM lifecycle paths were attached to
   Firecracker guests using TAP NICs on the real network substrate. Public
   running state is gated on real guest packet reachability, and public
   stop/start/restart/delete actions operate on the Firecracker process and TAP
   lifecycle.
8. Guest-visible provider metadata was attached to the real guest packet path.
   AWS EC2 IMDS, GCP metadata server, and Azure IMDS are reachable from inside
   the guest through provider-shaped addresses/hostnames, and the metadata
   handlers resolve the guest private source IP to return instance-specific
   provider metadata.

## Completion Audit

Issue #332's real-execution umbrella was satisfied when all tracked subfamilies
had real substrate backing and public-client coverage:

| Family | AWS | GCP | Azure |
|---|---|---|---|
| VM lifecycle | EC2 instances boot Firecracker guests through `RunInstances` and lifecycle APIs operate on the guest/TAP process. | Compute Engine instances boot Firecracker guests and lifecycle APIs operate on the guest/TAP process. | Azure VMs boot Firecracker guests and lifecycle APIs operate on the guest/TAP process. |
| Guest metadata | IMDS routes through `169.254.169.254` DNAT and returns EC2 instance-specific fields. | `169.254.169.254`, `metadata.google.internal`, and `metadata` route to instance-specific Compute metadata. | IMDS routes through `169.254.169.254` DNAT and returns VM/NIC/subnet-specific fields. |
| Network fabric | VPCs, subnets, ENIs, EIPs, NAT gateways, route tables, and Auto Scaling ENIs use the realexec netns/bridge/veth/IPAM/SNAT substrate. | Networks, subnetworks, instance NICs, regional addresses, routers, Cloud NAT, and forwarding-rule public IPs use the realexec substrate. | VNets, subnets, NIC private IP/MAC allocation, public IPs, NAT gateway subnet programming, and route tables use the realexec substrate. |
| Security policy | EC2 security-group ingress compiles to nftables on attached real ENI packet paths. | GCP firewall ingress compiles by priority/tags/source ranges onto real NIC packet paths. | Azure subnet/NIC NSGs compile by priority and service tags onto real NIC packet paths. |
| Load balancing | ELBv2 target health performs real TCP/HTTP probes and data-plane requests route to healthy targets. | Backend services, unmanaged instance groups, URL maps, forwarding rules, probes, and proxying use real backend reachability. | Azure Load Balancer frontend IP dispatch, backend pools, probes, and proxying use real backend reachability. |

No remaining #332 sub-issue stayed open after this audit. Future real-execution
regressions or coverage gaps should be filed as new concrete issues/BUG entries
instead of reopening this umbrella unless the whole architectural premise
regresses.
