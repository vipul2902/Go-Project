# PostgreSQL Flexible Server, reachable only inside the VNet (delegated
# subnet + private DNS). The administrator password is EPHEMERAL and passed
# to a WRITE-ONLY argument: Terraform sends it to Azure but never writes it
# to the plan or the state file. Rotating it means bumping password_version.

variable "name" { type = string }
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }
variable "subnet_id" { type = string }
variable "private_dns_zone_id" { type = string }

variable "administrator_login" {
  type    = string
  default = "clouddeploy_admin"
}

variable "administrator_password" {
  type      = string
  ephemeral = true
  sensitive = true
}

variable "password_version" {
  type        = number
  description = "Increment to push a new administrator password (write-only arguments are only sent when this changes)."
}

variable "sku_name" {
  type        = string
  description = "B_Standard_B1ms (burstable) for dev; GP_Standard_D2ds_v5 or larger for production."
}

variable "storage_mb" {
  type    = number
  default = 32768
}

variable "postgres_version" {
  type    = string
  default = "17"
}

variable "backup_retention_days" {
  type = number
  validation {
    condition     = var.backup_retention_days >= 7 && var.backup_retention_days <= 35
    error_message = "backup_retention_days must be 7-35."
  }
}

variable "geo_redundant_backup" { type = bool }

variable "high_availability" {
  type        = bool
  description = "Zone-redundant standby. Not available on Burstable SKUs and roughly doubles the cost."
}

variable "zone" {
  type    = string
  default = "1"
}

resource "azurerm_postgresql_flexible_server" "this" {
  name                = "psql-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  version             = var.postgres_version
  sku_name            = var.sku_name
  storage_mb          = var.storage_mb
  auto_grow_enabled   = true
  zone                = var.zone
  tags                = var.tags

  delegated_subnet_id           = var.subnet_id
  private_dns_zone_id           = var.private_dns_zone_id
  public_network_access_enabled = false

  administrator_login               = var.administrator_login
  administrator_password_wo         = var.administrator_password
  administrator_password_wo_version = var.password_version

  backup_retention_days        = var.backup_retention_days
  geo_redundant_backup_enabled = var.geo_redundant_backup

  dynamic "high_availability" {
    for_each = var.high_availability ? [1] : []
    content {
      mode = "ZoneRedundant"
    }
  }

  lifecycle {
    precondition {
      condition     = !(var.high_availability && startswith(var.sku_name, "B_"))
      error_message = "High availability is not supported on Burstable (B_) SKUs."
    }
    # Azure may move the primary to the standby zone after a failover.
    ignore_changes = [zone, high_availability[0].standby_availability_zone]
  }
}

resource "azurerm_postgresql_flexible_server_database" "app" {
  name      = "clouddeploy"
  server_id = azurerm_postgresql_flexible_server.this.id
  charset   = "UTF8"
  collation = "en_US.utf8"
}

# The API's TLS default is sslmode=require; keep the server enforcing it.
resource "azurerm_postgresql_flexible_server_configuration" "require_tls" {
  name      = "require_secure_transport"
  server_id = azurerm_postgresql_flexible_server.this.id
  value     = "on"
}

output "id" { value = azurerm_postgresql_flexible_server.this.id }
output "fqdn" { value = azurerm_postgresql_flexible_server.this.fqdn }
output "administrator_login" { value = var.administrator_login }
output "database" { value = azurerm_postgresql_flexible_server_database.app.name }
