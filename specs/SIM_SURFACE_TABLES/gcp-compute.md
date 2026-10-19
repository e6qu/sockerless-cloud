# Sim surface — gcp-compute

Surface registered in `simulator-gcp/compute.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:1011::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:1035::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:1061::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:1087::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:1111::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:1123::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:1146::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceTemplates` | ✓ `simulator-gcp/compute.go:1158::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1176::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1214::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1231::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/expandIpCidrRange` | ✓ `simulator-gcp/compute.go:1260::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1295::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/setPrivateIpGoogleAccess` | ✓ `simulator-gcp/compute.go:1318::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1336::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1368::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1401::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1413::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1436::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1451::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1512::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1558::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1571::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/setLabels` | ✓ `simulator-gcp/compute.go:1595::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1619::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1633::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1662::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1675::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1699::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1711::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}/getRouterStatus` | ✓ `simulator-gcp/compute.go:1748::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute.go:1768::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute.go:1773::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:1776::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1946::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1967::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1979::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1991::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/addInstances` | ✓ `simulator-gcp/compute.go:2000::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/removeInstances` | ✓ `simulator-gcp/compute.go:2026::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/listInstances` | ✓ `simulator-gcp/compute.go:2058::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/setNamedPorts` | ✓ `simulator-gcp/compute.go:2082::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}` | ○ `simulator-gcp/compute.go:2263::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones` | ○ `simulator-gcp/compute.go:2266::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes/{machineType}` | ○ `simulator-gcp/compute.go:2288::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes` | ○ `simulator-gcp/compute.go:2299::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes/{diskType}` | ○ `simulator-gcp/compute.go:2313::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/imageViews/{resourceId}` | ✓ `simulator-gcp/compute.go:2331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/imageViews` | ✓ `simulator-gcp/compute.go:2341::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2557::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}` | ✓ `simulator-gcp/compute.go:2616::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2629::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instances` | ✓ `simulator-gcp/compute.go:2653::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2683::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setTags` | ✓ `simulator-gcp/compute.go:2716::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2764::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2793::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2824::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/resize` | ✓ `simulator-gcp/compute.go:2838::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2863::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/disks` | ✓ `simulator-gcp/compute.go:2898::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute.go:2933::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:2936::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:952::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:997::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/addresses` | ✓ `simulator-gcp/compute_more.go:1007::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/backendServices` | ✓ `simulator-gcp/compute_more.go:1030::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/healthChecks` | ✓ `simulator-gcp/compute_more.go:1033::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/urlMaps` | ✓ `simulator-gcp/compute_more.go:1036::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/targetHttpProxies` | ✓ `simulator-gcp/compute_more.go:1039::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/forwardingRules` | ✓ `simulator-gcp/compute_more.go:1042::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceGroups` | ✓ `simulator-gcp/compute_more.go:1045::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMachineType` | ✓ `simulator-gcp/compute_more.go:1088::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMetadata` | ✓ `simulator-gcp/compute_more.go:1108::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/attachDisk` | ✓ `simulator-gcp/compute_more.go:1127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/detachDisk` | ✓ `simulator-gcp/compute_more.go:1145::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}/serialPort` | ✓ `simulator-gcp/compute_more.go:1166::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}` | ○ `simulator-gcp/compute_more.go:821::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions` | ○ `simulator-gcp/compute_more.go:824::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes/{acceleratorType}` | ○ `simulator-gcp/compute_more.go:846::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes` | ○ `simulator-gcp/compute_more.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/acceleratorTypes` | ○ `simulator-gcp/compute_more.go:859::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes` | ○ `simulator-gcp/compute_more.go:883::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/diskTypes` | ○ `simulator-gcp/compute_more.go:891::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/machineTypes` | ○ `simulator-gcp/compute_more.go:904::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations` | ✓ `simulator-gcp/compute_more.go:938::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations` | ✓ `simulator-gcp/compute_more.go:941::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations` | ✓ `simulator-gcp/compute_more.go:944::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:957::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:958::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute_more.go:959::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/operations/{name}/wait` | ✓ `simulator-gcp/compute_more.go:961::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/operations` | ✓ `simulator-gcp/compute_more.go:965::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/subnetworks` | ✓ `simulator-gcp/compute_more.go:988::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/licenseCodes/{licenseCode}` | ✓ `simulator-gcp/compute_more2.go:219::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/nodeTypes/{name}` | ○ `simulator-gcp/compute_more2.go:652::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/nodeTypes` | ○ `simulator-gcp/compute_more2.go:655::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/nodeTypes` | ○ `simulator-gcp/compute_more2.go:663::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/storagePoolTypes/{name}` | ○ `simulator-gcp/compute_more2.go:692::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/storagePoolTypes` | ○ `simulator-gcp/compute_more2.go:695::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/storagePoolTypes` | ○ `simulator-gcp/compute_more2.go:703::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networkProfiles/{name}` | ○ `simulator-gcp/compute_more2.go:730::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networkProfiles` | ○ `simulator-gcp/compute_more2.go:733::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/diskTypes/{name}` | ○ `simulator-gcp/compute_more2.go:758::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/diskTypes` | ○ `simulator-gcp/compute_more2.go:761::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/firewallPolicies` | ✓ `simulator-gcp/compute_more3.go:410::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/firewallPolicies/getEffectiveFirewalls` | ○ `simulator-gcp/compute_more3.go:444::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/networkEndpointGroups` | ✓ `simulator-gcp/compute_more3.go:456::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /compute/v1/projects/{project}/regions/{region}/routers/{router}` | ✓ `simulator-gcp/compute_more3.go:619::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/routers` | ✓ `simulator-gcp/compute_more3.go:650::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:692::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:713::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:741::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:756::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/preview` | ✓ `simulator-gcp/compute_more3.go:813::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/vmExtensionPolicies/{name}/delete` | ✓ `simulator-gcp/compute_more4.go:144::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchNamedSet` | ✓ `simulator-gcp/compute_more_verbs.go:114::false` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateNamedSet` | ✓ `simulator-gcp/compute_more_verbs.go:115::true` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteNamedSet` | ✓ `simulator-gcp/compute_more_verbs.go:117::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/locations/global/operations` | ✓ `simulator-gcp/compute_more_verbs.go:244::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/locations/global/operations/{operation}` | ✓ `simulator-gcp/compute_more_verbs.go:261::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/locations/global/operations/{operation}` | ✓ `simulator-gcp/compute_more_verbs.go:270::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getNamedSet` | ✓ `simulator-gcp/compute_more_verbs.go:45::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/listNamedSets` | ✓ `simulator-gcp/compute_more_verbs.go:59::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Issue #266 closed the Compute Engine VM lifecycle gap. Zonal instance insert/get/list/delete/start/stop, aggregated instances, labels/tags, machine types, disk types, images, attached disks, and NIC metadata are covered by `simulator-gcp/sdk-tests/compute_test.go`, `simulator-gcp/cli-tests/compute_instances_test.go`, and `simulator-gcp/terraform-tests/main.tf` through `google_compute_instance`.

PR #392 added global instance template CRUD (`POST/GET/LIST/DELETE /compute/v1/projects/{p}/global/instanceTemplates`) plus the aggregated list endpoint used by `gcloud compute instance-templates list`. Tested by `simulator-gcp/sdk-tests/compute_test.go` (`TestCompute_InstanceTemplateCRUD`) and `simulator-gcp/cli-tests/client_surface_audit_test.go` (`TestCLI_ComputeInstanceTemplate`).

Issue #279 closed the Compute NAT/public-IP parity pass. Regional address insert/get/list/delete, address `setLabels`, manual Cloud NAT router patch/validation, router status, and regional operation wait are covered by `simulator-gcp/sdk-tests/compute_test.go`, `simulator-gcp/cli-tests/compute_nat_test.go`, and `simulator-gcp/terraform-tests/main.tf` through `google_compute_address`, `google_compute_router`, and `google_compute_router_nat`.
<!-- HAND-WRITTEN END -->
