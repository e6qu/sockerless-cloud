# Sim surface — aws-elbv2

Surface registered in `simulator-aws/elbv2.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CreateLoadBalancer` | ✓ `simulator-aws/elbv2.go:183::handleELBv2CreateLoadBalancer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeLoadBalancers` | ✓ `simulator-aws/elbv2.go:184::handleELBv2DescribeLoadBalancers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteLoadBalancer` | ✓ `simulator-aws/elbv2.go:185::handleELBv2DeleteLoadBalancer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyLoadBalancerAttributes` | ✓ `simulator-aws/elbv2.go:186::handleELBv2ModifyLoadBalancerAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeLoadBalancerAttributes` | ✓ `simulator-aws/elbv2.go:187::handleELBv2DescribeLoadBalancerAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCapacityReservation` | ✓ `simulator-aws/elbv2.go:188::handleELBv2DescribeCapacityReservation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetSecurityGroups` | ✓ `simulator-aws/elbv2.go:189::handleELBv2SetSecurityGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetSubnets` | ✓ `simulator-aws/elbv2.go:190::handleELBv2SetSubnets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetIpAddressType` | ✓ `simulator-aws/elbv2.go:191::handleELBv2SetIpAddressType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateTargetGroup` | ✓ `simulator-aws/elbv2.go:193::handleELBv2CreateTargetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTargetGroups` | ✓ `simulator-aws/elbv2.go:194::handleELBv2DescribeTargetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteTargetGroup` | ✓ `simulator-aws/elbv2.go:195::handleELBv2DeleteTargetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyTargetGroup` | ✓ `simulator-aws/elbv2.go:196::handleELBv2ModifyTargetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyTargetGroupAttributes` | ✓ `simulator-aws/elbv2.go:197::handleELBv2ModifyTargetGroupAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTargetGroupAttributes` | ✓ `simulator-aws/elbv2.go:198::handleELBv2DescribeTargetGroupAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RegisterTargets` | ✓ `simulator-aws/elbv2.go:199::handleELBv2RegisterTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeregisterTargets` | ✓ `simulator-aws/elbv2.go:200::handleELBv2DeregisterTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTargetHealth` | ✓ `simulator-aws/elbv2.go:201::handleELBv2DescribeTargetHealth` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateListener` | ✓ `simulator-aws/elbv2.go:203::handleELBv2CreateListener` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeListeners` | ✓ `simulator-aws/elbv2.go:204::handleELBv2DescribeListeners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeListenerAttributes` | ✓ `simulator-aws/elbv2.go:205::handleELBv2DescribeListenerAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyListenerAttributes` | ✓ `simulator-aws/elbv2.go:206::handleELBv2ModifyListenerAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteListener` | ✓ `simulator-aws/elbv2.go:207::handleELBv2DeleteListener` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTags` | ✓ `simulator-aws/elbv2.go:209::handleELBv2AddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTags` | ✓ `simulator-aws/elbv2.go:210::handleELBv2RemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTags` | ✓ `simulator-aws/elbv2.go:211::handleELBv2DescribeTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAccountLimits` | ○ `simulator-aws/elbv2.go:212::handleELBv2DescribeAccountLimits` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateRule` | ✓ `simulator-aws/elbv2_rules.go:42::handleELBv2CreateRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeRules` | ✓ `simulator-aws/elbv2_rules.go:43::handleELBv2DescribeRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyRule` | ✓ `simulator-aws/elbv2_rules.go:44::handleELBv2ModifyRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteRule` | ✓ `simulator-aws/elbv2_rules.go:45::handleELBv2DeleteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetRulePriorities` | ✓ `simulator-aws/elbv2_rules.go:46::handleELBv2SetRulePriorities` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyListener` | ✓ `simulator-aws/elbv2_rules.go:47::handleELBv2ModifyListener` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddListenerCertificates` | ✓ `simulator-aws/elbv2_rules.go:48::handleELBv2AddListenerCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveListenerCertificates` | ✓ `simulator-aws/elbv2_rules.go:49::handleELBv2RemoveListenerCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeListenerCertificates` | ✓ `simulator-aws/elbv2_rules.go:50::handleELBv2DescribeListenerCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateTrustStore` | ✓ `simulator-aws/elbv2_truststore.go:57::handleELBv2CreateTrustStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTrustStores` | ✓ `simulator-aws/elbv2_truststore.go:58::handleELBv2DescribeTrustStores` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyTrustStore` | ✓ `simulator-aws/elbv2_truststore.go:59::handleELBv2ModifyTrustStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteTrustStore` | ✓ `simulator-aws/elbv2_truststore.go:60::handleELBv2DeleteTrustStore` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetTrustStoreCaCertificatesBundle` | ✓ `simulator-aws/elbv2_truststore.go:61::handleELBv2GetTrustStoreCaCertificatesBundle` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTrustStoreAssociations` | ✓ `simulator-aws/elbv2_truststore.go:62::handleELBv2DescribeTrustStoreAssociations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteSharedTrustStoreAssociation` | ✓ `simulator-aws/elbv2_truststore.go:63::handleELBv2DeleteSharedTrustStoreAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTrustStoreRevocations` | ✓ `simulator-aws/elbv2_truststore.go:64::handleELBv2AddTrustStoreRevocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTrustStoreRevocations` | ✓ `simulator-aws/elbv2_truststore.go:65::handleELBv2RemoveTrustStoreRevocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTrustStoreRevocations` | ✓ `simulator-aws/elbv2_truststore.go:66::handleELBv2DescribeTrustStoreRevocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetTrustStoreRevocationContent` | ✓ `simulator-aws/elbv2_truststore.go:67::handleELBv2GetTrustStoreRevocationContent` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeSSLPolicies` | ○ `simulator-aws/elbv2_truststore.go:69::handleELBv2DescribeSSLPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetResourcePolicy` | ✓ `simulator-aws/elbv2_truststore.go:70::handleELBv2GetResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCapacityReservation` | ✓ `simulator-aws/elbv2_truststore.go:71::handleELBv2ModifyCapacityReservation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyIpPools` | ✓ `simulator-aws/elbv2_truststore.go:72::handleELBv2ModifyIpPools` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Issue #263 closed the AWS managed load-balancer gap for the ELBv2 public Query API. The implemented slice covers application/network load balancer lifecycle, target groups, listeners, target registration/health, mutable load-balancer/target-group/listener attributes, tagging, account limits, and the provider-read `DescribeCapacityReservation` operation. Coverage uses the official `elasticloadbalancingv2` Go SDK in `simulator-aws/sdk-tests/elbv2_test.go`, AWS CLI `elbv2` lifecycle coverage in `simulator-aws/cli-tests/elbv2_test.go`, and Terraform `aws_lb`, `aws_lb_target_group`, and `aws_lb_listener` resources in `simulator-aws/terraform-tests/main.tf`.
<!-- HAND-WRITTEN END -->
