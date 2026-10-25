terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

# Standalone configuration run by TestTerraformFrontEndForwardedHeaders: a
# Linux custom-container web app and a container app whose containers answer
# every request with the request headers they received.

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

# busybox nc runs the script on each connection: it collects the header lines
# that follow the request line, then answers with them as a body its
# Content-Length delimits.
locals {
  echo_headers = "set -f; f=$(mktemp); read -r l; while read -r l && [ $${#l} -gt 1 ]; do echo $l >> $f; done; printf 'HTTP/1.1 200 OK\\r\\nContent-Length: %s\\r\\nConnection: close\\r\\n\\r\\n' $(wc -c < $f); cat $f; rm $f"
}

resource "azurerm_resource_group" "xff" {
  name     = "tf-azrm-xff-rg"
  location = "eastus"
}

resource "azurerm_service_plan" "xff" {
  name                = "tf-azrm-xff-sp"
  resource_group_name = azurerm_resource_group.xff.name
  location            = azurerm_resource_group.xff.location
  os_type             = "Linux"
  sku_name            = "B1"
}

resource "azurerm_linux_web_app" "xff" {
  name                = "tf-azrm-xff-app"
  resource_group_name = azurerm_resource_group.xff.name
  location            = azurerm_resource_group.xff.location
  service_plan_id     = azurerm_service_plan.xff.id

  app_settings = {
    WEBSITES_PORT = "8080"
  }

  # TLS mutual authentication: the front end asks a client for a certificate
  # and forwards it as X-ARR-ClientCert; Optional lets a client without one
  # through, and the excluded path is never asked.
  client_certificate_enabled         = true
  client_certificate_mode            = "Optional"
  client_certificate_exclusion_paths = "/public"

  site_config {
    app_command_line = "nc -lk -p 8080 -e sh -c \"${local.echo_headers}\""

    application_stack {
      docker_image_name   = "library/alpine:latest"
      docker_registry_url = "https://mirror.gcr.io"
    }
  }
}

resource "azurerm_log_analytics_workspace" "xff" {
  name                = "tf-azrm-xff-law"
  resource_group_name = azurerm_resource_group.xff.name
  location            = azurerm_resource_group.xff.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}

resource "azurerm_container_app_environment" "xff" {
  name                       = "tf-azrm-xff-cae"
  resource_group_name        = azurerm_resource_group.xff.name
  location                   = azurerm_resource_group.xff.location
  logs_destination           = "log-analytics"
  log_analytics_workspace_id = azurerm_log_analytics_workspace.xff.id
}

resource "azurerm_container_app" "xff" {
  name                         = "tf-azrm-xff-ca"
  container_app_environment_id = azurerm_container_app_environment.xff.id
  resource_group_name          = azurerm_resource_group.xff.name
  revision_mode                = "Single"

  ingress {
    external_enabled        = true
    target_port             = 8080
    client_certificate_mode = "accept"
    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }

  template {
    min_replicas = 1
    max_replicas = 1

    container {
      name    = "main"
      image   = "mirror.gcr.io/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["nc", "-lk", "-p", "8080", "-e", "sh", "-c", local.echo_headers]
    }
  }
}

output "app_hostname" {
  value = azurerm_linux_web_app.xff.default_hostname
}

output "container_app_fqdn" {
  value = azurerm_container_app.xff.ingress[0].fqdn
}

output "app_client_certificate_mode" {
  value = azurerm_linux_web_app.xff.client_certificate_mode
}

output "container_app_client_certificate_mode" {
  value = azurerm_container_app.xff.ingress[0].client_certificate_mode
}
