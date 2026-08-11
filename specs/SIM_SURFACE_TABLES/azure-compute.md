# Sim surface — azure-compute

Surface registered in `simulator-azure/compute.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/vmSizes` | ✓ `simulator-azure/compute.go:222::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/skus` | ✓ `simulator-azure/compute.go:226::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/virtualMachines` | ✓ `simulator-azure/compute.go:1182::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Issue #266 closed the Azure VM lifecycle gap. `Microsoft.Network/networkInterfaces`, `Microsoft.Network/publicIPAddresses`, and `Microsoft.Compute/virtualMachines` are covered by `simulator-azure/sdk-tests/compute_test.go`, `simulator-azure/cli-tests/compute_test.go`, and `simulator-azure/terraform-tests/main.tf` through `azurerm_network_interface` and `azurerm_linux_virtual_machine`.

Issue #263 closed the Azure managed load-balancer gap for `Microsoft.Network/loadBalancers`. The simulator implements Load Balancer create/get/list/delete plus frontend IP configurations, backend address pools, probes, and load-balancing rules, including the child-resource paths used by the official clients and provider. Coverage uses `armnetwork` Load Balancer SDK coverage in `simulator-azure/sdk-tests/network_test.go`, Azure CLI `az rest` coverage in `simulator-azure/cli-tests/loadbalancer_test.go`, and Terraform `azurerm_public_ip`, `azurerm_lb`, `azurerm_lb_backend_address_pool`, `azurerm_lb_probe`, and `azurerm_lb_rule` resources in `simulator-azure/terraform-tests/main.tf`.

Issue #279 closed the Azure NAT/public-IP parity pass. `Microsoft.Network/publicIPPrefixes`, NAT Gateway list/read behavior, subnet NAT Gateway association persistence, and NAT Gateway subnet back-references are covered by `simulator-azure/sdk-tests/network_test.go`, `simulator-azure/cli-tests/nat_test.go`, and `simulator-azure/terraform-tests/main.tf` through `azurerm_public_ip_prefix`, `azurerm_nat_gateway`, `azurerm_nat_gateway_public_ip_prefix_association`, and `azurerm_subnet_nat_gateway_association`.
<!-- HAND-WRITTEN END -->
