terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.9.0"
    }
    azuread = {
      source  = "hashicorp/azuread"
      version = "3.10.0"
    }
  }
}

# Standalone configuration run by
# TestTerraformWebAppKeyVaultCertificateAndContainerAppSecrets: one vault,
# whose access policies grant three readers each by its own
# azurerm_key_vault_access_policy, and the three readers.
#
#   - App Service imports a certificate from the vault as its first-party
#     service principal, "Microsoft Azure App Service", read from Microsoft
#     Graph by its application ID.
#   - A container app with system-assigned and user-assigned identities holds
#     a secret that references the vault, read as the user-assigned identity,
#     and its ingress answers with the secret's value and the token its
#     identity endpoint issues that identity.
#   - A Container Apps job reads the same secret as the same identity.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "certificate_pem" {
  description = "PEM bundle of the certificate App Service imports and its key"
  type        = string
  sensitive   = true
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

provider "azuread" {
  client_id     = "test-client-id"
  client_secret = "test-client-secret"
  tenant_id     = "11111111-1111-1111-1111-111111111111"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")
}

data "azurerm_client_config" "current" {}

data "azuread_service_principal" "app_service" {
  client_id = "abfa0a7c-a6b6-4736-8310-5855508787cd"
}

resource "azurerm_resource_group" "kv" {
  name     = "tf-azrm-kvconsumers-rg"
  location = "eastus"
}

resource "azurerm_user_assigned_identity" "reader" {
  name                = "tf-azrm-kvconsumers-reader"
  resource_group_name = azurerm_resource_group.kv.name
  location            = azurerm_resource_group.kv.location
}

resource "azurerm_key_vault" "kv" {
  name                       = "tf-azrm-kvconsumers"
  resource_group_name        = azurerm_resource_group.kv.name
  location                   = azurerm_resource_group.kv.location
  tenant_id                  = "11111111-1111-1111-1111-111111111111"
  sku_name                   = "standard"
  rbac_authorization_enabled = false
  purge_protection_enabled   = false
  soft_delete_retention_days = 7
}

resource "azurerm_key_vault_access_policy" "writer" {
  key_vault_id       = azurerm_key_vault.kv.id
  tenant_id          = data.azurerm_client_config.current.tenant_id
  object_id          = data.azurerm_client_config.current.object_id
  secret_permissions = ["Get", "List", "Set", "Delete", "Purge"]
}

resource "azurerm_key_vault_access_policy" "app_service" {
  key_vault_id       = azurerm_key_vault.kv.id
  tenant_id          = "11111111-1111-1111-1111-111111111111"
  object_id          = data.azuread_service_principal.app_service.object_id
  secret_permissions = ["Get"]
}

resource "azurerm_key_vault_access_policy" "reader" {
  key_vault_id       = azurerm_key_vault.kv.id
  tenant_id          = "11111111-1111-1111-1111-111111111111"
  object_id          = azurerm_user_assigned_identity.reader.principal_id
  secret_permissions = ["Get"]
}

resource "azurerm_key_vault_secret" "db" {
  name         = "db-password"
  value        = "hunter2"
  key_vault_id = azurerm_key_vault.kv.id

  depends_on = [azurerm_key_vault_access_policy.writer]
}

resource "azurerm_key_vault_secret" "certificate" {
  name         = "site-certificate"
  value        = var.certificate_pem
  content_type = "application/x-pem-file"
  key_vault_id = azurerm_key_vault.kv.id

  depends_on = [azurerm_key_vault_access_policy.writer]
}

resource "azurerm_app_service_certificate" "kv" {
  name                = "tf-azrm-kvconsumers-cert"
  resource_group_name = azurerm_resource_group.kv.name
  location            = azurerm_resource_group.kv.location
  key_vault_secret_id = azurerm_key_vault_secret.certificate.id

  depends_on = [azurerm_key_vault_access_policy.app_service]
}

resource "azurerm_log_analytics_workspace" "kv" {
  name                = "tf-azrm-kvconsumers-law"
  resource_group_name = azurerm_resource_group.kv.name
  location            = azurerm_resource_group.kv.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}

