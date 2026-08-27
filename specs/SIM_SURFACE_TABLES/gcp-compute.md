# Sim surface — gcp-compute

Surface registered in `simulator-gcp/compute.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:789::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:835::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:869::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:889::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:915::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:940::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:952::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:971::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceTemplates` | ✓ `simulator-gcp/compute.go:983::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1001::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1046::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1063::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1084::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1113::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1146::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1158::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1177::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1195::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1301::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1314::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/setLabels` | ✓ `simulator-gcp/compute.go:1334::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1358::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1372::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1411::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1424::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1444::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1456::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}/getRouterStatus` | ✓ `simulator-gcp/compute.go:1502::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute.go:1522::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute.go:1527::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:1530::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1735::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1757::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1769::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1781::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/addInstances` | ✓ `simulator-gcp/compute.go:1790::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/removeInstances` | ✓ `simulator-gcp/compute.go:1816::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/listInstances` | ✓ `simulator-gcp/compute.go:1848::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/setNamedPorts` | ✓ `simulator-gcp/compute.go:1872::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}` | ✓ `simulator-gcp/compute.go:2059::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones` | ✓ `simulator-gcp/compute.go:2062::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes/{machineType}` | ✓ `simulator-gcp/compute.go:2084::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes` | ✓ `simulator-gcp/compute.go:2099::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes/{diskType}` | ✓ `simulator-gcp/compute.go:2116::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/images/{image}` | ✓ `simulator-gcp/compute.go:2142::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/images/family/{family}` | ✓ `simulator-gcp/compute.go:2153::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2343::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}` | ✓ `simulator-gcp/compute.go:2408::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2425::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instances` | ✓ `simulator-gcp/compute.go:2445::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/instances/{name}` | ✓ `simulator-gcp/compute.go:2473::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/stop` | ✓ `simulator-gcp/compute.go:2488::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/start` | ✓ `simulator-gcp/compute.go:2504::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2529::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setTags` | ✓ `simulator-gcp/compute.go:2562::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2612::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2642::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2656::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2673::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/resize` | ✓ `simulator-gcp/compute.go:2687::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2712::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/disks` | ✓ `simulator-gcp/compute.go:2747::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute.go:2782::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:2785::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}` | ✓ `simulator-gcp/compute_more.go:474::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions` | ✓ `simulator-gcp/compute_more.go:477::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes/{acceleratorType}` | ✓ `simulator-gcp/compute_more.go:499::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes` | ✓ `simulator-gcp/compute_more.go:502::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/acceleratorTypes` | ✓ `simulator-gcp/compute_more.go:512::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes` | ✓ `simulator-gcp/compute_more.go:536::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/diskTypes` | ✓ `simulator-gcp/compute_more.go:544::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/machineTypes` | ✓ `simulator-gcp/compute_more.go:557::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations` | ✓ `simulator-gcp/compute_more.go:598::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations` | ✓ `simulator-gcp/compute_more.go:601::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations` | ✓ `simulator-gcp/compute_more.go:604::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:615::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:616::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute_more.go:617::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/operations/{name}/wait` | ✓ `simulator-gcp/compute_more.go:619::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/operations` | ✓ `simulator-gcp/compute_more.go:623::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/subnetworks` | ✓ `simulator-gcp/compute_more.go:646::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/addresses` | ✓ `simulator-gcp/compute_more.go:665::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/backendServices` | ✓ `simulator-gcp/compute_more.go:688::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/healthChecks` | ✓ `simulator-gcp/compute_more.go:691::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/urlMaps` | ✓ `simulator-gcp/compute_more.go:694::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/targetHttpProxies` | ✓ `simulator-gcp/compute_more.go:697::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/forwardingRules` | ✓ `simulator-gcp/compute_more.go:700::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceGroups` | ✓ `simulator-gcp/compute_more.go:703::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/reset` | ✓ `simulator-gcp/compute_more.go:746::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMachineType` | ✓ `simulator-gcp/compute_more.go:754::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMetadata` | ✓ `simulator-gcp/compute_more.go:774::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/attachDisk` | ✓ `simulator-gcp/compute_more.go:793::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/detachDisk` | ✓ `simulator-gcp/compute_more.go:811::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}/serialPort` | ✓ `simulator-gcp/compute_more.go:829::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/firewallPolicies` | ✓ `simulator-gcp/compute_more3.go:363::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/firewallPolicies/getEffectiveFirewalls` | ✓ `simulator-gcp/compute_more3.go:397::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/networkEndpointGroups` | ✓ `simulator-gcp/compute_more3.go:409::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /compute/v1/projects/{project}/regions/{region}/routers/{router}` | ✓ `simulator-gcp/compute_more3.go:572::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/routers` | ✓ `simulator-gcp/compute_more3.go:603::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:645::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:666::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:694::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:709::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/preview` | ✓ `simulator-gcp/compute_more3.go:766::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Issue #266 closed the Compute Engine VM lifecycle gap. Zonal instance insert/get/list/delete/start/stop, aggregated instances, labels/tags, machine types, disk types, images, attached disks, and NIC metadata are covered by `simulator-gcp/sdk-tests/compute_test.go`, `simulator-gcp/cli-tests/compute_instances_test.go`, and `simulator-gcp/terraform-tests/main.tf` through `google_compute_instance`.

PR #392 added global instance template CRUD (`POST/GET/LIST/DELETE /compute/v1/projects/{p}/global/instanceTemplates`) plus the aggregated list endpoint used by `gcloud compute instance-templates list`. Tested by `simulator-gcp/sdk-tests/compute_test.go` (`TestCompute_InstanceTemplateCRUD`) and `simulator-gcp/cli-tests/client_surface_audit_test.go` (`TestCLI_ComputeInstanceTemplate`).

Issue #279 closed the Compute NAT/public-IP parity pass. Regional address insert/get/list/delete, address `setLabels`, manual Cloud NAT router patch/validation, router status, and regional operation wait are covered by `simulator-gcp/sdk-tests/compute_test.go`, `simulator-gcp/cli-tests/compute_nat_test.go`, and `simulator-gcp/terraform-tests/main.tf` through `google_compute_address`, `google_compute_router`, and `google_compute_router_nat`.
<!-- HAND-WRITTEN END -->
