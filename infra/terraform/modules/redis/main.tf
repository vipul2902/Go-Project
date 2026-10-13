# Azure Managed Redis (the successor to Azure Cache for Redis, which is
# being retired), reachable only through a private endpoint. CloudDeploy
# uses Redis for rate limiting only and fails open without it (ADR-003), so
# a small, non-persistent instance is enough.

variable "name" { type = string }
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }
variable "private_endpoint_subnet_id" { type = string }
variable "private_dns_zone_id" { type = string }

variable "sku_name" {
  type        = string
  description = "Balanced_B0 is the smallest; Balanced_B1+ for production."
}

variable "high_availability" { type = bool }

resource "azurerm_managed_redis" "this" {
  name                      = "redis-${var.name}"
  location                  = var.location
  resource_group_name       = var.resource_group_name
  sku_name                  = var.sku_name
  high_availability_enabled = var.high_availability
  public_network_access     = "Disabled"
  tags                      = var.tags

  default_database {
    client_protocol = "Encrypted"
    # Rate-limit buckets are disposable: evict rather than reject writes.
    eviction_policy = "VolatileLRU"
    # The app authenticates with an access key in REDIS_URL. Entra ID auth
    # (no key at all) needs a token-refreshing client; see docs/terraform.md.
    access_keys_authentication_enabled = true
  }
}

resource "azurerm_private_endpoint" "this" {
  name                = "pe-redis-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  subnet_id           = var.private_endpoint_subnet_id
  tags                = var.tags

  private_service_connection {
    name                           = "redis"
    private_connection_resource_id = azurerm_managed_redis.this.id
    subresource_names              = ["redisEnterprise"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "redis"
    private_dns_zone_ids = [var.private_dns_zone_id]
  }
}

output "id" { value = azurerm_managed_redis.this.id }
output "hostname" { value = azurerm_managed_redis.this.hostname }
output "port" { value = azurerm_managed_redis.this.default_database[0].port }
output "primary_access_key" {
  value     = azurerm_managed_redis.this.default_database[0].primary_access_key
  sensitive = true
}
