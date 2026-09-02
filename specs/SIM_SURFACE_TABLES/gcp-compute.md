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
| `GET /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:1010::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:1022::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/instanceTemplates/{name}` | ✓ `simulator-gcp/compute.go:1041::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceTemplates` | ✓ `simulator-gcp/compute.go:1053::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1071::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1116::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/subnetworks` | ✓ `simulator-gcp/compute.go:1133::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/expandIpCidrRange` | ✓ `simulator-gcp/compute.go:1158::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1192::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/setPrivateIpGoogleAccess` | ✓ `simulator-gcp/compute.go:1215::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}` | ✓ `simulator-gcp/compute.go:1233::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1262::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1295::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/firewalls` | ✓ `simulator-gcp/compute.go:1307::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1326::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/firewalls/{name}` | ✓ `simulator-gcp/compute.go:1344::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1404::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1451::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/addresses` | ✓ `simulator-gcp/compute.go:1464::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/setLabels` | ✓ `simulator-gcp/compute.go:1484::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{name}` | ✓ `simulator-gcp/compute.go:1508::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1522::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1561::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers` | ✓ `simulator-gcp/compute.go:1574::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1594::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/regions/{region}/routers/{name}` | ✓ `simulator-gcp/compute.go:1606::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{name}/getRouterStatus` | ✓ `simulator-gcp/compute.go:1652::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute.go:1672::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute.go:1677::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:1680::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1888::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1910::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups` | ✓ `simulator-gcp/compute.go:1922::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}` | ✓ `simulator-gcp/compute.go:1934::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/addInstances` | ✓ `simulator-gcp/compute.go:1943::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/removeInstances` | ✓ `simulator-gcp/compute.go:1969::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/listInstances` | ✓ `simulator-gcp/compute.go:2001::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/setNamedPorts` | ✓ `simulator-gcp/compute.go:2025::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}` | ○ `simulator-gcp/compute.go:2212::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones` | ○ `simulator-gcp/compute.go:2215::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes/{machineType}` | ○ `simulator-gcp/compute.go:2237::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/machineTypes` | ○ `simulator-gcp/compute.go:2252::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes/{diskType}` | ○ `simulator-gcp/compute.go:2269::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/images/{image}` | ✓ `simulator-gcp/compute.go:2282::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/images/{first}/{second}` | ✓ `simulator-gcp/compute.go:2299::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2507::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}` | ✓ `simulator-gcp/compute.go:2572::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances` | ✓ `simulator-gcp/compute.go:2589::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instances` | ✓ `simulator-gcp/compute.go:2609::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/instances/{name}` | ✓ `simulator-gcp/compute.go:2637::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/stop` | ✓ `simulator-gcp/compute.go:2652::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/start` | ✓ `simulator-gcp/compute.go:2668::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2693::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setTags` | ✓ `simulator-gcp/compute.go:2726::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2777::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/disks` | ✓ `simulator-gcp/compute.go:2821::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{name}` | ✓ `simulator-gcp/compute.go:2838::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/resize` | ✓ `simulator-gcp/compute.go:2852::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/setLabels` | ✓ `simulator-gcp/compute.go:2877::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/disks` | ✓ `simulator-gcp/compute.go:2912::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute.go:2947::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/operations/{name}/wait` | ✓ `simulator-gcp/compute.go:2950::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:856::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:905::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networks` | ✓ `simulator-gcp/compute.go:919::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:939::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/networks/{name}` | ✓ `simulator-gcp/compute.go:959::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/instanceTemplates` | ✓ `simulator-gcp/compute.go:985::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/addresses` | ✓ `simulator-gcp/compute_more.go:1001::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/backendServices` | ✓ `simulator-gcp/compute_more.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/healthChecks` | ✓ `simulator-gcp/compute_more.go:1027::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/urlMaps` | ✓ `simulator-gcp/compute_more.go:1030::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/targetHttpProxies` | ✓ `simulator-gcp/compute_more.go:1033::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/forwardingRules` | ✓ `simulator-gcp/compute_more.go:1036::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/instanceGroups` | ✓ `simulator-gcp/compute_more.go:1039::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/reset` | ✓ `simulator-gcp/compute_more.go:1082::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMachineType` | ✓ `simulator-gcp/compute_more.go:1090::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMetadata` | ✓ `simulator-gcp/compute_more.go:1110::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/attachDisk` | ✓ `simulator-gcp/compute_more.go:1129::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/detachDisk` | ✓ `simulator-gcp/compute_more.go:1147::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}/serialPort` | ✓ `simulator-gcp/compute_more.go:1165::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}` | ○ `simulator-gcp/compute_more.go:810::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions` | ○ `simulator-gcp/compute_more.go:813::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes/{acceleratorType}` | ○ `simulator-gcp/compute_more.go:835::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes` | ○ `simulator-gcp/compute_more.go:838::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/acceleratorTypes` | ○ `simulator-gcp/compute_more.go:848::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/diskTypes` | ○ `simulator-gcp/compute_more.go:872::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/diskTypes` | ○ `simulator-gcp/compute_more.go:880::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/machineTypes` | ○ `simulator-gcp/compute_more.go:893::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/operations` | ✓ `simulator-gcp/compute_more.go:934::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/operations` | ✓ `simulator-gcp/compute_more.go:937::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/operations` | ✓ `simulator-gcp/compute_more.go:940::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/zones/{zone}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:951::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/regions/{region}/operations/{name}` | ✓ `simulator-gcp/compute_more.go:952::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/operations/{name}` | ✓ `simulator-gcp/compute_more.go:953::delOp` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/operations/{name}/wait` | ✓ `simulator-gcp/compute_more.go:955::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/operations` | ✓ `simulator-gcp/compute_more.go:959::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/subnetworks` | ✓ `simulator-gcp/compute_more.go:982::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/licenseCodes/{licenseCode}` | ✓ `simulator-gcp/compute_more2.go:220::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/nodeTypes/{name}` | ○ `simulator-gcp/compute_more2.go:649::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/nodeTypes` | ○ `simulator-gcp/compute_more2.go:652::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/nodeTypes` | ○ `simulator-gcp/compute_more2.go:660::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/storagePoolTypes/{name}` | ○ `simulator-gcp/compute_more2.go:689::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/storagePoolTypes` | ○ `simulator-gcp/compute_more2.go:692::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/storagePoolTypes` | ○ `simulator-gcp/compute_more2.go:700::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networkProfiles/{name}` | ○ `simulator-gcp/compute_more2.go:727::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/networkProfiles` | ○ `simulator-gcp/compute_more2.go:730::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/diskTypes/{name}` | ○ `simulator-gcp/compute_more2.go:755::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/diskTypes` | ○ `simulator-gcp/compute_more2.go:758::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/firewallPolicies` | ✓ `simulator-gcp/compute_more3.go:364::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/firewallPolicies/getEffectiveFirewalls` | ○ `simulator-gcp/compute_more3.go:398::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/networkEndpointGroups` | ✓ `simulator-gcp/compute_more3.go:410::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /compute/v1/projects/{project}/regions/{region}/routers/{router}` | ✓ `simulator-gcp/compute_more3.go:573::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/aggregated/routers` | ✓ `simulator-gcp/compute_more3.go:604::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:646::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:667::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:695::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getRoutePolicy` | ✓ `simulator-gcp/compute_more3.go:710::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/preview` | ✓ `simulator-gcp/compute_more3.go:767::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
