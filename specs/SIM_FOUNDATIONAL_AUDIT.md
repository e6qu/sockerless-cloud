# Simulator Foundational Service Audit

Date: 2026-05-27

This audit checks whether each per-cloud simulator has real cloud-API slices for foundational services: object storage, basic managed data stores, DNS, queues, event routing, stream/event ingestion, VPC/networking, NAT/egress, and managed load balancers.

This is not a license to add simulator-specific APIs. Any missing row below is a missing public cloud API slice and is tracked in `BUGS.md`.

## Summary

| Category | AWS simulator | GCP simulator | Azure simulator | Finding |
|---|---|---|---|---|
| Object storage | S3 implemented | GCS implemented | Blob/File/Queue/Table storage implemented | Present across all three. The stale surface-table marker cleanup plus S3 bucket-subresource implementation and row-level client coverage follow-ups were closed by BUG-1221/#281 and BUG-1226/#285. |
| Queue/message systems | SQS and SNS implemented | Pub/Sub implemented | Service Bus and Storage Queue implemented | Present for core queue/pub-sub flows. |
| Event routing | EventBridge rules/targets plus buses/policies/archives/replays implemented | Eventarc triggers plus channels/providers/channel connections implemented | Event Grid topics/domains/domain topics/system topics/partner topics/subscriptions implemented | Event routing parity is rounded out across the advanced event-service phase. |
| Stream/event ingestion | Kinesis implemented | Pub/Sub present for basic event bus flows | Event Hubs implemented | Present for the foundational stream-ingestion flows. |
| Managed NoSQL/data SaaS | DynamoDB implemented with query/filter equality predicates | BigQuery and Firestore implemented | Cosmos DB implemented | Present for the foundational managed analytics/document data flows. |
| DNS | Route 53 and Cloud Map implemented | Cloud DNS implemented | Private DNS and public DNS implemented | Present across all three for foundational DNS/discovery flows. |
| VPC/network primitives | VPC, subnet, IGW, route table, SG, EIP, NAT, ENI describe implemented and covered | Network, subnetwork, firewall, router/NAT, regional addresses, and VPC Access implemented and covered | VNet, subnet, NSG, public IP/prefix, NAT gateway, route table implemented and covered | Present across foundational egress and NAT/public-IP flows. |
| VM/EC2-like compute | EC2 instance lifecycle implemented | Compute Engine instance lifecycle implemented | Azure VM lifecycle implemented | Present across all three through public cloud APIs; any local execution substrate stays behind the simulator boundary. |
| Managed load balancers | ELBv2 implemented | Cloud Load Balancing resources implemented | Azure Load Balancer implemented | Present for foundational managed load-balancer flows. |
| Gateway/proxy APIs | API Gateway, API Gateway v2, CloudFront implemented | API Gateway implemented | APIM implemented | Present, but not a substitute for managed load-balancer APIs. |

## Current Implemented Slices

### AWS

Foundational slices registered today:

- Object storage: S3, including multipart and many bucket/object subresources.
- Data stores: DynamoDB, RDS, ElastiCache.
- DNS and discovery: Route 53, Cloud Map.
- Queue and pub-sub: SQS, SNS, including SNS to SQS fanout.
- Event routing: EventBridge rules, targets, tags, event buses, bus policies, archives, replays, and `PutEvents`, including SQS/SNS target delivery.
- Networking: EC2 VPCs, subnets, internet gateways, elastic IPs, NAT gateways, route tables, security groups, network-interface describe.
- VM/compute: EC2 instance lifecycle APIs, instance status, image/key-pair/type discovery, tags, volumes, and ENI attachment state.
- Managed load balancers: ELBv2 load balancers, target groups, listeners, target registration/health, attributes, tags, and account limits.
- Gateways and edge: API Gateway v1/v2, CloudFront, WAFv2, ACM.
- Identity and secrets: IAM, STS, Secrets Manager, SSM Parameter Store, KMS.
- Stream ingestion: Kinesis stream lifecycle, shards, records, iterators, tags, retention, monitoring, encryption state, shard-count updates, and limits.

Missing foundational slices:

No current missing foundational AWS slices from this audit remain open.

### GCP

Foundational slices registered today:

- Object storage: GCS.
- Queue/pub-sub: Pub/Sub.
- Event routing: Eventarc trigger lifecycle, channels, provider discovery/listing, and channel connections.
- DNS and discovery: Cloud DNS.
- Networking: Compute networks, subnetworks, firewalls, routers/NAT, regional addresses, and VPC Access connectors.
- VM/compute: Compute Engine instance lifecycle, zonal operations, machine/disk/image catalog reads, labels/tags, attached disks, and NIC metadata.
- Managed load balancers: Compute health checks, backend services, URL maps, target HTTP proxies, and global forwarding rules.
- Gateways: API Gateway.
- Data stores: Cloud SQL, Memorystore Redis, Secret Manager, BigQuery, Firestore.
- Identity/logging/build: IAM, OAuth2 token endpoint, Cloud Logging, Cloud Build, Service Usage.

Missing foundational slices:

No current missing foundational GCP slices from this audit remain open.

### Azure

Foundational slices registered today:

- Object/storage data planes: Blob, Files, Queues, Tables.
- Queue/message systems: Service Bus ARM/admin/data plane, REST, AMQP-over-WebSocket, and raw AMQP/TLS.
- Event routing: Event Grid topics, domains, domain topics, system topics, partner topics, event subscriptions, subscription validation, and custom-topic publish/delivery.
- Stream ingestion: Event Hubs ARM namespace/event hub/consumer group/auth-rule lifecycle plus AMQP send/receive over raw AMQP/TLS.
- DNS and discovery: Private DNS, public DNS zones and record sets.
- Networking: Virtual Networks, subnets, Network Security Groups, public IP addresses/prefixes, NAT gateways, subnet NAT association, and route tables.
- VM/compute: Network interfaces, public IPs, and `Microsoft.Compute/virtualMachines` lifecycle with instanceView and power-state operations.
- Managed load balancers: `Microsoft.Network/loadBalancers` with public IP frontends, backend pools, probes, load-balancing rules, and child-resource paths.
- Gateways: APIM.
- Data stores: Cache for Redis, PostgreSQL Flexible Server, Cosmos DB for NoSQL.
- Identity/secrets/logging: Managed Identity, Key Vault, Monitor, Application Insights, authorization/resources/tags.

Missing foundational slices:

No current missing foundational Azure slices from this audit remain open.

## Next Implementation Phase

Recommended order:

1. The AWS S3 bucket-subresource implementation and row-level client coverage follow-ups from issue #281 / BUG-1221 and issue #285 / BUG-1226 were closed.
2. The Azure Key Vault data-plane parity follow-up from issue #282 / BUG-1222 was closed.
3. Continue the standing audit cadence through BUG-1104: when a new community issue or provider/SDK path surfaces a missing public API slice, file the concrete BUG first, then implement the cloud-compatible public API with SDK, CLI, and Terraform coverage in the same PR.

Each added service slice must follow the simulator testing contract: official SDK tests, vendor CLI tests, and Terraform provider tests in the same PR unless the public API is not exposed by one of those client surfaces.
