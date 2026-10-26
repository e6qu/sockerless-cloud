terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

# The one azurerm provider driving the stack. It bootstraps its cloud config and
# auth from the simulator's /metadata/endpoints (api-version=2022-09-01),
# /<tenant>/oauth2/v2.0/token, JWKS and OpenID discovery.
provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(trimprefix(var.endpoint, "https://"), "http://")

  resource_provider_registrations = "none"

  features {}
}

resource "azurerm_resource_group" "main" {
  name     = "tf-test-rg"
  location = "eastus"
}

# Two address prefixes: the virtual network is one routing domain over both,
# and the machine below lives in a subnet of the second.
resource "azurerm_virtual_network" "main" {
  name                = "tf-test-vnet"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location

  address_space = ["10.0.0.0/16", "10.111.0.0/16"]
}

resource "azurerm_subnet" "main" {
  name                 = "tf-test-subnet"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_subnet" "second_prefix" {
  name                 = "tf-test-subnet-second-prefix"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.111.1.0/24"]
}

output "azrm_vnet_address_space" {
  value = azurerm_virtual_network.main.address_space
}

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

resource "azurerm_storage_account" "main" {
  name                     = "tftestsa12345"
  resource_group_name      = azurerm_resource_group.main.name
  location                 = azurerm_resource_group.main.location
  account_tier             = "Standard"
  account_replication_type = "LRS"

  # The Blob service enforces its CORS rules on a browser's cross-origin
  # requests.
  blob_properties {
    cors_rule {
      allowed_origins    = ["https://console.example.com"]
      allowed_methods    = ["GET", "PUT"]
      allowed_headers    = ["x-ms-*"]
      exposed_headers    = ["x-ms-request-id"]
      max_age_in_seconds = 600
    }
  }
}

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

resource "azurerm_resource_group" "az_rg" {
  name     = "tf-azrm-rg"
  location = "eastus"
}

# The admin user is enabled so the provider surfaces the admin credential the
# registry's data plane authenticates.
resource "azurerm_container_registry" "az_acr" {
  name                = "tfazrmacr"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "Standard"
  admin_enabled       = true
}

# An Azure Container Registry Tasks task with an inline task file step, and a
# run of it: azurerm_container_registry_task_schedule_run_now schedules a
# TaskRunRequest and completes only once Runs_Get reports it Succeeded.
resource "azurerm_container_registry_task" "az_acr_task" {
  name                  = "tfazrmtask"
  container_registry_id = azurerm_container_registry.az_acr.id

  platform {
    os = "Linux"
  }

  encoded_step {
    task_content = <<-EOT
      version: v1.1.0
      steps:
        - cmd: mirror.gcr.io/library/alpine:3.20 echo run-now from $RegistryName
    EOT
  }
}

resource "azurerm_container_registry_task_schedule_run_now" "az_acr_task_run" {
  container_registry_task_id = azurerm_container_registry_task.az_acr_task.id
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

# The interface sits in the subnet of the network's second address prefix, so
# the machine takes an address there.
resource "azurerm_network_interface" "az_vm_nic" {
  name                = "tf-azrm-vm-nic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.second_prefix.id
    private_ip_address_allocation = "Dynamic"
    primary                       = true
  }
}

output "azrm_vm_nic_private_ip_address" {
  value = azurerm_network_interface.az_vm_nic.private_ip_address
}

# Each IP configuration holds an address of its own on the realized interface.
resource "azurerm_network_interface" "az_multi_ip_nic" {
  name                = "tf-azrm-multi-ip-nic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  ip_configuration {
    name                          = "primary"
    subnet_id                     = azurerm_subnet.main.id
    private_ip_address_allocation = "Dynamic"
    primary                       = true
  }

  ip_configuration {
    name                          = "secondary"
    subnet_id                     = azurerm_subnet.main.id
    private_ip_address_allocation = "Static"
    private_ip_address            = "10.0.1.40"
  }
}

output "azrm_multi_ip_nic_private_ip_addresses" {
  value = azurerm_network_interface.az_multi_ip_nic.private_ip_addresses
}

output "azrm_multi_ip_nic_primary_private_ip_address" {
  value = azurerm_network_interface.az_multi_ip_nic.private_ip_address
}

