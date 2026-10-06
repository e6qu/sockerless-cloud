terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.8.0"
    }
  }
}

# Standalone App Service deployment-slot configuration, run by
# TestTerraformWebAppSlotApplyDestroy: a Linux custom-container web app and its
# staging slot each run their own container, and azurerm_web_app_active_slot
# swaps the slot into production. The startup commands travel with the swap,
# so the configuration ignores them after creation, as a configuration that
# swaps slots does; the STICKY app setting and the DB connection string stay
# with their slot. Each container answers with its content and the
# CUSTOMCONNSTR_DB variable App Service puts the DB connection string in.

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

resource "azurerm_resource_group" "slots" {
  name     = "tf-azrm-slots-rg"
  location = "eastus"
}

resource "azurerm_service_plan" "slots" {
  name                = "tf-azrm-slots-sp"
  resource_group_name = azurerm_resource_group.slots.name
  location            = azurerm_resource_group.slots.location
  os_type             = "Linux"
  sku_name            = "S1"
}

# busybox nc answers each connection on WEBSITES_PORT after reading the
# request's headers, with the line that names the content.
locals {
  serve = "nc -lk -p 8080 -e sh -c \"while read -r l && [ $${#l} -gt 1 ]; do :; done; printf 'HTTP/1.1 200 OK\\r\\nConnection: close\\r\\n\\r\\n'; echo %s\""
}

resource "azurerm_linux_web_app" "slots" {
  name                = "tf-azrm-slots-app"
  resource_group_name = azurerm_resource_group.slots.name
  location            = azurerm_resource_group.slots.location
  service_plan_id     = azurerm_service_plan.slots.id

  app_settings = {
    WEBSITES_PORT = "8080"
    STICKY        = "production"
  }

  connection_string {
    name  = "DB"
    type  = "Custom"
    value = "production-db"
  }

  sticky_settings {
    app_setting_names       = ["STICKY"]
    connection_string_names = ["DB"]
  }

  site_config {
    app_command_line = format(local.serve, "production-content $${CUSTOMCONNSTR_DB}")

    application_stack {
      docker_image_name   = "docker/library/alpine:latest"
      docker_registry_url = "https://public.ecr.aws"
    }
  }

  lifecycle {
    ignore_changes = [site_config[0].app_command_line]
  }
}

resource "azurerm_linux_web_app_slot" "staging" {
  name           = "staging"
  app_service_id = azurerm_linux_web_app.slots.id

  app_settings = {
    WEBSITES_PORT = "8080"
    STICKY        = "staging"
  }

  connection_string {
    name  = "DB"
    type  = "Custom"
    value = "staging-db"
  }

  site_config {
    app_command_line = format(local.serve, "staging-content $${CUSTOMCONNSTR_DB}")

    application_stack {
      docker_image_name   = "docker/library/alpine:latest"
      docker_registry_url = "https://public.ecr.aws"
    }
  }

  lifecycle {
    ignore_changes = [site_config[0].app_command_line]
  }
}

resource "azurerm_web_app_active_slot" "staging" {
  slot_id = azurerm_linux_web_app_slot.staging.id
}

output "app_hostname" {
  value = azurerm_linux_web_app.slots.default_hostname
}

output "slot_hostname" {
  value = azurerm_linux_web_app_slot.staging.default_hostname
}

output "active_slot_id" {
  value = azurerm_web_app_active_slot.staging.slot_id
}

output "last_successful_swap" {
  value = azurerm_web_app_active_slot.staging.last_successful_swap
}
