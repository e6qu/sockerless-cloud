# Sim surface — aws-route53

Surface registered in `simulator-aws/route53.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /2013-04-01/hostedzone` | ✓ `simulator-aws/route53.go:327::handleR53CreateHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzone` | ✓ `simulator-aws/route53.go:328::handleR53ListHostedZones` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzonesbyname` | ✓ `simulator-aws/route53.go:329::handleR53ListHostedZonesByName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzone/{id}` | ✓ `simulator-aws/route53.go:330::handleR53GetHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2013-04-01/hostedzone/{id}` | ✓ `simulator-aws/route53.go:331::handleR53DeleteHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2013-04-01/hostedzone/{id}/rrset` | ✓ `simulator-aws/route53.go:332::handleR53ChangeRRSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2013-04-01/hostedzone/{id}/rrset/` | ✓ `simulator-aws/route53.go:333::handleR53ChangeRRSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzone/{id}/rrset` | ✓ `simulator-aws/route53.go:334::handleR53ListRRSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzone/{id}/rrset/` | ✓ `simulator-aws/route53.go:335::handleR53ListRRSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/change/{id}` | ✓ `simulator-aws/route53.go:336::handleR53GetChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/tags/{resourceType}/{resourceId}` | ✓ `simulator-aws/route53.go:339::handleR53ListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2013-04-01/tags/{resourceType}/{resourceId}` | ✓ `simulator-aws/route53.go:340::handleR53ChangeTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
