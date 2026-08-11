terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

# azurerm provider against the sim — the single provider driving the whole
# stack. The sim ships /metadata/endpoints (api-version=2022-09-01) +
# /<tenant>/oauth2/v2.0/token + JWKS + OpenID discovery; together these
# let azurerm bootstrap its cloud config and auth without ever reaching
# real Azure.
provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")

  resource_provider_registrations = "none"

  features {}
}

# ---------- Resource group (foundation for the network/storage/KV slice) ----------

resource "azurerm_resource_group" "main" {
  name     = "tf-test-rg"
  location = "eastus"
}

# ---------- Virtual Network + Subnet ----------

resource "azurerm_virtual_network" "main" {
  name                = "tf-test-vnet"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location

  address_space = ["10.0.0.0/16"]
}

resource "azurerm_subnet" "main" {
  name                 = "tf-test-subnet"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

# ---------- Network Security Group + rule ----------

resource "azurerm_network_security_group" "main" {
  name                = "tf-test-nsg"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
}

resource "azurerm_network_security_rule" "allow_ssh" {
  name                        = "allow-ssh"
  resource_group_name         = azurerm_resource_group.main.name
  network_security_group_name = azurerm_network_security_group.main.name

  priority                   = 100
  direction                  = "Inbound"
  access                     = "Allow"
  protocol                   = "Tcp"
  source_port_range          = "*"
  destination_port_range     = "22"
  source_address_prefix      = "*"
  destination_address_prefix = "*"
}

# ---------- Storage account (Azure Files / runner shared volumes) ----------

resource "azurerm_storage_account" "main" {
  name                     = "tftestsa12345"
  resource_group_name      = azurerm_resource_group.main.name
  location                 = azurerm_resource_group.main.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

# ---------- Key vault (runner credential storage) ----------

resource "azurerm_key_vault" "main" {
  name                = "tf-test-kv"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  tenant_id           = "11111111-1111-1111-1111-111111111111"

  sku_name                   = "standard"
  rbac_authorization_enabled = false
  purge_protection_enabled   = false
  soft_delete_retention_days = 7
}

# ---------- Second resource group (workload/runner slice) ----------

resource "azurerm_resource_group" "az_rg" {
  name     = "tf-azrm-rg"
  location = "eastus"
}

# Container Registry — ACA + AZF runner backends both pull container
# images from a private ACR. Standard SKU is the cheapest tier that
# azurerm accepts (Basic / Standard / Premium).
resource "azurerm_container_registry" "az_acr" {
  name                = "tfazrmacr"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "Standard"
  admin_enabled       = false
}

resource "azurerm_redis_cache" "az_redis" {
  name                = "tfazrmredis"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  capacity            = 1
  family              = "C"
  sku_name            = "Basic"
  minimum_tls_version = "1.2"
  redis_version       = "6"
}

resource "azurerm_redis_firewall_rule" "az_redis_fw" {
  name                = "allow_ci"
  redis_cache_name    = azurerm_redis_cache.az_redis.name
  resource_group_name = azurerm_resource_group.az_rg.name
  start_ip            = "203.0.113.10"
  end_ip              = "203.0.113.10"
}

# User-assigned managed identity — the runner backends bind one of these
# to each pod/container so it can pull from ACR + read from Key Vault.
resource "azurerm_user_assigned_identity" "az_uai" {
  name                = "tf-azrm-uai"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
}

resource "azurerm_public_ip" "az_lb_pip" {
  name                = "tf-azrm-lb-pip"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_virtual_network" "az_nat_vnet" {
  name                = "tf-azrm-nat-vnet"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  address_space       = ["10.93.0.0/16"]
}

resource "azurerm_subnet" "az_nat_subnet" {
  name                 = "tf-azrm-nat-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_nat_vnet.name
  address_prefixes     = ["10.93.1.0/24"]
}

resource "azurerm_public_ip_prefix" "az_nat_prefix" {
  name                = "tf-azrm-nat-prefix"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  prefix_length       = 28
  sku                 = "Standard"
}

resource "azurerm_nat_gateway" "az_nat" {
  name                    = "tf-azrm-nat"
  resource_group_name     = azurerm_resource_group.az_rg.name
  location                = azurerm_resource_group.az_rg.location
  sku_name                = "Standard"
  idle_timeout_in_minutes = 10
}

resource "azurerm_nat_gateway_public_ip_prefix_association" "az_nat_prefix" {
  nat_gateway_id      = azurerm_nat_gateway.az_nat.id
  public_ip_prefix_id = azurerm_public_ip_prefix.az_nat_prefix.id
}

resource "azurerm_subnet_nat_gateway_association" "az_nat_subnet" {
  subnet_id      = azurerm_subnet.az_nat_subnet.id
  nat_gateway_id = azurerm_nat_gateway.az_nat.id
}

resource "azurerm_lb" "az_lb" {
  name                = "tf-azrm-lb"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "Standard"

  frontend_ip_configuration {
    name                 = "frontend"
    public_ip_address_id = azurerm_public_ip.az_lb_pip.id
  }
}

resource "azurerm_lb_backend_address_pool" "az_lb_backend" {
  name            = "backend"
  loadbalancer_id = azurerm_lb.az_lb.id
}

resource "azurerm_lb_probe" "az_lb_probe" {
  name            = "tcp-probe"
  loadbalancer_id = azurerm_lb.az_lb.id
  protocol        = "Tcp"
  port            = 80
}

resource "azurerm_lb_rule" "az_lb_rule" {
  name                           = "http-rule"
  loadbalancer_id                = azurerm_lb.az_lb.id
  protocol                       = "Tcp"
  frontend_port                  = 80
  backend_port                   = 80
  frontend_ip_configuration_name = "frontend"
  backend_address_pool_ids       = [azurerm_lb_backend_address_pool.az_lb_backend.id]
  probe_id                       = azurerm_lb_probe.az_lb_probe.id
}

resource "azurerm_network_interface" "az_vm_nic" {
  name                = "tf-azrm-vm-nic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.main.id
    private_ip_address_allocation = "Dynamic"
    primary                       = true
  }
}

resource "azurerm_linux_virtual_machine" "az_vm" {
  name                            = "tf-azrm-vm"
  resource_group_name             = azurerm_resource_group.az_rg.name
  location                        = azurerm_resource_group.az_rg.location
  size                            = "Standard_B1s"
  admin_username                  = "azureuser"
  admin_password                  = "Str0ng-password-12345!"
  disable_password_authentication = false
  network_interface_ids = [
    azurerm_network_interface.az_vm_nic.id,
  ]

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts"
    version   = "latest"
  }

  # Boot diagnostics names the storage account the machine's console log is
  # written to, which is what RetrieveBootDiagnosticsData reads it back from.
  # It is applied here because a member the model drops reads back missing and
  # shows up as perpetual drift in the follow-up plan.
  boot_diagnostics {
    storage_account_uri = azurerm_storage_account.main.primary_blob_endpoint
  }
}

