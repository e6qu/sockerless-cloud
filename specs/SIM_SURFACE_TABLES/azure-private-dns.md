# Sim surface — azure-private-dns

Surface registered in `simulator-azure/dns.go` under the `registerPrivateDNS` function. Covers Azure Private DNS zones, A-records, generic record types (AAAA/CNAME/MX/PTR/SRV/TXT), and virtual network links.

`armBase` = `/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network`

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

### Private DNS zones

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `PUT {armBase}/privateDnsZones/{zoneName}` | ✓ `simulator-azure/dns.go:152::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | upsert; auto-adds SOA record |
| `GET {armBase}/privateDnsZones` | ✓ `simulator-azure/dns.go:223::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}` | ✓ `simulator-azure/dns.go:234::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE {armBase}/privateDnsZones/{zoneName}` | ✓ `simulator-azure/dns.go:253::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

### A records

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET {armBase}/privateDnsZones/{zoneName}/SOA/{recordName}` | ✓ `simulator-azure/dns.go:276::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | auto-created with zone |
| `PUT {armBase}/privateDnsZones/{zoneName}/A/{recordName}` | ✓ `simulator-azure/dns.go:296::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/A/{recordName}` | ✓ `simulator-azure/dns.go:353::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE {armBase}/privateDnsZones/{zoneName}/A/{recordName}` | ✓ `simulator-azure/dns.go:373::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/A` | ✓ `simulator-azure/dns.go:398::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

### Generic record types (AAAA, CNAME, MX, PTR, SRV, TXT)

Registered via a loop over `[]string{"AAAA","CNAME","MX","PTR","SRV","TXT"}` at `simulator-azure/dns.go:421`.

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `PUT {armBase}/privateDnsZones/{zoneName}/{recordType}/{recordName}` | ✓ `simulator-azure/dns.go:423::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/{recordType}/{recordName}` | ✓ `simulator-azure/dns.go:477::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE {armBase}/privateDnsZones/{zoneName}/{recordType}/{recordName}` | ✓ `simulator-azure/dns.go:497::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/{recordType}` | ✓ `simulator-azure/dns.go:521::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

### Virtual network links

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `PUT {armBase}/privateDnsZones/{zoneName}/virtualNetworkLinks/{linkName}` | ✓ `simulator-azure/dns.go:543::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/virtualNetworkLinks` | ✓ `simulator-azure/dns.go:578::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET {armBase}/privateDnsZones/{zoneName}/virtualNetworkLinks/{linkName}` | ✓ `simulator-azure/dns.go:591::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE {armBase}/privateDnsZones/{zoneName}/virtualNetworkLinks/{linkName}` | ✓ `simulator-azure/dns.go:609::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`.

<!-- HAND-WRITTEN BEGIN -->
PR #388 (BUG-1318/1319) added A-record CRUD (`PUT/GET/DELETE/LIST /privateDnsZones/{zone}/A/{name}`) via `armprivatedns.RecordSetsClient`. CLI coverage: `simulator-azure/cli-tests/dns_test.go` (`TestPrivateDNS_RecordSetListAndDelete`). SDK coverage: `simulator-azure/sdk-tests/dns_private_test.go` (`TestPrivateDNS_RecordSetsCRUD`, `TestPrivateDNS_RecordSetsUpdate`). Terraform coverage: `simulator-azure/terraform-tests/main.tf` (`azurerm_private_dns_a_record`). Zone CRUD, virtual network links, and generic record types (AAAA/CNAME/MX/PTR/SRV/TXT) were added in an earlier phase.
<!-- HAND-WRITTEN END -->
