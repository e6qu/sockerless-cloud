terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "5.13.0"
    }
  }
}

# Run by TestTerraformComputeGalleryCapturesAMachine: a running machine
# captured into a specialized image version of an Azure Compute Gallery image
# definition, which reads back with the machine as its source.

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

resource "azurerm_resource_group" "gallery" {
  name     = "tf-azrm-gallery-rg"
  location = "eastus"
}

resource "azurerm_virtual_network" "gallery" {
  name                = "tf-azrm-gallery-vnet"
  resource_group_name = azurerm_resource_group.gallery.name
  location            = azurerm_resource_group.gallery.location
  address_space       = ["10.89.0.0/16"]
}

resource "azurerm_subnet" "gallery" {
  name                 = "tf-azrm-gallery-subnet"
  resource_group_name  = azurerm_resource_group.gallery.name
  virtual_network_name = azurerm_virtual_network.gallery.name
  address_prefixes     = ["10.89.1.0/24"]
}

resource "azurerm_network_interface" "gallery" {
  name                = "tf-azrm-gallery-nic"
  resource_group_name = azurerm_resource_group.gallery.name
  location            = azurerm_resource_group.gallery.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.gallery.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_linux_virtual_machine" "source" {
  name                            = "tf-azrm-gallery-vm"
  resource_group_name             = azurerm_resource_group.gallery.name
  location                        = azurerm_resource_group.gallery.location
  size                            = "Standard_B1s"
  admin_username                  = "azureuser"
  admin_password                  = "Str0ng-password-12345!"
  disable_password_authentication = false
  network_interface_ids           = [azurerm_network_interface.gallery.id]

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

resource "azurerm_shared_image_gallery" "gallery" {
  name                = "tf_azrm_gallery"
  resource_group_name = azurerm_resource_group.gallery.name
  location            = azurerm_resource_group.gallery.location
  description         = "Images captured by Terraform"

  tags = {
    purpose = "capture"
  }
}

resource "azurerm_shared_image" "debian" {
  name                = "tf-azrm-debian"
  gallery_name        = azurerm_shared_image_gallery.gallery.name
  resource_group_name = azurerm_resource_group.gallery.name
  location            = azurerm_resource_group.gallery.location
  os_type             = "Linux"
  specialized         = true
  hyper_v_generation  = "V2"

  identifier {
    publisher = "sockerless"
    offer     = "debian"
    sku       = "12"
  }
}

resource "azurerm_shared_image_version" "v1" {
  name                = "1.0.0"
  gallery_name        = azurerm_shared_image_gallery.gallery.name
  image_name          = azurerm_shared_image.debian.name
  resource_group_name = azurerm_resource_group.gallery.name
  location            = azurerm_resource_group.gallery.location
  managed_image_id    = azurerm_linux_virtual_machine.source.id

  target_region {
    name                   = azurerm_resource_group.gallery.location
    regional_replica_count = 1
    storage_account_type   = "Standard_LRS"
  }
}

output "gallery_unique_name" {
  value = azurerm_shared_image_gallery.gallery.unique_name
}

output "image_id" {
  value = azurerm_shared_image.debian.id
}

output "version_id" {
  value = azurerm_shared_image_version.v1.id
}

output "vm_id" {
  value = azurerm_linux_virtual_machine.source.id
}