# Private DNS zone — sockerless's Azure DNS driver creates one of these
# per cluster to resolve `<service>.internal` to cloud-internal IPs.
resource "azurerm_private_dns_zone" "az_pdns" {
  name                = "tf-azrm.internal"
  resource_group_name = azurerm_resource_group.az_rg.name
}

resource "azurerm_dns_zone" "az_dns" {
  name                = "tf-azrm.example.com"
  resource_group_name = azurerm_resource_group.az_rg.name

  tags = {
    env = "terraform"
  }
}

resource "azurerm_dns_a_record" "az_dns_a" {
  name                = "www"
  zone_name           = azurerm_dns_zone.az_dns.name
  resource_group_name = azurerm_resource_group.az_rg.name
  ttl                 = 300
  records             = ["203.0.113.42"]

  tags = {
    env = "terraform"
  }
}

resource "azurerm_eventhub_namespace" "az_eh_ns" {
  name                = "tfazrmehns"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "Standard"
  capacity            = 1

  tags = {
    env = "terraform"
  }
}

resource "azurerm_eventhub" "az_eh" {
  name              = "tfazrmeventhub"
  namespace_id      = azurerm_eventhub_namespace.az_eh_ns.id
  partition_count   = 1
  message_retention = 1
}

resource "azurerm_eventhub_consumer_group" "az_eh_cg" {
  name                = "tfazrmehcg"
  namespace_name      = azurerm_eventhub_namespace.az_eh_ns.name
  eventhub_name       = azurerm_eventhub.az_eh.name
  resource_group_name = azurerm_resource_group.az_rg.name
  user_metadata       = "terraform"
}

resource "azurerm_eventhub_authorization_rule" "az_eh_rule" {
  name                = "tfazrmehrule"
  namespace_name      = azurerm_eventhub_namespace.az_eh_ns.name
  eventhub_name       = azurerm_eventhub.az_eh.name
  resource_group_name = azurerm_resource_group.az_rg.name

  listen = true
  send   = true
  manage = false
}

resource "azurerm_servicebus_namespace" "az_sb_ns" {
  name                = "tfazrmsbns"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "Standard"

  tags = {
    env = "terraform"
  }
}

resource "azurerm_servicebus_queue" "az_sb_queue" {
  name         = "tfazrmsbqueue"
  namespace_id = azurerm_servicebus_namespace.az_sb_ns.id

  max_size_in_megabytes = 1024
}

resource "azurerm_eventgrid_topic" "az_eg_topic" {
  name                = "tf-azrm-eg-topic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  tags = {
    env = "test"
  }
}

resource "azurerm_eventgrid_domain" "az_eg_domain" {
  name                = "tf-azrm-eg-domain"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  tags = {
    env = "test"
  }
}

resource "azurerm_eventgrid_domain_topic" "az_eg_domain_topic" {
  name                = "tf-azrm-eg-domain-topic"
  domain_name         = azurerm_eventgrid_domain.az_eg_domain.name
  resource_group_name = azurerm_resource_group.az_rg.name
}

resource "azurerm_eventgrid_system_topic" "az_eg_system_topic" {
  name                = "tf-azrm-eg-system-topic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  source_resource_id  = azurerm_storage_account.main.id
  topic_type          = "Microsoft.Storage.StorageAccounts"
}

# ---------- Cosmos DB (NoSQL control plane) ----------

resource "azurerm_cosmosdb_account" "az_cosmos" {
  name                = "tfazrmcosmos"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  offer_type          = "Standard"
  kind                = "GlobalDocumentDB"

  consistency_policy {
    consistency_level = "Session"
  }

  geo_location {
    location          = azurerm_resource_group.az_rg.location
    failover_priority = 0
  }

  tags = {
    env = "terraform"
  }
}

