terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

# The operator's configuration, run by
# TestTerraformRoleAssignmentsGateTheProvider as the bootstrap service
# principal: a resource group, a storage account that refuses its keys, and the
# role assignments the workload's service principal is granted.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "principal_id" {
  description = "Object ID of the workload's service principal"
  type        = string
}

variable "role" {
  description = "Role the workload holds on the resource group"
  type        = string
}

variable "blob_data_role" {
  description = "Whether the workload holds Storage Blob Data Contributor on the account"
  type        = bool
}

provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")

  resource_provider_registrations = "none"
  storage_use_azuread             = true

  features {}
}

resource "azurerm_resource_group" "rbac" {
  name     = "tf-azrm-rbac-rg"
  location = "eastus"
}

resource "azurerm_storage_account" "rbac" {
  name                      = "tfazrmrbacsa"
  resource_group_name       = azurerm_resource_group.rbac.name
  location                  = azurerm_resource_group.rbac.location
  account_tier              = "Standard"
  account_replication_type  = "LRS"
  shared_access_key_enabled = false
}

resource "azurerm_storage_container" "rbac" {
  name               = "data"
  storage_account_id = azurerm_storage_account.rbac.id
}

resource "azurerm_role_assignment" "group" {
  scope                = azurerm_resource_group.rbac.id
  role_definition_name = var.role
  principal_id         = var.principal_id
  principal_type       = "ServicePrincipal"
}

resource "azurerm_role_assignment" "blob_data" {
  count                = var.blob_data_role ? 1 : 0
  scope                = azurerm_storage_account.rbac.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = var.principal_id
  principal_type       = "ServicePrincipal"
}

output "container_id" {
  value = azurerm_storage_container.rbac.id
}