resource "azurerm_container_app_environment" "kv" {
  name                       = "tf-azrm-kvconsumers-cae"
  resource_group_name        = azurerm_resource_group.kv.name
  location                   = azurerm_resource_group.kv.location
  logs_destination           = "log-analytics"
  log_analytics_workspace_id = azurerm_log_analytics_workspace.kv.id
}

# busybox nc answers each connection on 8080 after reading the request's
# headers, with the secret's value and then the token the app's identity
# endpoint issues its user-assigned identity, in a body its Content-Length
# delimits.
resource "azurerm_container_app" "kv" {
  name                         = "tf-azrm-kvconsumers-ca"
  container_app_environment_id = azurerm_container_app_environment.kv.id
  resource_group_name          = azurerm_resource_group.kv.name
  revision_mode                = "Single"

  identity {
    type         = "SystemAssigned, UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.reader.id]
  }

  secret {
    name                = "db"
    key_vault_secret_id = azurerm_key_vault_secret.db.versionless_id
    identity            = azurerm_user_assigned_identity.reader.id
  }

  ingress {
    external_enabled = true
    target_port      = 8080
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
      image   = "public.ecr.aws/docker/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["sh", "-c", "nc -lk -p 8080 -e sh -c 'while read -r l && [ $${#l} -gt 1 ]; do :; done; b=$(echo $DB; wget -qO- --header X-IDENTITY-HEADER:$IDENTITY_HEADER \"$IDENTITY_ENDPOINT?api-version=2019-08-01&resource=https://vault.azure.net&client_id=$READER_CLIENT_ID\"); printf \"HTTP/1.1 200 OK\\r\\nContent-Length: %d\\r\\nConnection: close\\r\\n\\r\\n%s\" $${#b} \"$b\"'"]

      env {
        name        = "DB"
        secret_name = "db"
      }

      env {
        name  = "READER_CLIENT_ID"
        value = azurerm_user_assigned_identity.reader.client_id
      }
    }
  }

  depends_on = [azurerm_key_vault_access_policy.reader]
}

resource "azurerm_container_app_job" "kv" {
  name                         = "tf-azrm-kvconsumers-job"
  container_app_environment_id = azurerm_container_app_environment.kv.id
  resource_group_name          = azurerm_resource_group.kv.name
  location                     = azurerm_resource_group.kv.location

  replica_timeout_in_seconds = 60
  replica_retry_limit        = 1

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.reader.id]
  }

  secret {
    name                = "db"
    key_vault_secret_id = azurerm_key_vault_secret.db.versionless_id
    identity            = azurerm_user_assigned_identity.reader.id
  }

  manual_trigger_config {
    parallelism              = 1
    replica_completion_count = 1
  }

  template {
    container {
      name    = "main"
      image   = "public.ecr.aws/docker/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["sh", "-c", "test \"$DB\" = hunter2"]

      env {
        name        = "DB"
        secret_name = "db"
      }
    }
  }

  depends_on = [azurerm_key_vault_access_policy.reader]
}

output "app_service_principal_id" {
  value = data.azuread_service_principal.app_service.object_id
}

output "certificate_thumbprint" {
  value = azurerm_app_service_certificate.kv.thumbprint
}

output "app_fqdn" {
  value = azurerm_container_app.kv.ingress[0].fqdn
}

output "app_principal_id" {
  value = azurerm_container_app.kv.identity[0].principal_id
}

output "reader_principal_id" {
  value = azurerm_user_assigned_identity.reader.principal_id
}

output "job_identity_ids" {
  value = azurerm_container_app_job.kv.identity[0].identity_ids
}

# Each azurerm_key_vault_access_policy adds its own policy and leaves the
# others as they are.
data "azurerm_key_vault" "granted" {
  name                = azurerm_key_vault.kv.name
  resource_group_name = azurerm_resource_group.kv.name

  depends_on = [
    azurerm_key_vault_access_policy.writer,
    azurerm_key_vault_access_policy.app_service,
    azurerm_key_vault_access_policy.reader,
  ]
}

output "access_policy_object_ids" {
  value = sort([for p in data.azurerm_key_vault.granted.access_policy : p.object_id])
}