resource "azurerm_cosmosdb_sql_database" "az_cosmos_db" {
  name                = "tfappdb"
  resource_group_name = azurerm_resource_group.az_rg.name
  account_name        = azurerm_cosmosdb_account.az_cosmos.name
  throughput          = 400
}

resource "azurerm_cosmosdb_sql_container" "az_cosmos_container" {
  name                  = "users"
  resource_group_name   = azurerm_resource_group.az_rg.name
  account_name          = azurerm_cosmosdb_account.az_cosmos.name
  database_name         = azurerm_cosmosdb_sql_database.az_cosmos_db.name
  partition_key_paths   = ["/id"]
  partition_key_version = 1
  throughput            = 400
}

resource "azurerm_cosmosdb_table" "az_cosmos_table" {
  name                = "tfcosmostable"
  resource_group_name = azurerm_resource_group.az_rg.name
  account_name        = azurerm_cosmosdb_account.az_cosmos.name
  throughput          = 400
}

# Log Analytics workspace — Container App Environment requires one for
# log ingestion. PerGB2018 is the canonical SKU.
resource "azurerm_log_analytics_workspace" "az_law" {
  name                = "tf-azrm-law"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}

# Application Insights — observability companion to Container Apps.
resource "azurerm_application_insights" "az_appins" {
  name                = "tf-azrm-ai"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  application_type    = "web"
  workspace_id        = azurerm_log_analytics_workspace.az_law.id
}

# Container App Environment — the ACA host plane. Sockerless's ACA
# backend lives in one of these.
resource "azurerm_container_app_environment" "az_cae" {
  name                       = "tf-azrm-cae"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  logs_destination           = "log-analytics"
  log_analytics_workspace_id = azurerm_log_analytics_workspace.az_law.id
}

# Container App — the ACA workload. Minimal configuration: one container
# with image + 0.25vCPU/0.5Gi memory + ingress disabled.
resource "azurerm_container_app" "az_ca" {
  name                         = "tf-azrm-ca"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  resource_group_name          = azurerm_resource_group.az_rg.name
  revision_mode                = "Single"

  template {
    container {
      name    = "main"
      image   = "public.ecr.aws/docker/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["sh", "-c", "sleep infinity"]
    }
  }
}

# Container App Job — the ACA runner-job primitive. Sockerless dispatches
# CI runner jobs as Container App Jobs.
resource "azurerm_container_app_job" "az_caj" {
  name                         = "tf-azrm-caj"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  resource_group_name          = azurerm_resource_group.az_rg.name
  location                     = azurerm_resource_group.az_rg.location

  replica_timeout_in_seconds = 60
  replica_retry_limit        = 1

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
      command = ["sh", "-c", "echo hello && exit 0"]
    }
  }
}

# Container App Environment storage — the Azure Files mount definition an ACA
# app or job references by name to get a shared volume. Sockerless's ACA backend
# provisions one of these whenever a docker volume is bound into a container, so
# the managedEnvironments/{env}/storages sub-resource is driven here through the
# real azurerm resource rather than only through the SDK and CLI.
resource "azurerm_container_app_environment_storage" "az_cae_storage" {
  name                         = "tfazrmcaestorage"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  account_name                 = azurerm_storage_account.az_st.name
  share_name                   = "tfazrmshare"
  access_key                   = azurerm_storage_account.az_st.primary_access_key
  access_mode                  = "ReadWrite"
}

# Logic App workflow — runner orchestration stacks often use Logic Apps for
# webhook fan-in around Azure-hosted jobs. This drives Microsoft.Logic/workflows
# through the real azurerm resource.
resource "azurerm_logic_app_workflow" "az_logic" {
  name                = "tf-azrm-logic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  tags = {
    env = "terraform"
  }
}

# Azure Container Instance — the direct container primitive beneath a number
# of small-runner and one-shot task deployments. Uses the cached alpine image
# pre-pulled by the Terraform test harness so the simulator starts a real
# Docker container without hitting the network during apply.
resource "azurerm_container_group" "az_aci" {
  name                = "tf-azrm-aci"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  restart_policy      = "Never"

  container {
    name     = "main"
    image    = "alpine:latest"
    cpu      = 0.25
    memory   = 0.5
    commands = ["sh", "-c", "echo terraform-aci"]
  }

  tags = {
    env = "terraform"
  }
}

# Service Plan — Function App host.
resource "azurerm_service_plan" "az_sp" {
  name                = "tf-azrm-sp"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  sku_name            = "Y1"
}

