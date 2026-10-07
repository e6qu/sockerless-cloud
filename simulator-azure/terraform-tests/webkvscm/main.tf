terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.8.0"
    }
  }
}

# Standalone App Service configuration run by
# TestTerraformWebAppKeyVaultReferenceAndSourceControl: a Linux custom-container
# web app resolves a Key Vault reference in its app settings through the
# user-assigned identity key_vault_reference_identity_id names, which the
# vault's access policy grants, and azurerm_app_service_source_control deploys
# the repository the test serves into the app's wwwroot. The container answers
# with the resolved setting and the deployed page.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "repo_url" {
  description = "Git repository the app deploys from"
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

resource "azurerm_resource_group" "kvscm" {
  name     = "tf-azrm-kvscm-rg"
  location = "eastus"
}

resource "azurerm_user_assigned_identity" "kvscm" {
  name                = "tf-azrm-kvscm-identity"
  resource_group_name = azurerm_resource_group.kvscm.name
  location            = azurerm_resource_group.kvscm.location
}

resource "azurerm_key_vault" "kvscm" {
  name                       = "tf-azrm-kvscm-kv"
  resource_group_name        = azurerm_resource_group.kvscm.name
  location                   = azurerm_resource_group.kvscm.location
  tenant_id                  = "11111111-1111-1111-1111-111111111111"
  sku_name                   = "standard"
  rbac_authorization_enabled = false
  purge_protection_enabled   = false
  soft_delete_retention_days = 7
}

data "azurerm_client_config" "current" {}

resource "azurerm_key_vault_access_policy" "writer" {
  key_vault_id       = azurerm_key_vault.kvscm.id
  tenant_id          = data.azurerm_client_config.current.tenant_id
  object_id          = data.azurerm_client_config.current.object_id
  secret_permissions = ["Get", "List", "Set", "Delete", "Purge"]
}

resource "azurerm_key_vault_access_policy" "app" {
  key_vault_id       = azurerm_key_vault.kvscm.id
  tenant_id          = "11111111-1111-1111-1111-111111111111"
  object_id          = azurerm_user_assigned_identity.kvscm.principal_id
  secret_permissions = ["Get"]
}

resource "azurerm_key_vault_secret" "db" {
  name         = "db-password"
  value        = "hunter2"
  key_vault_id = azurerm_key_vault.kvscm.id

  depends_on = [azurerm_key_vault_access_policy.writer]
}

resource "azurerm_service_plan" "kvscm" {
  name                = "tf-azrm-kvscm-sp"
  resource_group_name = azurerm_resource_group.kvscm.name
  location            = azurerm_resource_group.kvscm.location
  os_type             = "Linux"
  sku_name            = "S1"
}

# busybox nc answers each connection on WEBSITES_PORT after reading the
# request's headers, with the resolved DB setting and the deployed page, then
# the token the app's identity endpoint issues its user-assigned identity.
resource "azurerm_linux_web_app" "kvscm" {
  name                            = "tf-azrm-kvscm-app"
  resource_group_name             = azurerm_resource_group.kvscm.name
  location                        = azurerm_resource_group.kvscm.location
  service_plan_id                 = azurerm_service_plan.kvscm.id
  key_vault_reference_identity_id = azurerm_user_assigned_identity.kvscm.id

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.kvscm.id]
  }

  app_settings = {
    WEBSITES_PORT                       = "8080"
    WEBSITES_ENABLE_APP_SERVICE_STORAGE = "true"
    DB                                  = "@Microsoft.KeyVault(SecretUri=${azurerm_key_vault_secret.db.versionless_id})"
    IDENTITY_CLIENT_ID                  = azurerm_user_assigned_identity.kvscm.client_id
  }

  site_config {
    app_command_line = "nc -lk -p 8080 -e sh -c \"while read -r l && [ $${#l} -gt 1 ]; do :; done; printf 'HTTP/1.1 200 OK\\r\\nConnection: close\\r\\n\\r\\n'; echo $DB $(cat /home/site/wwwroot/index.html); wget -qO- --header X-IDENTITY-HEADER:$IDENTITY_HEADER $IDENTITY_ENDPOINT'?api-version=2019-08-01&resource=https://vault.azure.net&client_id='$IDENTITY_CLIENT_ID\""

    application_stack {
      docker_image_name   = "docker/library/alpine:latest"
      docker_registry_url = "https://public.ecr.aws"
    }
  }

  depends_on = [azurerm_key_vault_access_policy.app]
}

resource "azurerm_app_service_source_control" "kvscm" {
  app_id                 = azurerm_linux_web_app.kvscm.id
  repo_url               = var.repo_url
  branch                 = "main"
  use_manual_integration = true
}

output "app_hostname" {
  value = azurerm_linux_web_app.kvscm.default_hostname
}

output "principal_id" {
  value = azurerm_user_assigned_identity.kvscm.principal_id
}

output "scm_type" {
  value = azurerm_app_service_source_control.kvscm.scm_type
}
