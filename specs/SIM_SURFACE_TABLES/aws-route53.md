# Sim surface — aws-route53

Surface registered in `simulator-aws/route53.go`. Rows below are the Route 53 REST XML ops the AWS simulator currently registers.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /2013-04-01/hostedzone` | ✓ `simulator-aws/route53.go:405::handleR53CreateHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzone` | ✓ `simulator-aws/route53.go:514::handleR53ListHostedZones` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/hostedzonesbyname` | ✓ `simulator-aws/route53.go:534::handleR53ListHostedZonesByName` | ✓ `simulator-aws/sdk-tests/route53_test.go::TestRoute53ListHostedZonesByName` | n/a | ✓ SDK test covers `MaxItems`, `NextDNSName`, and `NextHostedZoneId` | terraform-provider-aws v6.47.0 uses `ListHostedZones` for current `aws_route53_zone`/`aws_route53_zones` lookup flows, so this exact op has no current provider resource path. |
| `GET /2013-04-01/hostedzone/{id}` | ✓ `simulator-aws/route53.go:475::handleR53GetHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2013-04-01/hostedzone/{id}` | ✓ `simulator-aws/route53.go:489::handleR53DeleteHostedZone` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2013-04-01/hostedzone/{id}/rrset` | ✓ `simulator-aws/route53.go:616::handleR53ChangeRRSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2013-04-01/hostedzone/{id}/rrset/` | ✓ `simulator-aws/route53.go:616::handleR53ChangeRRSets` | n/a | n/a | n/a | AWS CLI path variant with trailing slash. |
| `GET /2013-04-01/hostedzone/{id}/rrset` | ✓ `simulator-aws/route53.go:710::handleR53ListRRSets` | ✓ `simulator-aws/sdk-tests/route53_test.go::TestRoute53ListResourceRecordSetsSortedCursor` | ✓ (direct; see coverage matrix) | ✓ SDK test covers reversed-label ordering, start-name/type cursoring, `MaxItems`, `IsTruncated`, `NextRecordName`, and `NextRecordType` | |
| `GET /2013-04-01/hostedzone/{id}/rrset/` | ✓ `simulator-aws/route53.go:710::handleR53ListRRSets` | n/a | n/a | n/a | AWS CLI path variant with trailing slash. |
| `GET /2013-04-01/change/{id}` | ✓ `simulator-aws/route53.go:832::handleR53GetChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2013-04-01/tags/{resourceType}/{resourceId}` | ✓ `simulator-aws/route53.go:361::handleR53ListTagsForResource` | n/a | ✓ (direct; see coverage matrix) | n/a | Covered by terraform-provider-aws Route 53 zone tag reads when tags are configured. |
| `POST /2013-04-01/tags/{resourceType}/{resourceId}` | ✓ `simulator-aws/route53.go:374::handleR53ChangeTagsForResource` | n/a | ✓ (direct; see coverage matrix) | n/a | Covered by terraform-provider-aws Route 53 zone tag writes when tags are configured. |

## Coverage status

- Route 53 hosted-zone and record lifecycle coverage uses the official AWS SDK, AWS CLI, and terraform-provider-aws resources.
- Issue #296 / BUG-1238 closed the `ListResourceRecordSets` ordering gap: record sets are listed by Route 53's reversed-label DNS-name order, then type, then set identifier, and the response honors `maxitems` with continuation cursor fields.
- `ListHostedZonesByName` was added for issue #291 / BUG-1233 and is covered through the official AWS SDK and AWS CLI surfaces. The pinned Terraform provider source was checked at v6.47.0; its current Route 53 zone data sources use `ListHostedZones`, not `ListHostedZonesByName`, so there is no Terraform provider call path for this exact API operation today.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
