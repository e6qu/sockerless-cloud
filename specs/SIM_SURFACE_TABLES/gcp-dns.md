# Sim surface — gcp-dns

Surface registered in `simulator-gcp/dns.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:208::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones` | ✓ `simulator-gcp/dns.go:268::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:287::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:301::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/rrsets` | ✓ `simulator-gcp/dns.go:384::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:430::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:447::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}` | ✓ `simulator-gcp/dns.go:475::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:516::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes/{change}` | ✓ `simulator-gcp/dns.go:579::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/changes` | ✓ `simulator-gcp/dns.go:595::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:637::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/managedZones/{zone}` | ✓ `simulator-gcp/dns.go:642::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys` | ✓ `simulator-gcp/dns.go:647::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys/{dnsKeyId}` | ✓ `simulator-gcp/dns.go:674::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations` | ✓ `simulator-gcp/dns.go:693::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/managedZones/{zone}/operations/{operation}` | ✓ `simulator-gcp/dns.go:729::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}` | ✓ `simulator-gcp/dns.go:746::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:758::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies` | ✓ `simulator-gcp/dns.go:782::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:808::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:819::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:847::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/policies/{policy}` | ✓ `simulator-gcp/dns.go:850::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:858::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies` | ✓ `simulator-gcp/dns.go:882::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:905::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:916::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:950::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}` | ✓ `simulator-gcp/dns.go:953::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:958::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules` | ✓ `simulator-gcp/dns.go:984::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1012::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1054::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}` | ✓ `simulator-gcp/dns.go:1057::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
