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
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:1016::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1044::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1056::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1086::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1089::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:207::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:286::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:300::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:336::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:389::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:435::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:452::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:480::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:521::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes/{change}` | ✓ `simulator-gcp/dns.go:584::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:600::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:642::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:647::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zoneAction}` | ✓ `simulator-gcp/dns.go:659::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys` | ✓ `simulator-gcp/dns.go:679::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys/{dnsKeyId}` | ✓ `simulator-gcp/dns.go:706::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations` | ✓ `simulator-gcp/dns.go:725::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations/{operation}` | ✓ `simulator-gcp/dns.go:761::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}` | ○ `simulator-gcp/dns.go:778::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:790::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:814::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:840::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:851::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:879::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:882::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:890::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:914::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:937::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:948::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:982::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:985::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:990::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
