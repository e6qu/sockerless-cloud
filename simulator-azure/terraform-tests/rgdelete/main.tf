terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.12.0"
    }
  }
}

# Run by TestTerraformResourceGroupDeleteTakesItsResources. The identity names
# its group literally, so `terraform import` can look for it once the group is
# gone.

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

  features {
    resource_group {
      prevent_deletion_if_contains_resources = false
    }
  }
}

resource "azurerm_resource_group" "rgdelete" {
  name     = "tf-azrm-rgdelete-rg"
  location = "eastus"
}

resource "azurerm_user_assigned_identity" "left_behind" {
  name                = "tf-azrm-rgdelete-uai"
  resource_group_name = "tf-azrm-rgdelete-rg"
  location            = "eastus"

  depends_on = [azurerm_resource_group.rgdelete]
}

output "identity_id" {
  value = azurerm_user_assigned_identity.left_behind.id
}

output "group_id" {
  value = azurerm_resource_group.rgdelete.id
}
