# The Azure stack of docs/tutorial.md, the counterpart of the AWS one: a
# virtual network with a NAT gateway, a Linux virtual machine, a Container App,
# a Blob Storage container, an Azure Cosmos DB container, a Service Bus queue,
# an Azure Functions app, and a Log Analytics workspace with Application
# Insights.

terraform {
  required_providers {
    archive = {
      source  = "hashicorp/archive"
      version = "2.8.1"
    }
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.11.0"
    }
  }
}

variable "endpoint" {
  description = "The HTTPS gateway in front of the Azure simulator."
  type        = string
  default     = "https://azure.sockerless.localhost:8443"
}

provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"

  metadata_host = trimprefix(var.endpoint, "https://")

  resource_provider_registrations = "none"

  features {}
}

resource "azurerm_resource_group" "main" {
  name     = "tutorial"
  location = "eastus"
}

# Network: a virtual network whose subnet reaches the internet through a NAT
# gateway on a public IP prefix.

resource "azurerm_virtual_network" "main" {
  name                = "tutorial"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  address_space       = ["10.97.0.0/16"]
}

resource "azurerm_subnet" "private" {
  name                 = "private"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.97.1.0/24"]
}

resource "azurerm_public_ip_prefix" "nat" {
  name                = "tutorial-nat"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  prefix_length       = 30
  sku                 = "Standard"
}

resource "azurerm_nat_gateway" "main" {
  name                = "tutorial"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  sku_name            = "Standard"
}

resource "azurerm_nat_gateway_public_ip_prefix_association" "main" {
  nat_gateway_id      = azurerm_nat_gateway.main.id
  public_ip_prefix_id = azurerm_public_ip_prefix.nat.id
}

resource "azurerm_subnet_nat_gateway_association" "private" {
  subnet_id      = azurerm_subnet.private.id
  nat_gateway_id = azurerm_nat_gateway.main.id
}

resource "azurerm_network_security_group" "app" {
  name                = "tutorial-app"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location

  security_rule {
    name                       = "allow-app"
    priority                   = 100
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "8080"
    source_address_prefix      = "VirtualNetwork"
    destination_address_prefix = "*"
  }
}

# Compute: a Linux virtual machine from the Ubuntu 24.04 LTS image.

data "azurerm_platform_image" "ubuntu" {
  location  = azurerm_resource_group.main.location
  publisher = "Canonical"
  offer     = "ubuntu-24_04-lts"
  sku       = "server"
}

resource "azurerm_network_interface" "app" {
  name                = "tutorial-app"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.private.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_network_interface_security_group_association" "app" {
  network_interface_id      = azurerm_network_interface.app.id
  network_security_group_id = azurerm_network_security_group.app.id
}

resource "azurerm_linux_virtual_machine" "app" {
  name                            = "tutorial"
  resource_group_name             = azurerm_resource_group.main.name
  location                        = azurerm_resource_group.main.location
  size                            = "Standard_B1s"
  admin_username                  = "azureuser"
  admin_password                  = "Tutorial-password-12345!"
  disable_password_authentication = false
  network_interface_ids           = [azurerm_network_interface.app.id]

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = data.azurerm_platform_image.ubuntu.publisher
    offer     = data.azurerm_platform_image.ubuntu.offer
    sku       = data.azurerm_platform_image.ubuntu.sku
    version   = "latest"
  }

  depends_on = [azurerm_subnet_nat_gateway_association.private]
}

# Monitoring: a Log Analytics workspace, which the Container Apps environment
# sends its logs to, and Application Insights for the function app.

resource "azurerm_log_analytics_workspace" "main" {
  name                = "tutorial"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
}

resource "azurerm_application_insights" "main" {
  name                = "tutorial"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  application_type    = "web"
  workspace_id        = azurerm_log_analytics_workspace.main.id
}

# Containers: a Container App with external ingress, the counterpart of an
# Amazon ECS service on AWS Fargate.

resource "azurerm_container_app_environment" "main" {
  name                       = "tutorial"
  resource_group_name        = azurerm_resource_group.main.name
  location                   = azurerm_resource_group.main.location
  logs_destination           = "log-analytics"
  log_analytics_workspace_id = azurerm_log_analytics_workspace.main.id
}

resource "azurerm_container_app" "web" {
  name                         = "tutorial-web"
  container_app_environment_id = azurerm_container_app_environment.main.id
  resource_group_name          = azurerm_resource_group.main.name
  revision_mode                = "Single"

  ingress {
    external_enabled = true
    target_port      = 8080

    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }

  template {
    container {
      name    = "web"
      image   = "mirror.gcr.io/library/busybox:latest"
      cpu     = 0.25
      memory  = "0.5Gi"
      command = ["httpd", "-f", "-p", "8080"]
    }
  }
}

