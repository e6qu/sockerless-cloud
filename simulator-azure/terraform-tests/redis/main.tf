terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.12.0"
    }
  }
}

# Run by TestTerraformRedisCacheServesItsHostName: a Standard Azure Cache for
# Redis whose engine answers at its hostname, over TLS on ssl_port and in
# plaintext on port, to its access keys.

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

resource "azurerm_resource_group" "redis" {
  name     = "tf-azrm-redis-engine-rg"
  location = "eastus"
}

resource "azurerm_redis_cache" "engine" {
  name                 = "tfazrmredisengine"
  resource_group_name  = azurerm_resource_group.redis.name
  location             = azurerm_resource_group.redis.location
  capacity             = 1
  family               = "C"
  sku_name             = "Standard"
  minimum_tls_version  = "1.2"
  non_ssl_port_enabled = true

  redis_configuration {
    maxmemory_policy = "allkeys-lru"
  }
}

output "hostname" {
  value = azurerm_redis_cache.engine.hostname
}

output "ssl_port" {
  value = tostring(azurerm_redis_cache.engine.ssl_port)
}

output "port" {
  value = tostring(azurerm_redis_cache.engine.port)
}

output "primary_access_key" {
  value     = azurerm_redis_cache.engine.primary_access_key
  sensitive = true
}

output "secondary_access_key" {
  value     = azurerm_redis_cache.engine.secondary_access_key
  sensitive = true
}
