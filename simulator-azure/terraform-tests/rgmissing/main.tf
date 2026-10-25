terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

# Missing resource group configuration, run by
# TestTerraformMissingResourceGroupRefusesApply: the identity names a group no
# configuration creates, so Azure Resource Manager refuses its create with
# ResourceGroupNotFound.

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

resource "azurerm_user_assigned_identity" "orphan" {
  name                = "tf-azrm-orphan-uai"
  resource_group_name = "tf-azrm-never-created-rg"
  location            = "eastus"
}
