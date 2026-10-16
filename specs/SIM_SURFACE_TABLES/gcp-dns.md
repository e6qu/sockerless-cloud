# Sim surface — gcp-dns

Surface registered in `simulator-gcp/dns.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:1020::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:1023::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:1028::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:1054::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1082::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1094::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1124::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1127::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations` | 501 `simulator-gcp/dns.go:221::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations/{location}` | 501 `simulator-gcp/dns.go:222::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations/{location}/outboundEndpoints` | 501 `simulator-gcp/dns.go:223::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/locations/{location}/outboundEndpoints` | 501 `simulator-gcp/dns.go:224::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations/{location}/outboundEndpoints/{endpoint}` | 501 `simulator-gcp/dns.go:225::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/locations/{location}/outboundEndpoints/{endpoint}` | 501 `simulator-gcp/dns.go:226::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/locations/{location}/outboundEndpoints/{endpoint}` | 501 `simulator-gcp/dns.go:227::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations/{location}/operations` | 501 `simulator-gcp/dns.go:228::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/locations/{location}/operations/{operation}` | 501 `simulator-gcp/dns.go:229::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/locations/{location}/operations/{operation}` | 501 `simulator-gcp/dns.go:230::dnsUnserved` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/locations/{location}/operations/{opverb}` | 501 `simulator-gcp/dns.go:231::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:240::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:304::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:323::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:337::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:375::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:428::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:474::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:491::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:519::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:560::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes/{change}` | ✓ `simulator-gcp/dns.go:623::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:639::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:676::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:681::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zoneAction}` | ✓ `simulator-gcp/dns.go:693::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys` | ✓ `simulator-gcp/dns.go:713::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys/{dnsKeyId}` | ✓ `simulator-gcp/dns.go:740::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations` | ✓ `simulator-gcp/dns.go:759::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations/{operation}` | ✓ `simulator-gcp/dns.go:795::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}` | ✓ `simulator-gcp/dns.go:812::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:828::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:852::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:878::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:889::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:917::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:920::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:928::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:952::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:975::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:986::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
