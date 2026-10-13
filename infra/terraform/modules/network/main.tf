# Network: one VNet with a subnet per tier, and the private DNS zones that
# make private endpoints resolve inside it. Nothing here has a public IP;
# data services are reachable only from inside the VNet.

variable "name" { type = string }
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }

variable "address_space" {
  type        = string
  description = "VNet CIDR. Subnets are carved from it: /20 for AKS nodes, /24 each for PostgreSQL and private endpoints."
  validation {
    condition     = can(cidrnetmask(var.address_space)) && tonumber(split("/", var.address_space)[1]) <= 18
    error_message = "address_space must be a valid CIDR of /18 or larger."
  }
}

locals {
  aks_cidr      = cidrsubnet(var.address_space, 2, 0)                   # first /20 of a /18
  postgres_cidr = cidrsubnet(cidrsubnet(var.address_space, 2, 1), 4, 0) # /24
  pe_cidr       = cidrsubnet(cidrsubnet(var.address_space, 2, 1), 4, 1) # /24

  # Private DNS zones for the services reached over private networking.
  dns_zones = {
    postgres = "${var.name}.private.postgres.database.azure.com"
    redis    = "privatelink.redis.azure.net"
    keyvault = "privatelink.vaultcore.azure.net"
  }
}

resource "azurerm_virtual_network" "this" {
  name                = "vnet-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  address_space       = [var.address_space]
  tags                = var.tags
}

resource "azurerm_subnet" "aks" {
  name                 = "snet-aks"
  resource_group_name  = var.resource_group_name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [local.aks_cidr]
}

# PostgreSQL Flexible Server with VNet integration needs a subnet delegated
# to it exclusively.
resource "azurerm_subnet" "postgres" {
  name                 = "snet-postgres"
  resource_group_name  = var.resource_group_name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [local.postgres_cidr]
  service_endpoints    = ["Microsoft.Storage"]
  delegation {
    name = "postgres"
    service_delegation {
      name    = "Microsoft.DBforPostgreSQL/flexibleServers"
      actions = ["Microsoft.Network/virtualNetworks/subnets/join/action"]
    }
  }
}

resource "azurerm_subnet" "private_endpoints" {
  name                 = "snet-private-endpoints"
  resource_group_name  = var.resource_group_name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [local.pe_cidr]
}

resource "azurerm_private_dns_zone" "this" {
  for_each            = local.dns_zones
  name                = each.value
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "this" {
  for_each              = local.dns_zones
  name                  = "link-${each.key}"
  resource_group_name   = var.resource_group_name
  private_dns_zone_name = azurerm_private_dns_zone.this[each.key].name
  virtual_network_id    = azurerm_virtual_network.this.id
  registration_enabled  = false
  tags                  = var.tags
}

output "vnet_id" { value = azurerm_virtual_network.this.id }
output "aks_subnet_id" { value = azurerm_subnet.aks.id }
output "postgres_subnet_id" { value = azurerm_subnet.postgres.id }
output "private_endpoint_subnet_id" { value = azurerm_subnet.private_endpoints.id }
output "dns_zone_ids" {
  value = { for k, z in azurerm_private_dns_zone.this : k => z.id }
  # The PostgreSQL zone must be linked before the server is created.
  depends_on = [azurerm_private_dns_zone_virtual_network_link.this]
}
output "cidrs" {
  value = { aks = local.aks_cidr, postgres = local.postgres_cidr, private_endpoints = local.pe_cidr }
}