# Resolved through the marketplace catalog under
# locations/{location}/publishers.
data "azurerm_platform_image" "az_ubuntu" {
  location  = azurerm_resource_group.az_rg.location
  publisher = "Canonical"
  offer     = "ubuntu-24_04-lts"
  sku       = "server"
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

  # Read back below through VirtualMachineImages_List and
  # VirtualMachineImages_Get.
  source_image_reference {
    publisher = data.azurerm_platform_image.az_ubuntu.publisher
    offer     = data.azurerm_platform_image.az_ubuntu.offer
    sku       = data.azurerm_platform_image.az_ubuntu.sku
    version   = "latest"
  }

  # cloud-init runs the script once at first boot; the extension below reads
  # what it wrote.
  custom_data = base64encode("#!/bin/sh\necho tf-custom-data > /var/tmp/tf-custom-data\n")

  # RetrieveBootDiagnosticsData reads the console log from this storage account.
  # A member the model dropped would read back missing and show as drift in the
  # follow-up plan.
  boot_diagnostics {
    storage_account_uri = azurerm_storage_account.main.primary_blob_endpoint
  }

  # The provider reads the principal back from identity.principalId, and the
  # Instance Metadata Service inside the machine mints tokens for it.
  identity {
    type = "SystemAssigned"
  }
}

# The machine's one sub-surface Terraform expresses:
# virtualMachines/{vm}/extensions/{name}. The provider wraps no resource for
# the patch and capture verbs, and no configuration lists machines by location.
resource "azurerm_virtual_machine_extension" "az_vm_extension" {
  name                 = "tf-azrm-vm-extension"
  virtual_machine_id   = azurerm_linux_virtual_machine.az_vm.id
  publisher            = "Microsoft.Azure.Extensions"
  type                 = "CustomScript"
  type_handler_version = "2.1"

  settings = jsonencode({
    commandToExecute = "cat /var/tmp/tf-custom-data"
  })

  tags = {
    environment = "test"
  }
}

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

# A Container Apps environment requires a workspace for log ingestion.
resource "azurerm_log_analytics_workspace" "az_law" {
  name                = "tf-azrm-law"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}

# The data collection rule below writes this table through the Logs Ingestion
# API.
resource "azurerm_log_analytics_workspace_table_custom_log" "az_law_table" {
  name         = "TfEvents_CL"
  workspace_id = azurerm_log_analytics_workspace.az_law.id

  column {
    name = "TimeGenerated"
    type = "dateTime"
  }
  column {
    name = "Message"
    type = "string"
  }
}

resource "azurerm_monitor_data_collection_endpoint" "az_dce" {
  name                = "tf-azrm-dce"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
}

resource "azurerm_monitor_data_collection_rule" "az_dcr" {
  name                        = "tf-azrm-dcr"
  resource_group_name         = azurerm_resource_group.az_rg.name
  location                    = azurerm_resource_group.az_rg.location
  data_collection_endpoint_id = azurerm_monitor_data_collection_endpoint.az_dce.id

  destinations {
    log_analytics {
      name                  = "law"
      workspace_resource_id = azurerm_log_analytics_workspace.az_law.id
    }
  }

  data_flow {
    streams       = ["Custom-TfEventsRaw"]
    destinations  = ["law"]
    transform_kql = "source | extend TimeGenerated = todatetime(Time) | project TimeGenerated, Message"
    output_stream = "Custom-${azurerm_log_analytics_workspace_table_custom_log.az_law_table.name}"
  }

  stream_declaration {
    stream_name = "Custom-TfEventsRaw"
    column {
      name = "Time"
      type = "string"
    }
    column {
      name = "Message"
      type = "string"
    }
  }
}

resource "azurerm_application_insights" "az_appins" {
  name                = "tf-azrm-ai"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  application_type    = "web"
  workspace_id        = azurerm_log_analytics_workspace.az_law.id
}

resource "azurerm_container_app_environment" "az_cae" {
  name                       = "tf-azrm-cae"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  logs_destination           = "log-analytics"
  log_analytics_workspace_id = azurerm_log_analytics_workspace.az_law.id
}

# One container with 0.25 vCPU and 0.5Gi memory, ingress disabled.
resource "azurerm_container_app" "az_ca" {
  name                         = "tf-azrm-ca"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  resource_group_name          = azurerm_resource_group.az_rg.name
  revision_mode                = "Single"

  template {
    container {
      name    = "main"
      image   = "mirror.gcr.io/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["sh", "-c", "sleep infinity"]
    }
  }
}

# A multiple-revision Container App whose ingress sends all traffic to the
# latest revision and keeps up to five inactive revisions. Its container
# listens on the target port, which the default startup probe dials.
resource "azurerm_container_app" "az_ca_multi" {
  name                         = "tf-azrm-ca-multi"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  resource_group_name          = azurerm_resource_group.az_rg.name
  revision_mode                = "Multiple"
  max_inactive_revisions       = 5

  ingress {
    target_port = 80
    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }

  template {
    container {
      name    = "main"
      image   = "mirror.gcr.io/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["nc", "-lk", "-p", "80", "-e", "true"]
    }
  }
}

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
      image   = "mirror.gcr.io/library/alpine:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["sh", "-c", "echo hello && exit 0"]
    }
  }
}

