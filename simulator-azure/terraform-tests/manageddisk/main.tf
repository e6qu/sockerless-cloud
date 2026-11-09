terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.13.0"
    }
  }
}

# Run by TestTerraformManagedDiskAttachment: an Empty managed disk attached to
# a running machine at LUN 10, which the guest writes through the path Azure
# gives the LUN, and which reads back Attached and held by the machine.

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

resource "azurerm_resource_group" "disk" {
  name     = "tf-azrm-disk-rg"
  location = "eastus"
}

resource "azurerm_virtual_network" "disk" {
  name                = "tf-azrm-disk-vnet"
  resource_group_name = azurerm_resource_group.disk.name
  location            = azurerm_resource_group.disk.location
  address_space       = ["10.98.0.0/16"]
}

resource "azurerm_subnet" "disk" {
  name                 = "tf-azrm-disk-subnet"
  resource_group_name  = azurerm_resource_group.disk.name
  virtual_network_name = azurerm_virtual_network.disk.name
  address_prefixes     = ["10.98.1.0/24"]
}

resource "azurerm_network_interface" "disk" {
  name                = "tf-azrm-disk-nic"
  resource_group_name = azurerm_resource_group.disk.name
  location            = azurerm_resource_group.disk.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.disk.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_linux_virtual_machine" "disk" {
  name                            = "tf-azrm-disk-vm"
  resource_group_name             = azurerm_resource_group.disk.name
  location                        = azurerm_resource_group.disk.location
  size                            = "Standard_B1s"
  admin_username                  = "azureuser"
  admin_password                  = "Str0ng-password-12345!"
  disable_password_authentication = false
  network_interface_ids           = [azurerm_network_interface.disk.id]

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Debian"
    offer     = "debian-12"
    sku       = "12"
    version   = "latest"
  }
}

resource "azurerm_managed_disk" "data" {
  name                 = "tf-azrm-data-disk"
  resource_group_name  = azurerm_resource_group.disk.name
  location             = azurerm_resource_group.disk.location
  storage_account_type = "StandardSSD_LRS"
  create_option        = "Empty"
  disk_size_gb         = 4

  tags = {
    purpose = "data"
  }
}

resource "azurerm_virtual_machine_data_disk_attachment" "data" {
  managed_disk_id    = azurerm_managed_disk.data.id
  virtual_machine_id = azurerm_linux_virtual_machine.disk.id
  lun                = 10
  caching            = "ReadWrite"
}

# The guest writes to the attached disk at the path its LUN names.
resource "azurerm_virtual_machine_extension" "write" {
  name                 = "tf-azrm-disk-write"
  virtual_machine_id   = azurerm_linux_virtual_machine.disk.id
  publisher            = "Microsoft.Azure.Extensions"
  type                 = "CustomScript"
  type_handler_version = "2.1"

  settings = jsonencode({
    commandToExecute = "echo tf-data-disk > /dev/disk/azure/scsi1/lun10/proof && cat /dev/disk/azure/scsi1/lun10/proof"
  })

  depends_on = [azurerm_virtual_machine_data_disk_attachment.data]
}

output "disk_id" {
  value = azurerm_managed_disk.data.id
}

output "attachment_id" {
  value = azurerm_virtual_machine_data_disk_attachment.data.id
}

output "vm_id" {
  value = azurerm_linux_virtual_machine.disk.id
}
