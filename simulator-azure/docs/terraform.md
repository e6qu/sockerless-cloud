# Using the Azure simulator with Terraform

## Prerequisites

- Terraform installed (`terraform version`)
- The simulator reachable over HTTPS: behind the Caddy HTTPS gateway at
  `https://azure.sockerless.localhost:8443`, or with its own TLS listener

## TLS

The `azurerm` provider reads the cloud's metadata and calls Azure Resource
Manager over HTTPS only. The repository's Caddy gateway fronts the Azure
simulator at `https://azure.sockerless.localhost:8443` and keeps Azure's
host-addressed data-plane names, such as `{account}.blob.azure.sockerless.localhost`
and `{vault}.vault.azure.sockerless.localhost`:

```sh
caddy run --config make/https-gateway/Caddyfile --adapter caddyfile &
# Caddy's local root CA; on macOS it lives under ~/Library/Application Support/Caddy/
export SSL_CERT_FILE="$HOME/.local/share/caddy/pki/authorities/local/root.crt"
```

To use the simulator's own TLS listener instead, give it a certificate and have
Terraform trust the CA that signed it:

```sh
openssl ecparam -genkey -name prime256v1 -out ca-key.pem
openssl req -new -x509 -key ca-key.pem -out ca.pem -days 1 -subj "/CN=Test CA"
openssl ecparam -genkey -name prime256v1 -out server-key.pem
openssl req -new -key server-key.pem -out server.csr -subj "/CN=localhost"
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -out server-cert.pem -days 1 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1")

SIM_TLS_CERT=server-cert.pem SIM_TLS_KEY=server-key.pem ./simulator-azure &
export SSL_CERT_FILE=$(pwd)/ca.pem
```

Go on macOS verifies certificates through Security.framework and ignores
`SSL_CERT_FILE`, so the Terraform harness runs inside the shared Linux test image
there.

## Provider configuration

The provider bootstraps its cloud configuration from the simulator's
`/metadata/endpoints` and signs in through its OAuth 2.0 token endpoint:

```hcl
terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

variable "endpoint" {
  type    = string
  default = "https://azure.sockerless.localhost:8443"
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
```

`resource_provider_registrations = "none"` keeps the provider from registering
resource providers before its first apply.

## Example resources

```hcl
resource "azurerm_resource_group" "main" {
  name     = "my-rg"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "my-vnet"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  address_space       = ["10.0.0.0/16"]
}

resource "azurerm_subnet" "main" {
  name                 = "my-subnet"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_storage_account" "main" {
  name                     = "mystorageacct"
  resource_group_name      = azurerm_resource_group.main.name
  location                 = azurerm_resource_group.main.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

resource "azurerm_log_analytics_workspace" "main" {
  name                = "my-workspace"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}
```

```sh
terraform init
terraform apply -auto-approve
terraform destroy -auto-approve
```

## Coverage

`simulator-azure/terraform-tests/` holds the configurations the provider is
tested with (`main.tf` and one directory per focused stack), and
[`specs/SIM_TEST_COVERAGE_MATRIX.md`](../../specs/SIM_TEST_COVERAGE_MATRIX.md)
names the Terraform test of each surface.

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR`; restarting a non-persistent simulator leaves
  Terraform state describing resources that no longer exist.
- The token endpoint issues Azure-shaped tokens with per-resource audiences,
  from an OAuth 2.0 v2 `scope` or a v1 `resource`.