# The Azure Files mount definition an app or job references by name for a
# shared volume: the managedEnvironments/{env}/storages sub-resource.
resource "azurerm_container_app_environment_storage" "az_cae_storage" {
  name                         = "tfazrmcaestorage"
  container_app_environment_id = azurerm_container_app_environment.az_cae.id
  account_name                 = azurerm_storage_account.az_st.name
  share_name                   = "tfazrmshare"
  access_key                   = azurerm_storage_account.az_st.primary_access_key
  access_mode                  = "ReadWrite"
}

resource "azurerm_logic_app_workflow" "az_logic" {
  name                = "tf-azrm-logic"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location

  tags = {
    env = "terraform"
  }
}

# Uses the alpine image the harness pre-pulled, so the simulator starts the
# container without network access during apply.
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

resource "azurerm_service_plan" "az_sp" {
  name                = "tf-azrm-sp"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  sku_name            = "Y1"
}

# A dedicated account keeps the function app's wiring independent of the
# general-purpose account above.
resource "azurerm_storage_account" "az_st" {
  name                     = "tfazrmst12345"
  resource_group_name      = azurerm_resource_group.az_rg.name
  location                 = azurerm_resource_group.az_rg.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

# AzureRM 5 resolves the container's data-plane endpoints from the storage
# account ID, so the simulator must emit azurerm-parseable
# {account}.{service}.{suffix} endpoints and matching /metadata/endpoints
# storage suffixes.
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

# Azure Files directories: Create, Get and Delete Directory make a nested path
# inside a share, which a mounting Container Apps or Functions workload walks.
# The provider reads each directory back on every refresh, so the idempotency
# plan covers Get Directory Properties too.
resource "azurerm_storage_share_directory" "az_st_share_dir" {
  name              = "build"
  storage_share_url = azurerm_storage_share.az_st_share.url
}

resource "azurerm_storage_share_directory" "az_st_share_subdir" {
  name              = "${azurerm_storage_share_directory.az_st_share_dir.name}/artifacts"
  storage_share_url = azurerm_storage_share.az_st_share.url
}

# terraform-provider-azurerm's FlattenBackupConfig dereferences the backup
# schedule's start time before its nil check; Azure defaults the start time at
# save, so the simulator must too. storage_account_url is the provider's
# documented shape: an account SAS from data.azurerm_storage_account_sas, which
# the storage data plane verifies against the account keys.
resource "azurerm_storage_container" "az_fa_backups" {
  name               = "tf-azrm-fa-backups"
  storage_account_id = azurerm_storage_account.az_st.id
}

data "azurerm_storage_account_sas" "az_fa_backup" {
  connection_string = azurerm_storage_account.az_st.primary_connection_string
  https_only        = false
  signed_version    = "2022-11-02"
  resource_types {
    service   = false
    container = true
    object    = true
  }
  services {
    blob  = true
    queue = false
    table = false
    file  = false
  }
  start  = "2026-01-01T00:00:00Z"
  expiry = "2030-01-01T00:00:00Z"
  permissions {
    read    = true
    write   = true
    delete  = true
    list    = true
    add     = false
    create  = true
    update  = false
    process = false
    tag     = false
    filter  = false
  }
}

resource "azurerm_linux_function_app" "az_fa" {
  name                       = "tf-azrm-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  site_config {}
}

# Azure refuses a backup configuration on a Consumption plan, and az_sp is Y1,
# so backups need a dedicated Basic-or-higher plan.
resource "azurerm_service_plan" "az_backup_sp" {
  name                = "tf-azrm-backup-sp"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  sku_name            = "S1"
}

resource "azurerm_linux_function_app" "az_backup_fa" {
  name                       = "tf-azrm-backup-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_backup_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  site_config {}

  backup {
    name                = "tf-azrm-fa-nightly"
    storage_account_url = "${azurerm_storage_account.az_st.primary_blob_endpoint}${azurerm_storage_container.az_fa_backups.name}${data.azurerm_storage_account_sas.az_fa_backup.sas}"
    schedule {
      frequency_interval    = 1
      frequency_unit        = "Day"
      retention_period_days = 7
    }
  }
}

# application_stack.docker becomes linuxFxVersion
# "DOCKER|mirror.gcr.io/library/alpine:latest", and app_command_line is the
# startup command run on the image's entrypoint. WEBSITES_PORT names the port
# requests go to; busybox nc answers each connection after reading the request
# headers.
resource "azurerm_linux_function_app" "az_container_fa" {
  name                       = "tf-azrm-container-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_backup_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  app_settings = {
    WEBSITES_PORT = "8080"
  }

  site_config {
    app_command_line = "nc -lk -p 8080 -e sh -c \"while read -r l && [ $${#l} -gt 1 ]; do :; done; printf 'HTTP/1.1 200 OK\\r\\nConnection: close\\r\\n\\r\\n'; echo from-the-terraform-startup-command\""

    application_stack {
      docker {
        registry_url = "https://mirror.gcr.io"
        image_name   = "library/alpine"
        image_tag    = "latest"
      }
    }
  }
}

