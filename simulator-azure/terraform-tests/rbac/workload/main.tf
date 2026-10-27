terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

# The workload's configuration, run by
# TestTerraformRoleAssignmentsGateTheProvider as a service principal holding
# only the roles the operator's configuration assigns it.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "client_id" {
  description = "Application (client) ID of the workload's service principal"
  type        = string
}

variable "client_secret" {
  description = "Client secret of the workload's service principal"
  type        = string
  sensitive   = true
}

variable "container_id" {
  description = "Resource ID of the blob container the workload writes to"
  type        = string
}

provider "azurerm" {
  client_id       = var.client_id
  client_secret   = var.client_secret
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")

  resource_provider_registrations = "none"
  storage_use_azuread             = true

  features {}
}

data "azurerm_resource_group" "rbac" {
  name = "tf-azrm-rbac-rg"
}

resource "azurerm_user_assigned_identity" "workload" {
  name                = "tf-azrm-rbac-uai"
  resource_group_name = data.azurerm_resource_group.rbac.name
  location            = data.azurerm_resource_group.rbac.location
}

resource "azurerm_storage_blob" "workload" {
  name                 = "written-by-the-workload.txt"
  storage_container_id = var.container_id
  type                 = "Block"
  source_content       = "written with a Microsoft Entra token"
}