# Storage: a Blob Storage container holding one blob, and a Cosmos DB for
# NoSQL container.

resource "azurerm_storage_account" "main" {
  name                     = "tutorialassets"
  resource_group_name      = azurerm_resource_group.main.name
  location                 = azurerm_resource_group.main.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

resource "azurerm_storage_container" "assets" {
  name                  = "assets"
  storage_account_id    = azurerm_storage_account.main.id
  container_access_type = "private"
}

resource "azurerm_storage_blob" "hello" {
  name                 = "hello.txt"
  storage_container_id = azurerm_storage_container.assets.id
  type                 = "Block"
  content_type         = "text/plain"
  source_content       = "Hello from Blob Storage"
}

resource "azurerm_cosmosdb_account" "main" {
  name                = "tutorial-orders"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  offer_type          = "Standard"
  kind                = "GlobalDocumentDB"

  consistency_policy {
    consistency_level = "Session"
  }

  geo_location {
    location          = azurerm_resource_group.main.location
    failover_priority = 0
  }
}

resource "azurerm_cosmosdb_sql_database" "main" {
  name                = "tutorial"
  resource_group_name = azurerm_resource_group.main.name
  account_name        = azurerm_cosmosdb_account.main.name
}

resource "azurerm_cosmosdb_sql_container" "orders" {
  name                = "orders"
  resource_group_name = azurerm_resource_group.main.name
  account_name        = azurerm_cosmosdb_account.main.name
  database_name       = azurerm_cosmosdb_sql_database.main.name
  partition_key_paths = ["/id"]
}

# Messaging and functions: a Service Bus queue, and a Node.js function app
# deployed from a zip package.

resource "azurerm_servicebus_namespace" "main" {
  name                = "tutorial-orders-ns"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  sku                 = "Standard"
}

resource "azurerm_servicebus_queue" "orders" {
  name                                 = "orders"
  namespace_id                         = azurerm_servicebus_namespace.main.id
  max_delivery_count                   = 3
  dead_lettering_on_message_expiration = true
}

data "archive_file" "worker" {
  type        = "zip"
  output_path = "${path.module}/.terraform/worker.zip"

  source {
    filename = "host.json"
    content  = jsonencode({ version = "2.0" })
  }

  source {
    filename = "hello/function.json"
    content = jsonencode({
      bindings = [
        { type = "httpTrigger", direction = "in", name = "req", authLevel = "function", methods = ["get"] },
        { type = "http", direction = "out", name = "res" },
      ]
    })
  }

  source {
    filename = "hello/index.js"
    content  = "module.exports = async function (context, req) { context.res = { body: \"hello from \" + process.env.WEBSITE_SITE_NAME }; };"
  }
}

resource "azurerm_service_plan" "functions" {
  name                = "tutorial-functions"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  os_type             = "Linux"
  sku_name            = "B1"
}

resource "azurerm_linux_function_app" "worker" {
  name                       = "tutorial-worker"
  resource_group_name        = azurerm_resource_group.main.name
  location                   = azurerm_resource_group.main.location
  service_plan_id            = azurerm_service_plan.functions.id
  storage_account_name       = azurerm_storage_account.main.name
  storage_account_access_key = azurerm_storage_account.main.primary_access_key
  zip_deploy_file            = data.archive_file.worker.output_path

  app_settings = {
    AzureWebJobsSecretStorageType = "files"
  }

  site_config {
    application_insights_connection_string = azurerm_application_insights.main.connection_string

    application_stack {
      node_version = "22"
    }
  }
}

data "azurerm_function_app_host_keys" "worker" {
  name                = azurerm_linux_function_app.worker.name
  resource_group_name = azurerm_resource_group.main.name
}

output "nat_gateway_ip_prefix" {
  value = azurerm_public_ip_prefix.nat.ip_prefix
}

output "vm_private_ip" {
  value = azurerm_linux_virtual_machine.app.private_ip_address
}

output "container_app_fqdn" {
  value = azurerm_container_app.web.latest_revision_fqdn
}

output "blob_url" {
  value = azurerm_storage_blob.hello.url
}

output "cosmos_container_id" {
  value = azurerm_cosmosdb_sql_container.orders.id
}

output "servicebus_queue_id" {
  value = azurerm_servicebus_queue.orders.id
}

output "function_hostname" {
  value = azurerm_linux_function_app.worker.default_hostname
}

output "function_key" {
  value     = data.azurerm_function_app_host_keys.worker.default_function_key
  sensitive = true
}

output "log_analytics_workspace_id" {
  value = azurerm_log_analytics_workspace.main.workspace_id
}