# application_stack becomes linuxFxVersion "NODE|20-lts" / "PYTHON|3.12", and
# the platform runs that stack's image on the app's content. The Node app runs
# the zip package WEBSITE_RUN_FROM_PACKAGE names; the Python app has no content
# and serves the platform's default page.
resource "azurerm_service_plan" "az_web_sp" {
  name                = "tf-azrm-web-sp"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  os_type             = "Linux"
  sku_name            = "B1"
}

resource "azurerm_linux_web_app" "az_node_app" {
  name                = "tf-azrm-node-app"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  service_plan_id     = azurerm_service_plan.az_web_sp.id

  app_settings = {
    WEBSITE_RUN_FROM_PACKAGE = var.node_app_package_url
  }

  site_config {
    application_stack {
      node_version = "20-lts"
    }
  }
}

# Microsoft.Insights/diagnosticSettings on the site send its console logs to
# the Log Analytics workspace.
resource "azurerm_monitor_diagnostic_setting" "az_node_app_logs" {
  name                       = "tf-azrm-node-app-logs"
  target_resource_id         = azurerm_linux_web_app.az_node_app.id
  log_analytics_workspace_id = azurerm_log_analytics_workspace.az_law.id

  enabled_log {
    category = "AppServiceConsoleLogs"
  }
}

output "azrm_node_app_diagnostic_setting_id" {
  value = azurerm_monitor_diagnostic_setting.az_node_app_logs.id
}

# zip_deploy_file publishes to the SCM site: the provider reads the Repository
# hostname from hostNameSslStates and the publishing credentials, warms Kudu at
# /deployments, posts to /api/zipdeploy?isAsync=true and polls
# /api/deployments/latest.
resource "azurerm_linux_web_app" "az_zip_app" {
  name                = "tf-azrm-zip-app"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  service_plan_id     = azurerm_service_plan.az_web_sp.id
  zip_deploy_file     = var.zip_app_package_path

  app_settings = {
    WEBSITE_RUN_FROM_PACKAGE = "1"
  }

  site_config {
    application_stack {
      node_version = "20-lts"
    }
  }
}

resource "azurerm_linux_web_app" "az_python_app" {
  name                = "tf-azrm-python-app"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  service_plan_id     = azurerm_service_plan.az_web_sp.id

  site_config {
    application_stack {
      python_version = "3.12"
    }
  }
}

# application_stack becomes linuxFxVersion "Node|22", and the platform runs the
# Azure Functions host image on the WEBSITE_RUN_FROM_PACKAGE package. With
# AzureWebJobsSecretStorageType "files" the host keeps its keys in its file
# secret store, where the host key data source reads them.
resource "azurerm_linux_function_app" "az_node_fa" {
  name                       = "tf-azrm-node-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_web_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  app_settings = {
    WEBSITE_RUN_FROM_PACKAGE      = var.node_function_package_url
    AzureWebJobsSecretStorageType = "files"
  }

  site_config {
    application_stack {
      node_version = "22"
    }
  }
}

data "azurerm_function_app_host_keys" "az_node_fa" {
  name                = azurerm_linux_function_app.az_node_fa.name
  resource_group_name = azurerm_resource_group.az_rg.name
}

# Under the default secret store the host keeps its keys in the
# azure-webjobs-secrets container of the AzureWebJobsStorage account, where the
# host key data source reads them.
resource "azurerm_linux_function_app" "az_node_blob_fa" {
  name                       = "tf-azrm-node-blob-fa"
  resource_group_name        = azurerm_resource_group.az_rg.name
  location                   = azurerm_resource_group.az_rg.location
  service_plan_id            = azurerm_service_plan.az_web_sp.id
  storage_account_name       = azurerm_storage_account.az_st.name
  storage_account_access_key = azurerm_storage_account.az_st.primary_access_key

  site_config {
    application_stack {
      node_version = "22"
    }
  }
}

