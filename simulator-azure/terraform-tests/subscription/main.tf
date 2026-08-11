terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

# Standalone Microsoft.Subscription alias-API configuration, run by
# TestTerraformSubscriptionApplyDestroy as its own CI shard: each
# azurerm_subscription create and cancel sits through the provider's fixed 60s
# StateChangeConf delay plus four 10s confirmation polls, too slow for the
# shared stack's CI budget. The provider wiring is identical to the shared
# stack's — same simulator endpoint coordinate, same service principal.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")

  resource_provider_registrations = "none"

  features {}
}

# azurerm_subscription supports two creation modes; both run here. Creation
# PUTs the alias with a billing scope and polls the alias to Succeeded;
# adoption attaches an alias to an existing subscription id. Destroy deletes
# the alias and cancels the subscription (the provider then waits for the
# Disabled state).

resource "azurerm_subscription" "created" {
  alias             = "tf-test-sub-alias"
  subscription_name = "TF Created Subscription"
  billing_scope_id  = "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment"
  workload          = "Production"
}

resource "azurerm_subscription" "adopted" {
  alias             = "tf-test-sub-alias-adopted"
  subscription_name = "Simulator Subscription"
  subscription_id   = "00000000-0000-0000-0000-0000000000ad"
}

output "azrm_subscription_created_alias_id" {
  value = azurerm_subscription.created.id
}

output "azrm_subscription_created_subscription_id" {
  value = azurerm_subscription.created.subscription_id
}

output "azrm_subscription_created_tenant_id" {
  value = azurerm_subscription.created.tenant_id
}

output "azrm_subscription_adopted_alias_id" {
  value = azurerm_subscription.adopted.id
}

output "azrm_subscription_adopted_subscription_id" {
  value = azurerm_subscription.adopted.subscription_id
}