# Storage account for the Function App. Real Function Apps need a storage
# account for queue triggers + run metadata; a dedicated account keeps the
# Function App wiring independent of the general-purpose account above.
resource "azurerm_storage_account" "az_st" {
  name                     = "tfazrmst12345"
  resource_group_name      = azurerm_resource_group.az_rg.name
  location                 = azurerm_resource_group.az_rg.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

# Storage container through the AzureRM data-plane resource path.
# AzureRM 5 requires the storage account ID and resolves the account's
# data-plane endpoints from that resource. This verifies the simulator emits
# azurerm-parseable {account}.{service}.{suffix} endpoints and matching
# /metadata/endpoints storage suffixes.
resource "azurerm_storage_container" "az_st_container" {
  name                  = "tfazrmcontainer"
  storage_account_id    = azurerm_storage_account.az_st.id
  container_access_type = "private"
}

resource "azurerm_storage_table" "az_st_table" {
  name               = "tfazrmstable"
  storage_account_id = azurerm_storage_account.az_st.id
}

resource "azurerm_storage_share" "az_st_share" {
  name               = "tfazrmshare"
  storage_account_id = azurerm_storage_account.az_st.id
  quota              = 50

  acl {
    id = "terraform-policy"

    access_policy {
      permissions = "rwdl"
      start       = "2026-07-28T10:00:00Z"
      expiry      = "2026-07-29T10:00:00Z"
    }
  }
}

# Azure Files directories — the Files data plane's Create / Get / Delete
# Directory operations, which is how a nested path inside a share comes into
# being. An Azure Container Apps or Azure Functions workload mounting the share
# walks exactly this tree, so a share is only usable as a volume if directories
# below its root are real. The provider reads each directory back on every
# refresh, so the idempotency plan covers Get Directory Properties too.
resource "azurerm_storage_share_directory" "az_st_share_dir" {
  name              = "build"
  storage_share_url = azurerm_storage_share.az_st_share.url
}

resource "azurerm_storage_share_directory" "az_st_share_subdir" {
  name              = "${azurerm_storage_share_directory.az_st_share_dir.name}/artifacts"
  storage_share_url = azurerm_storage_share.az_st_share.url
}

# Linux Function App — AZF runner backend's host primitive.
resource "azurerm_linux_function_app" "az_fa" {
  name                       = "tf-azrm-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  site_config {}
}

# App Service regional VNet integration (the "swift" virtual network
# connection) — the Microsoft.Web/sites/networkConfig/virtualNetwork endpoint
# the azure-functions backend uses for cloud-dns service discovery. Regional
# VNet integration requires an Elastic Premium (or dedicated) plan and a subnet
# delegated to Microsoft.Web/serverFarms. The provider PUTs the swift
# connection, then reads it back on every plan — so the sim must round-trip
# both the subnet delegation (incl. its actions) and the connection's
# subnetResourceId for the apply to stay idempotent.
resource "azurerm_virtual_network" "az_swift_vnet" {
  name                = "tf-azrm-swift-vnet"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  address_space       = ["10.94.0.0/16"]
}

resource "azurerm_subnet" "az_swift_subnet" {
  name                 = "tf-azrm-swift-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_swift_vnet.name
  address_prefixes     = ["10.94.1.0/24"]

  delegation {
    name = "appservice-delegation"
    service_delegation {
      name    = "Microsoft.Web/serverFarms"
      actions = ["Microsoft.Network/virtualNetworks/subnets/action"]
    }
  }
}

resource "azurerm_service_plan" "az_swift_sp" {
  name                = "tf-azrm-swift-sp"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  sku_name            = "EP1"
}

resource "azurerm_linux_function_app" "az_swift_fa" {
  name                       = "tf-azrm-swift-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_swift_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  site_config {}
}

resource "azurerm_app_service_virtual_network_swift_connection" "az_swift" {
  app_service_id = azurerm_linux_function_app.az_swift_fa.id
  subnet_id      = azurerm_subnet.az_swift_subnet.id
}

# Key Vault + a single secret. The secret resource is what fires the
# challenge-then-retry handshake: terraform-provider-azurerm constructs
# an azsecrets client, issues the unauthenticated PUT, parses the
# WWW-Authenticate response (which is where the prior regression reopened — the
# authorization URL must split to ≥ 4 segments or the SDK's
# parseTenant panics), fetches a token from the configured credential,
# retries the PUT. If the sim's challenge format is wrong, apply fails
# with `index out of range [3]`.
resource "azurerm_key_vault" "az_kv" {
  name                       = "tf-azrm-kv"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  tenant_id                  = "11111111-1111-1111-1111-111111111111"
  sku_name                   = "standard"
  rbac_authorization_enabled = false
  purge_protection_enabled   = false
  soft_delete_retention_days = 7
}

resource "azurerm_key_vault_access_policy" "az_kv_policy" {
  key_vault_id = azurerm_key_vault.az_kv.id
  tenant_id    = "11111111-1111-1111-1111-111111111111"
  object_id    = "22222222-2222-2222-2222-222222222222"

  key_permissions         = ["Get", "List", "Create", "Update", "Delete", "Backup", "Restore", "Import", "Sign", "Verify", "Encrypt", "Decrypt", "WrapKey", "UnwrapKey"]
  secret_permissions      = ["Get", "List", "Set", "Delete", "Backup", "Restore"]
  certificate_permissions = ["Get", "List", "Create", "Update", "Delete", "Import", "Backup", "Restore"]
}

resource "azurerm_key_vault_secret" "az_kv_secret" {
  name         = "tf-azrm-secret"
  value        = "hunter2"
  key_vault_id = azurerm_key_vault.az_kv.id

  depends_on = [azurerm_key_vault_access_policy.az_kv_policy]
}

resource "azurerm_key_vault_key" "az_kv_key" {
  name         = "tf-azrm-key"
  key_vault_id = azurerm_key_vault.az_kv.id
  key_type     = "RSA"
  key_size     = 2048
  key_opts     = ["decrypt", "encrypt", "sign", "unwrapKey", "verify", "wrapKey"]

  depends_on = [azurerm_key_vault_access_policy.az_kv_policy]
}

resource "azurerm_key_vault_certificate" "az_kv_cert" {
  name         = "tf-azrm-cert"
  key_vault_id = azurerm_key_vault.az_kv.id

  certificate_policy {
    issuer_parameters {
      name = "Self"
    }

    key_properties {
      exportable = true
      key_size   = 2048
      key_type   = "RSA"
      reuse_key  = false
    }

    secret_properties {
      content_type = "application/x-pkcs12"
    }

    x509_certificate_properties {
      subject            = "CN=tf-azrm-cert"
      validity_in_months = 12
      key_usage          = ["digitalSignature", "keyEncipherment"]
    }
  }

  depends_on = [azurerm_key_vault_access_policy.az_kv_policy]
}

# ---------- API Management (Microsoft.ApiManagement control plane) ----------
# Drives the sim's APIM service + api + product + subscription slice through
# the real terraform-provider-azurerm client (the stable consumer of these
# routes — see consumer issues #178 / #210, which used the Consumption SKU).
# Consumption_0 is what the consumer provisions and is the SKU the provider
# does NOT gate behind the portalsettings/tenant-access create choreography
# (Developer/Standard/Premium PUT /portalsettings/{signin,signup,delegation}
# + /tenant/access after create — surface no sockerless consumer exercises).
resource "azurerm_api_management" "az_apim" {
  name                = "tf-azrm-apim"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  publisher_name      = "Sockerless CI"
  publisher_email     = "ci@sockerless.test"
  sku_name            = "Consumption_0"
}

resource "azurerm_api_management_api" "az_apim_api" {
  name                = "tf-azrm-api"
  resource_group_name = azurerm_resource_group.az_rg.name
  api_management_name = azurerm_api_management.az_apim.name
  revision            = "1"
  display_name        = "TF API"
  path                = "tfapi"
  protocols           = ["https"]
}

resource "azurerm_api_management_product" "az_apim_product" {
  product_id            = "tf-azrm-product"
  resource_group_name   = azurerm_resource_group.az_rg.name
  api_management_name   = azurerm_api_management.az_apim.name
  display_name          = "TF Product"
  published             = true
  subscription_required = true
  approval_required     = false
}

resource "azurerm_api_management_subscription" "az_apim_sub" {
  resource_group_name = azurerm_resource_group.az_rg.name
  api_management_name = azurerm_api_management.az_apim.name
  product_id          = azurerm_api_management_product.az_apim_product.id
  display_name        = "TF Subscription"
  state               = "active"
}

# ---------- Azure Database for PostgreSQL flexible server ----------

resource "azurerm_postgresql_flexible_server" "az_pg" {
  name                   = "tf-azrm-pg"
  resource_group_name    = azurerm_resource_group.az_rg.name
  location               = azurerm_resource_group.az_rg.location
  version                = "15"
  administrator_login    = "psqladmin"
  administrator_password = "Sup3rSecret!"
  storage_mb             = 32768
  sku_name               = "B_Standard_B1ms"
}

resource "azurerm_postgresql_flexible_server_database" "az_pg_db" {
  name      = "tfapp"
  server_id = azurerm_postgresql_flexible_server.az_pg.id
  charset   = "UTF8"
  collation = "en_US.utf8"
}

resource "azurerm_postgresql_flexible_server_firewall_rule" "az_pg_fw" {
  name             = "allow-ci"
  server_id        = azurerm_postgresql_flexible_server.az_pg.id
  start_ip_address = "10.0.0.1"
  end_ip_address   = "10.0.0.10"
}

# ---------- Azure Private Link (both halves) + the attachment resources ----------
# The provider half is a private link service in front of the load balancer's
# frontend; the consumer half is a private endpoint in its own subnet. The
# endpoint's create opens a connection on the service, and the provider reads
# that connection's state back on every plan — so the two surfaces have to
# address one object for the apply to be idempotent. A second endpoint targets
# the Key Vault, which exercises the same integration across resource
# providers, and its private DNS zone group publishes the vault's private-link
# record into the zone the zone's own record surface then serves.

resource "azurerm_virtual_network" "az_pl_vnet" {
  name                = "tf-azrm-pl-vnet"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  address_space       = ["10.95.0.0/16"]
}

resource "azurerm_subnet" "az_pl_nat_subnet" {
  name                 = "tf-azrm-pl-nat-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_pl_vnet.name
  address_prefixes     = ["10.95.1.0/24"]
}

resource "azurerm_subnet" "az_pl_pe_subnet" {
  name                 = "tf-azrm-pl-pe-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_pl_vnet.name
  address_prefixes     = ["10.95.2.0/24"]
}

resource "azurerm_application_security_group" "az_asg" {
  name                = "tf-azrm-asg"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  tags = {
    tier = "web"
  }
}

resource "azurerm_private_link_service" "az_pls" {
  name                = "tf-azrm-pls"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  auto_approval_subscription_ids              = ["00000000-0000-0000-0000-000000000001"]
  visibility_subscription_ids                 = ["00000000-0000-0000-0000-000000000001"]
  load_balancer_frontend_ip_configuration_ids = [one(azurerm_lb.az_lb.frontend_ip_configuration).id]

  # A static address keeps the configuration and the service's own report of
  # the NAT configuration in agreement: the provider records whatever address
  # the service assigns, and refuses a later plan that would take an assigned
  # address back out of the configuration.
  nat_ip_configuration {
    name                       = "natcfg1"
    primary                    = true
    subnet_id                  = azurerm_subnet.az_pl_nat_subnet.id
    private_ip_address         = "10.95.1.10"
    private_ip_address_version = "IPv4"
  }
}

resource "azurerm_private_endpoint" "az_pe" {
  name                = "tf-azrm-pe"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  subnet_id           = azurerm_subnet.az_pl_pe_subnet.id

  private_service_connection {
    name                           = "tf-azrm-pe"
    is_manual_connection           = false
    private_connection_resource_id = azurerm_private_link_service.az_pls.id
  }
}

resource "azurerm_private_endpoint_application_security_group_association" "az_pe_asg" {
  private_endpoint_id           = azurerm_private_endpoint.az_pe.id
  application_security_group_id = azurerm_application_security_group.az_asg.id
}

resource "azurerm_private_dns_zone" "az_kv_pdns" {
  name                = "privatelink.vaultcore.azure.net"
  resource_group_name = azurerm_resource_group.az_rg.name
}

resource "azurerm_private_endpoint" "az_kv_pe" {
  name                = "tf-azrm-kv-pe"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  subnet_id           = azurerm_subnet.az_pl_pe_subnet.id

  private_service_connection {
    name                           = "tf-azrm-kv-pe"
    is_manual_connection           = false
    private_connection_resource_id = azurerm_key_vault.az_kv.id
    subresource_names              = ["vault"]
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [azurerm_private_dns_zone.az_kv_pdns.id]
  }
}

# ---------- Network profile + service endpoint policy ----------

resource "azurerm_subnet" "az_np_subnet" {
  name                 = "tf-azrm-np-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_pl_vnet.name
  address_prefixes     = ["10.95.3.0/24"]
}

resource "azurerm_network_profile" "az_np" {
  name                = "tf-azrm-np"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  container_network_interface {
    name = "tf-azrm-cnic"

    ip_configuration {
      name      = "ipconfig1"
      subnet_id = azurerm_subnet.az_np_subnet.id
    }
  }
}

resource "azurerm_subnet_service_endpoint_storage_policy" "az_sep" {
  name                = "tf-azrm-sep"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  definition {
    name              = "allow-one-account"
    description       = "restrict storage service endpoint traffic to one account"
    service           = "Microsoft.Storage"
    service_resources = [azurerm_storage_account.az_st.id]
  }
}

# ---------- Outputs (cross-resource invariants) ----------

output "resource_group_id" {
  value = azurerm_resource_group.main.id
}

output "vnet_id" {
  value = azurerm_virtual_network.main.id
}

output "subnet_id" {
  value = azurerm_subnet.main.id
}

output "nsg_id" {
  value = azurerm_network_security_group.main.id
}

output "nsg_rule_id" {
  value = azurerm_network_security_rule.allow_ssh.id
}

output "storage_account_id" {
  value = azurerm_storage_account.main.id
}

output "storage_account_blob_endpoint" {
  value = azurerm_storage_account.main.primary_blob_endpoint
}

output "key_vault_id" {
  value = azurerm_key_vault.main.id
}

output "key_vault_uri" {
  value = azurerm_key_vault.main.vault_uri
}

output "azrm_key_vault_access_policy_id" {
  value = azurerm_key_vault_access_policy.az_kv_policy.id
}

output "azrm_key_vault_key_id" {
  value = azurerm_key_vault_key.az_kv_key.id
}

output "azrm_key_vault_certificate_id" {
  value = azurerm_key_vault_certificate.az_kv_cert.id
}

output "azrm_resource_group_id" {
  value = azurerm_resource_group.az_rg.id
}

output "azrm_postgresql_flexible_server_id" {
  value = azurerm_postgresql_flexible_server.az_pg.id
}

output "azrm_postgresql_flexible_server_database_id" {
  value = azurerm_postgresql_flexible_server_database.az_pg_db.id
}

output "azrm_postgresql_flexible_server_firewall_rule_id" {
  value = azurerm_postgresql_flexible_server_firewall_rule.az_pg_fw.id
}

output "azrm_acr_id" {
  value = azurerm_container_registry.az_acr.id
}

output "azrm_redis_cache_hostname" {
  value = azurerm_redis_cache.az_redis.hostname
}

output "azrm_redis_firewall_rule_id" {
  value = azurerm_redis_firewall_rule.az_redis_fw.id
}

output "azrm_uai_id" {
  value = azurerm_user_assigned_identity.az_uai.id
}

output "azrm_lb_id" {
  value = azurerm_lb.az_lb.id
}

output "azrm_lb_backend_pool_id" {
  value = azurerm_lb_backend_address_pool.az_lb_backend.id
}

output "azrm_lb_probe_id" {
  value = azurerm_lb_probe.az_lb_probe.id
}

output "azrm_lb_rule_id" {
  value = azurerm_lb_rule.az_lb_rule.id
}

output "azrm_nat_gateway_id" {
  value = azurerm_nat_gateway.az_nat.id
}

output "azrm_nat_public_ip_prefix_id" {
  value = azurerm_public_ip_prefix.az_nat_prefix.id
}

output "azrm_nat_subnet_id" {
  value = azurerm_subnet_nat_gateway_association.az_nat_subnet.subnet_id
}

output "azrm_private_dns_zone_id" {
  value = azurerm_private_dns_zone.az_pdns.id
}

output "azrm_public_dns_zone_id" {
  value = azurerm_dns_zone.az_dns.id
}

output "azrm_public_dns_a_record_id" {
  value = azurerm_dns_a_record.az_dns_a.id
}

output "azrm_servicebus_namespace_id" {
  value = azurerm_servicebus_namespace.az_sb_ns.id
}

output "azrm_servicebus_queue_id" {
  value = azurerm_servicebus_queue.az_sb_queue.id
}

output "azrm_eventgrid_topic_endpoint" {
  value = azurerm_eventgrid_topic.az_eg_topic.endpoint
}

output "azrm_cosmosdb_account_endpoint" {
  value = azurerm_cosmosdb_account.az_cosmos.endpoint
}

output "azrm_cosmosdb_sql_container_id" {
  value = azurerm_cosmosdb_sql_container.az_cosmos_container.id
}

output "azrm_cosmosdb_table_id" {
  value = azurerm_cosmosdb_table.az_cosmos_table.id
}

output "azrm_law_id" {
  value = azurerm_log_analytics_workspace.az_law.id
}

output "azrm_appins_id" {
  value = azurerm_application_insights.az_appins.id
}

output "azrm_container_app_env_id" {
  value = azurerm_container_app_environment.az_cae.id
}

output "azrm_container_app_id" {
  value = azurerm_container_app.az_ca.id
}

output "azrm_container_app_job_id" {
  value = azurerm_container_app_job.az_caj.id
}

output "azrm_container_app_env_storage_id" {
  value = azurerm_container_app_environment_storage.az_cae_storage.id
}

output "azrm_logic_app_workflow_id" {
  value = azurerm_logic_app_workflow.az_logic.id
}

output "azrm_container_group_id" {
  value = azurerm_container_group.az_aci.id
}

output "azrm_service_plan_id" {
  value = azurerm_service_plan.az_sp.id
}

output "azrm_storage_account_id" {
  value = azurerm_storage_account.az_st.id
}

output "azrm_storage_container_id" {
  value = azurerm_storage_container.az_st_container.id
}

output "azrm_storage_table_id" {
  value = azurerm_storage_table.az_st_table.id
}

output "azrm_storage_share_id" {
  value = azurerm_storage_share.az_st_share.id
}

output "azrm_storage_share_directory_id" {
  value = azurerm_storage_share_directory.az_st_share_subdir.id
}

output "azrm_storage_share_directory_name" {
  value = azurerm_storage_share_directory.az_st_share_subdir.name
}

output "azrm_function_app_id" {
  value = azurerm_linux_function_app.az_fa.id
}

output "azrm_swift_subnet_id" {
  value = azurerm_subnet.az_swift_subnet.id
}

output "azrm_swift_connection_id" {
  value = azurerm_app_service_virtual_network_swift_connection.az_swift.id
}

output "azrm_apim_id" {
  value = azurerm_api_management.az_apim.id
}

output "azrm_apim_gateway_url" {
  value = azurerm_api_management.az_apim.gateway_url
}

output "azrm_apim_api_id" {
  value = azurerm_api_management_api.az_apim_api.id
}

output "azrm_apim_product_id" {
  value = azurerm_api_management_product.az_apim_product.id
}

output "azrm_apim_subscription_id" {
  value = azurerm_api_management_subscription.az_apim_sub.id
}

output "azrm_application_security_group_id" {
  value = azurerm_application_security_group.az_asg.id
}

output "azrm_private_link_service_id" {
  value = azurerm_private_link_service.az_pls.id
}

output "azrm_private_link_service_alias" {
  value = azurerm_private_link_service.az_pls.alias
}

output "azrm_private_endpoint_id" {
  value = azurerm_private_endpoint.az_pe.id
}

output "azrm_private_endpoint_private_ip" {
  value = one(azurerm_private_endpoint.az_pe.private_service_connection).private_ip_address
}

output "azrm_private_endpoint_nic_id" {
  value = one(azurerm_private_endpoint.az_pe.network_interface).id
}

output "azrm_kv_private_endpoint_id" {
  value = azurerm_private_endpoint.az_kv_pe.id
}

output "azrm_kv_private_endpoint_custom_dns_fqdn" {
  value = one(azurerm_private_endpoint.az_kv_pe.custom_dns_configs).fqdn
}

output "azrm_kv_private_endpoint_dns_zone_group_id" {
  value = one(azurerm_private_endpoint.az_kv_pe.private_dns_zone_group).id
}

output "azrm_network_profile_id" {
  value = azurerm_network_profile.az_np.id
}

output "azrm_subnet_service_endpoint_storage_policy_id" {
  value = azurerm_subnet_service_endpoint_storage_policy.az_sep.id
}

# ---------- Application Gateway ----------
# Azure's layer-7 load balancer, deployed into its own subnet and fronted by a
# public address. The listener, the URL path map and the request routing rule
# are the routing program the gateway's data plane executes: a request that
# matches no path rule goes to the default pool, and /api/* goes to the other
# one.

resource "azurerm_virtual_network" "az_appgw_vnet" {
  name                = "tf-azrm-appgw-vnet"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  address_space       = ["10.96.0.0/16"]
}

resource "azurerm_subnet" "az_appgw_subnet" {
  name                 = "tf-azrm-appgw-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_appgw_vnet.name
  address_prefixes     = ["10.96.1.0/24"]
}

resource "azurerm_public_ip" "az_appgw_pip" {
  name                = "tf-azrm-appgw-pip"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_application_gateway" "az_appgw" {
  name                = "tf-azrm-appgw"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  sku {
    name     = "Standard_v2"
    tier     = "Standard_v2"
    capacity = 2
  }

  gateway_ip_configuration {
    name      = "gw-ipcfg"
    subnet_id = azurerm_subnet.az_appgw_subnet.id
  }

  frontend_ip_configuration {
    name                 = "public"
    public_ip_address_id = azurerm_public_ip.az_appgw_pip.id
  }

  frontend_port {
    name = "port-80"
    port = 80
  }

  backend_address_pool {
    name         = "web"
    ip_addresses = ["10.96.1.10"]
  }

  backend_address_pool {
    name         = "api"
    ip_addresses = ["10.96.1.11"]
  }

  probe {
    name                = "web-probe"
    protocol            = "Http"
    path                = "/healthz"
    interval            = 30
    timeout             = 30
    unhealthy_threshold = 3
    host                = "127.0.0.1"

    match {
      status_code = ["200-399"]
    }
  }

  backend_http_settings {
    name                  = "web-settings"
    cookie_based_affinity = "Disabled"
    port                  = 80
    protocol              = "Http"
    request_timeout       = 30
    probe_name            = "web-probe"
  }

  backend_http_settings {
    name                  = "api-settings"
    cookie_based_affinity = "Disabled"
    port                  = 8080
    protocol              = "Http"
    request_timeout       = 30
  }

  http_listener {
    name                           = "listener"
    frontend_ip_configuration_name = "public"
    frontend_port_name             = "port-80"
    protocol                       = "Http"
  }

  url_path_map {
    name                               = "site-paths"
    default_backend_address_pool_name  = "web"
    default_backend_http_settings_name = "web-settings"

    path_rule {
      name                       = "api"
      paths                      = ["/api/*"]
      backend_address_pool_name  = "api"
      backend_http_settings_name = "api-settings"
    }
  }

  request_routing_rule {
    name               = "site-rule"
    rule_type          = "PathBasedRouting"
    priority           = 100
    http_listener_name = "listener"
    url_path_map_name  = "site-paths"
  }
}

# ---------- Network Watcher ----------
# The regional diagnostics anchor, plus the flow log configuration of a network
# security group — the record the watcher's target-addressed flow log
# operations read and write.

resource "azurerm_network_watcher" "az_nw" {
  name                = "tf-azrm-network-watcher"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  tags = {
    team = "network"
  }
}

# A packet capture against the stack's virtual machine. The provider creates it
# through PacketCaptures_Create and destroys it through PacketCaptures_Delete,
# which is a real capture session opening a socket on the machine's interface —
# so this resource only converges on a host that can run the machine.
resource "azurerm_virtual_machine_packet_capture" "az_pcap" {
  name               = "tf-azrm-packet-capture"
  network_watcher_id = azurerm_network_watcher.az_nw.id
  virtual_machine_id = azurerm_linux_virtual_machine.az_vm.id

  maximum_bytes_per_packet            = 0
  maximum_bytes_per_session           = 1048576
  maximum_capture_duration_in_seconds = 60

  storage_location {
    storage_account_id = azurerm_storage_account.az_st.id
  }

  filter {
    protocol         = "TCP"
    local_ip_address = "10.30.1.4"
  }
}

# ---------- Virtual Network Manager ----------
# The scope-and-deployment half of centrally managed networking: the manager
# declares the subscriptions it governs and the configuration kinds it may
# manage there.

resource "azurerm_network_manager" "az_netman" {
  name                = "tf-azrm-network-manager"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  description         = "terraform network manager"
  scope_accesses      = ["Connectivity", "SecurityAdmin"]

  scope {
    subscription_ids = ["/subscriptions/00000000-0000-0000-0000-000000000001"]
  }
}

output "azrm_application_gateway_id" {
  value = azurerm_application_gateway.az_appgw.id
}

output "azrm_application_gateway_backend_pool_ids" {
  value = [for pool in azurerm_application_gateway.az_appgw.backend_address_pool : pool.id]
}

output "azrm_application_gateway_listener_ids" {
  value = [for listener in azurerm_application_gateway.az_appgw.http_listener : listener.id]
}

output "azrm_network_watcher_id" {
  value = azurerm_network_watcher.az_nw.id
}

output "azrm_packet_capture_id" {
  value = azurerm_virtual_machine_packet_capture.az_pcap.id
}

# storage_path is computed by the service, so it proves the capture came back
# carrying where its recording will land rather than only echoing the request.
output "azrm_packet_capture_storage_path" {
  value = azurerm_virtual_machine_packet_capture.az_pcap.storage_location[0].storage_path
}

output "azrm_network_manager_id" {
  value = azurerm_network_manager.az_netman.id
}

output "azrm_network_manager_scope_accesses" {
  value = azurerm_network_manager.az_netman.scope_accesses
}