data "azurerm_function_app_host_keys" "az_node_blob_fa" {
  name                = azurerm_linux_function_app.az_node_blob_fa.name
  resource_group_name = azurerm_resource_group.az_rg.name
}

# The provider PUTs the DER blob and reads it back on every plan, so the
# simulator must derive and round-trip the SHA-1 thumbprint. The blob is a
# base64 self-signed X.509 certificate.
resource "azurerm_app_service_public_certificate" "az_fa_pubcert" {
  resource_group_name  = azurerm_resource_group.az_rg.name
  app_service_name     = azurerm_linux_function_app.az_fa.name
  certificate_name     = "tf-azrm-pubcert"
  certificate_location = "CurrentUserMy"
  blob                 = "MIIBODCB36ADAgECAgEBMAoGCCqGSM49BAMCMCUxIzAhBgNVBAMTGnNvY2tlcmxlc3Mtc2ltLXB1YmxpYy1jZXJ0MCAXDTI2MDEwMTAwMDAwMFoYDzIxMjYwMTAxMDAwMDAwWjAlMSMwIQYDVQQDExpzb2NrZXJsZXNzLXNpbS1wdWJsaWMtY2VydDBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABIYJeLik0TOyqjYwfwDiqAkGzAbpepEjv4QKPOTTI7OH598l1LPkSwMNslhaMITB4V3UHtod+Y5SmGM1nJeCdZEwCgYIKoZIzj0EAwIDSAAwRQIhALwq3le+aDLA2FJZr4hYiJvJ1PNpq5PUlNdrn4t6XY9CAiBQKGsAY1MrTtkXxK0tuItu4cbF85xw7RQajq5/ubpXGg=="
}

# The provider PUTs the base64 PFX with its password and reads it back on
# every plan, so the simulator must parse the PKCS#12 payload and round-trip
# the derived thumbprint, subject, host names and validity. The blob is a
# self-signed certificate for tf.stage4.sockerless.test (valid 2026-2126,
# SHA-1 C573212158F1E07C7C6BC2370AFB84A17EDCDAFB) under the password below.
resource "azurerm_app_service_certificate" "az_fa_cert" {
  name                = "tf-azrm-cert"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  pfx_blob            = "MIIEPgIBAzCCA+4GCSqGSIb3DQEHAaCCA98EggPbMIID1zCCAogGCSqGSIb3DQEHBqCCAnkwggJ1AgEAMIICbgYJKoZIhvcNAQcBMF0GCSqGSIb3DQEFDTBQMC8GCSqGSIb3DQEFDDAiBBBTHUD6XqhPmLm/BE9KutFZAgIIADAKBggqhkiG9w0CCTAdBglghkgBZQMEASoEEMtthVm+vmX0KBzz5aQDGXaAggIAr/GYJfmbTKh6RhrrNTVlIvRjvP43i0+qDQQUwulTBiCxxcd0fHE6mtCrIr4drogKhjoHLweM8Upi5V7xBc/ljIpEaOY9KOw+hAl36WA7Z+5+AYWmiTGwyIze+//GrzVN66u9bPPBxDevdCke/HjoWAs+XYTcy1En4ifVswtx/PKec4VFp45GWZPeCVgpfAM60AO//KnWl2emyn0PaDW6KX6x8+qOo76zkz+SQS2hrbiyegtP7J37TG2CaYAJzNbMW6K4T9ewIiC/rB+eOIhBxAmymkBEb790EPrFcmPyKRE1YcVRDOgfIuSUeCD8vCcvp3+JwjpTsLerrjCFC84rj141NK71XF2/P4gY8WhDJd4wI7RSOj2iEF52QBnh/fWPx0AtFJtg8mZUJW1hbVmHh7HN2YSfRYZMfwCcpTV+Ottz+5SAkDRoDDZFUPXL8ZygPcIkQYyepKhxLkKAxIkuEX5A3lAvVhbWjh4fCaKa6PGRnqctx+X38cLBwKjx+35tBX8aImZ9SLZfqje9C/V72lRWMddAiegm5iXwz8JymClmGeha7tIztJXukUolvvSEkRoH8MdqXnj3BTr7c1cp2XsEB6RM57qF1B5loDQC1QvKf2qbktUNkjBCOra8jqoRVwNCwJaz+VmGZis9tBnDvAb7sWxr5Zg3Q+EK/Rlxb+EwggFHBgkqhkiG9w0BBwGgggE4BIIBNDCCATAwggEsBgsqhkiG9w0BDAoBAqCB9TCB8jBdBgkqhkiG9w0BBQ0wUDAvBgkqhkiG9w0BBQwwIgQQ4uMr6sq0cwmwc1kSEAsYcwICCAAwCgYIKoZIhvcNAgkwHQYJYIZIAWUDBAEqBBDR53wNKCFWDO404+7T/LYXBIGQqMJ/FCAgt5cRG8asbNxTOuNEmHTDTuZflhNM/Z6C2WRsJtyOWCnKf2oJFID2N2AlLLypLcAclsEDL3T0q4ANB8zsScPWM5aMVaAk0j8IPide5mW9ZXQVgksZjuADCTz0SvFQiqtAzsIiSbOPTi8kivz4QkZz65G4tOtU6QsnlNloO0oVCIdA1O45tcS0Eq0eMSUwIwYJKoZIhvcNAQkVMRYEFMVzISFY8eB8fGvCNwr7hKF+3Nr7MEcwLzALBglghkgBZQMEAgEEICjLD5LMSOwqEqCggyCUXa5dZyJ7i8Q1L15fOIWs2GbzBBAqGwucn7r+L8oV4q56rkLDAgIIAA=="
  password            = "sockerless"
}

