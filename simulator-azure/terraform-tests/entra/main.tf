terraform {
  required_providers {
    azuread = {
      source = "hashicorp/azuread"
    }
  }
}

# Standalone Microsoft Entra ID configuration, run by
# TestTerraformEntraApplyDestroy as its own CI shard. It is driven by the
# `azuread` provider rather than `azurerm`, because Microsoft Entra is served
# by Microsoft Graph and not by Azure Resource Manager, and the two providers
# reach it through different coordinates.
#
# `metadata_host` is the only coordinate: the provider resolves the whole cloud
# environment from `https://<metadata_host>/metadata/endpoints`, reads
# `microsoftGraphResourceId` out of that document for the Graph base URI, and
# `loginEndpoint` for the token endpoint. The simulator serves both, so the
# provider reaches Microsoft Graph and Microsoft Entra ID at the simulator
# without a single sim-aware setting. The scheme is fixed at https:// by the
# provider, which is why this stack runs behind the harness HTTPS gateway.

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

provider "azuread" {
  client_id     = "test-client-id"
  client_secret = "test-client-secret"
  tenant_id     = "11111111-1111-1111-1111-111111111111"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")
}

# The calling service principal's own directory coordinates, read back from
# the token claims and Microsoft Graph.
data "azuread_client_config" "current" {}

# ---------- Application registration ----------

# The calling principal is deliberately absent from `owners`: the provider
# always creates an application owned by the caller and then removes that
# reference when configuration does not name it, so this exercises the owner
# reference collection in both directions — the `owners@odata.bind` the create
# carries and the later `DELETE .../owners/{id}/$ref`.
resource "azuread_application" "main" {
  display_name     = "tf-test-entra-app"
  sign_in_audience = "AzureADMyOrg"

  owners = [azuread_user.manager.object_id]
}

# ---------- Service principal materializing the application ----------

resource "azuread_service_principal" "main" {
  client_id = azuread_application.main.client_id

  owners = [data.azuread_client_config.current.object_id]
}

# ---------- Client secret on the application registration ----------

resource "azuread_application_password" "main" {
  application_id = azuread_application.main.id
  display_name   = "tf-test-entra-secret"
}

# ---------- Directory users ----------

resource "azuread_user" "manager" {
  user_principal_name = "tf-test-entra-manager@sockerless.local"
  display_name        = "TF Test Entra Manager"
  mail_nickname       = "tf-test-entra-manager"
  job_title           = "Directory Manager"
  password            = "Sockerless-Tf-Test-1!"
}

# `manager_id` drives the user's manager navigation property:
# PUT /users/{id}/manager/$ref on write, GET /users/{id}/manager on read, and
# DELETE /users/{id}/manager/$ref when it is cleared.
resource "azuread_user" "member" {
  user_principal_name = "tf-test-entra-user@sockerless.local"
  display_name        = "TF Test Entra User"
  mail_nickname       = "tf-test-entra-user"
  password            = "Sockerless-Tf-Test-1!"

  manager_id = azuread_user.manager.object_id
}

# ---------- Security group, its owners, and its members ----------

# A non-caller owner makes the provider resolve the principal through
# GET /directoryObjects/{id} and dispatch on the `@odata.type` it answers with,
# so the polymorphic directory-object read is exercised for real.
resource "azuread_group" "main" {
  display_name     = "tf-test-entra-group"
  description      = "Security group provisioned by the Entra Terraform round trip"
  security_enabled = true

  owners = [
    data.azuread_client_config.current.object_id,
    azuread_user.manager.object_id,
  ]
}

# Membership as its own resource so the member reference collection is driven
# through POST /groups/{id}/members/$ref and
# DELETE /groups/{id}/members/{id}/$ref rather than the create-time bind.
resource "azuread_group_member" "member" {
  group_object_id  = azuread_group.main.object_id
  member_object_id = azuread_user.member.object_id
}

output "azuread_client_config_object_id" {
  value = data.azuread_client_config.current.object_id
}

output "azuread_client_config_client_id" {
  value = data.azuread_client_config.current.client_id
}

output "azuread_client_config_tenant_id" {
  value = data.azuread_client_config.current.tenant_id
}

output "azuread_application_object_id" {
  value = azuread_application.main.object_id
}

output "azuread_application_client_id" {
  value = azuread_application.main.client_id
}

output "azuread_application_id" {
  value = azuread_application.main.id
}

output "azuread_service_principal_object_id" {
  value = azuread_service_principal.main.object_id
}

output "azuread_service_principal_client_id" {
  value = azuread_service_principal.main.client_id
}

output "azuread_application_password_key_id" {
  value = azuread_application_password.main.key_id
}

output "azuread_user_object_id" {
  value = azuread_user.member.object_id
}

output "azuread_user_manager_object_id" {
  value = azuread_user.manager.object_id
}

output "azuread_user_member_manager_id" {
  value = azuread_user.member.manager_id
}

output "azuread_group_object_id" {
  value = azuread_group.main.object_id
}

output "azuread_group_member_id" {
  value = azuread_group_member.member.id
}