# The CNAME points the hostname at the app's default hostname: the ownership
# proof WebApps_AnalyzeCustomHostname reads from the same DNS state.
resource "azurerm_dns_cname_record" "az_fa_cname" {
  name                = "fa"
  zone_name           = azurerm_dns_zone.az_dns.name
  resource_group_name = azurerm_resource_group.az_rg.name
  ttl                 = 300
  record              = azurerm_linux_function_app.az_fa.default_hostname
}

resource "azurerm_app_service_custom_hostname_binding" "az_fa_hostname" {
  hostname            = "${azurerm_dns_cname_record.az_fa_cname.name}.${azurerm_dns_zone.az_dns.name}"
  app_service_name    = azurerm_linux_function_app.az_fa.name
  resource_group_name = azurerm_resource_group.az_rg.name
}

# The provider re-PUTs the hostname binding with the certificate's thumbprint
# and SNI state, then reads the binding and the certificate back.
resource "azurerm_app_service_certificate_binding" "az_fa_cert_binding" {
  hostname_binding_id = azurerm_app_service_custom_hostname_binding.az_fa_hostname.id
  certificate_id      = azurerm_app_service_certificate.az_fa_cert.id
  ssl_state           = "SniEnabled"
}

# Regional virtual network integration (Microsoft.Web/sites/networkConfig/
# virtualNetwork) needs an Elastic Premium or dedicated plan and a subnet
# delegated to Microsoft.Web/serverFarms. The provider reads it back on every
# plan, so the subnet delegation, its actions and the connection's
# subnetResourceId must round-trip.
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

  # The spelling azurerm 5 documents. The standalone
  # azurerm_app_service_virtual_network_swift_connection is deprecated because
  # the site reports its integration as virtual_network_subnet_id: an app that
  # does not declare it plans the integration away on every refresh.
  virtual_network_subnet_id = azurerm_subnet.az_swift_subnet.id

  site_config {}
}

# Creating the endpoint writes the connection into the site's own collection,
# auto-approved because both share the subscription.
resource "azurerm_private_endpoint" "az_site_pe" {
  name                = "tf-azrm-site-pe"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  subnet_id           = azurerm_subnet.az_pl_pe_subnet.id

  private_service_connection {
    name                           = "tf-azrm-site-pe"
    is_manual_connection           = false
    private_connection_resource_id = azurerm_linux_function_app.az_swift_fa.id
    subresource_names              = ["sites"]
  }
}

# The provider PUTs the site (an LRO) and reads back the resource, its
# application settings (POST listAppSettings) and its deployment secrets (POST
# listSecrets, which fills api_key), so all three must round-trip.
resource "azurerm_static_web_app" "az_swa" {
  name                = "tf-azrm-swa"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  sku_tier            = "Standard"
  sku_size            = "Standard"

  app_settings = {
    PING = "pong"
  }

  tags = {
    env = "terraform"
  }
}

# The CNAME points the hostname at the app's default host name, and the domain
# create validates against that record.
resource "azurerm_dns_cname_record" "az_swa_cname" {
  name                = "swa"
  zone_name           = azurerm_dns_zone.az_dns.name
  resource_group_name = azurerm_resource_group.az_rg.name
  ttl                 = 300
  record              = azurerm_static_web_app.az_swa.default_host_name
}

resource "azurerm_static_web_app_custom_domain" "az_swa_domain" {
  static_web_app_id = azurerm_static_web_app.az_swa.id
  domain_name       = "${azurerm_dns_cname_record.az_swa_cname.name}.${azurerm_dns_zone.az_dns.name}"
  validation_type   = "cname-delegation"
}

# Links the existing Linux function app above; the simulator resolves it
# against its sites.
resource "azurerm_static_web_app_function_app_registration" "az_swa_fn" {
  static_web_app_id = azurerm_static_web_app.az_swa.id
  function_app_id   = azurerm_linux_function_app.az_fa.id
}

# The secret resource drives the azsecrets challenge-then-retry handshake: an
# unauthenticated PUT, the WWW-Authenticate challenge (whose authorization URL
# must split into at least four segments or the SDK's parseTenant panics with
# `index out of range [3]`), a token, and the retried PUT.
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

# The principal Terraform signs in as writes the vault's objects, so an access
# policy grants its object ID.
data "azurerm_client_config" "current" {}

resource "azurerm_key_vault_access_policy" "az_kv_policy" {
  key_vault_id = azurerm_key_vault.az_kv.id
  tenant_id    = data.azurerm_client_config.current.tenant_id
  object_id    = data.azurerm_client_config.current.object_id

  key_permissions         = ["Get", "List", "Create", "Update", "Delete", "Backup", "Restore", "Import", "Recover", "Purge", "Sign", "Verify", "Encrypt", "Decrypt", "WrapKey", "UnwrapKey", "GetRotationPolicy", "SetRotationPolicy"]
  secret_permissions      = ["Get", "List", "Set", "Delete", "Backup", "Restore", "Recover", "Purge"]
  certificate_permissions = ["Get", "List", "Create", "Update", "Delete", "Import", "Backup", "Restore", "Recover", "Purge"]
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

# Consumption_0 is the SKU the provider does not gate behind the
# portalsettings and tenant/access create choreography of the
# Developer, Standard and Premium tiers.
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

# Azure Private Link: a private link service in front of the load balancer's
# frontend, and a private endpoint in its own subnet. The endpoint's create
# opens a connection on the service whose state the provider reads back on
# every plan, so both must address one object. A second endpoint targets the
# key vault, and its private DNS zone group publishes the vault's record into
# the zone the zone's record surface serves.

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

  # The provider records whatever address the service assigns and refuses a
  # later plan that removes it, so pin the address.
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

output "azrm_acr_login_server" {
  value = azurerm_container_registry.az_acr.login_server
}

output "azrm_acr_admin_username" {
  value = azurerm_container_registry.az_acr.admin_username
}

output "azrm_acr_admin_password" {
  value     = azurerm_container_registry.az_acr.admin_password
  sensitive = true
}

output "azrm_acr_task_id" {
  value = azurerm_container_registry_task.az_acr_task.id
}

output "azrm_acr_task_run_now_id" {
  value = azurerm_container_registry_task_schedule_run_now.az_acr_task_run.id
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

output "azrm_vm_principal_id" {
  value = azurerm_linux_virtual_machine.az_vm.identity[0].principal_id
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

output "azrm_law_table_id" {
  value = azurerm_log_analytics_workspace_table_custom_log.az_law_table.id
}

output "azrm_dce_logs_ingestion_endpoint" {
  value = azurerm_monitor_data_collection_endpoint.az_dce.logs_ingestion_endpoint
}

output "azrm_dcr_immutable_id" {
  value = azurerm_monitor_data_collection_rule.az_dcr.immutable_id
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

output "azrm_container_app_multi_latest_revision_name" {
  value = azurerm_container_app.az_ca_multi.latest_revision_name
}

output "azrm_container_app_multi_latest_revision_fqdn" {
  value = azurerm_container_app.az_ca_multi.latest_revision_fqdn
}

output "azrm_container_app_multi_ingress_fqdn" {
  value = azurerm_container_app.az_ca_multi.ingress[0].fqdn
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

output "azrm_container_function_app_hostname" {
  value = azurerm_linux_function_app.az_container_fa.default_hostname
}

output "azrm_node_web_app_hostname" {
  value = azurerm_linux_web_app.az_node_app.default_hostname
}

output "azrm_zip_web_app_hostname" {
  value = azurerm_linux_web_app.az_zip_app.default_hostname
}

output "azrm_node_function_app_hostname" {
  value = azurerm_linux_function_app.az_node_fa.default_hostname
}

output "azrm_node_function_app_default_key" {
  value     = data.azurerm_function_app_host_keys.az_node_fa.default_function_key
  sensitive = true
}

output "azrm_node_blob_function_app_master_key" {
  value     = data.azurerm_function_app_host_keys.az_node_blob_fa.primary_key
  sensitive = true
}

output "azrm_python_web_app_hostname" {
  value = azurerm_linux_web_app.az_python_app.default_hostname
}

output "azrm_container_function_app_command_line" {
  value = azurerm_linux_function_app.az_container_fa.site_config[0].app_command_line
}

output "azrm_static_web_app_id" {
  value = azurerm_static_web_app.az_swa.id
}

output "azrm_static_web_app_default_hostname" {
  value = azurerm_static_web_app.az_swa.default_host_name
}

output "azrm_static_web_app_api_key" {
  value     = azurerm_static_web_app.az_swa.api_key
  sensitive = true
}

output "azrm_static_web_app_custom_domain_id" {
  value = azurerm_static_web_app_custom_domain.az_swa_domain.id
}

output "azrm_static_web_app_function_registration_id" {
  value = azurerm_static_web_app_function_app_registration.az_swa_fn.id
}

output "azrm_public_certificate_thumbprint" {
  value = azurerm_app_service_public_certificate.az_fa_pubcert.thumbprint
}

output "azrm_swift_subnet_id" {
  value = azurerm_subnet.az_swift_subnet.id
}

output "azrm_swift_integration_subnet_id" {
  value = azurerm_linux_function_app.az_swift_fa.virtual_network_subnet_id
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

# The listener, URL path map and request routing rule are the routing program
# the gateway's data plane executes: a request matching no path rule goes to
# the default pool, and /api/* to the other.

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
    ip_addresses = [var.appgw_echo_ip]
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
    port                  = var.appgw_echo_port
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

# The flow log configuration of a network security group is the record the
# watcher's target-addressed flow log operations read and write.

resource "azurerm_network_watcher" "az_nw" {
  name                = "tf-azrm-network-watcher"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  tags = {
    team = "network"
  }
}

# The manager declares the subscriptions it governs and the configuration
# kinds it may manage there.

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

output "azrm_application_gateway_frontend_ip" {
  value = azurerm_public_ip.az_appgw_pip.ip_address
}

output "azrm_application_gateway_backend_pool_ids" {
  value = [for pool in azurerm_application_gateway.az_appgw.backend_address_pool : pool.id]
}

output "azrm_application_gateway_listener_ids" {
  value = [for listener in azurerm_application_gateway.az_appgw.http_listener : listener.id]
}

output "azrm_platform_image_version" {
  value = data.azurerm_platform_image.az_ubuntu.version
}

output "azrm_network_watcher_id" {
  value = azurerm_network_watcher.az_nw.id
}

output "azrm_network_manager_id" {
  value = azurerm_network_manager.az_netman.id
}

output "azrm_network_manager_scope_accesses" {
  value = azurerm_network_manager.az_netman.scope_accesses
}

# An App Service Environment v3 is carved out of a subnet delegated to
# Microsoft.Web/hostingEnvironments. The provider reads back the domain suffix,
# addresses and inbound dependencies the service computes, so the apply is
# idempotent only if each comes back the same on refresh.
resource "azurerm_virtual_network" "az_ase_vnet" {
  name                = "tf-azrm-ase-vnet"
  resource_group_name = azurerm_resource_group.az_rg.name
  location            = azurerm_resource_group.az_rg.location
  address_space       = ["10.95.0.0/16"]
}

resource "azurerm_subnet" "az_ase_subnet" {
  name                 = "tf-azrm-ase-subnet"
  resource_group_name  = azurerm_resource_group.az_rg.name
  virtual_network_name = azurerm_virtual_network.az_ase_vnet.name
  address_prefixes     = ["10.95.1.0/24"]

  delegation {
    name = "ase-delegation"
    service_delegation {
      name    = "Microsoft.Web/hostingEnvironments"
      actions = ["Microsoft.Network/virtualNetworks/subnets/action"]
    }
  }
}

resource "azurerm_app_service_environment_v3" "az_ase" {
  name                         = "tf-azrm-ase"
  resource_group_name          = azurerm_resource_group.az_rg.name
  subnet_id                    = azurerm_subnet.az_ase_subnet.id
  internal_load_balancing_mode = "Web, Publishing"
  zone_redundant               = false

  tags = {
    env = "tf-test"
  }
}

output "azrm_app_service_environment_id" {
  value = azurerm_app_service_environment_v3.az_ase.id
}

# Computed by the service from the environment's name, load-balancing mode and
# subnet.
output "azrm_app_service_environment_dns_suffix" {
  value = azurerm_app_service_environment_v3.az_ase.dns_suffix
}

output "azrm_app_service_environment_internal_inbound_ip_addresses" {
  value = azurerm_app_service_environment_v3.az_ase.internal_inbound_ip_addresses
}
