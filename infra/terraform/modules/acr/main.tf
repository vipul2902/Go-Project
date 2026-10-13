# Container registry. Admin (username/password) access is disabled: AKS
# pulls with its kubelet managed identity (AcrPull, assigned in the aks
# module) and CI pushes with OIDC federation (Phase 19), so no registry
# password exists anywhere.

variable "name" {
  type        = string
  description = "Globally unique, 5-50 alphanumeric characters."
  validation {
    condition     = can(regex("^[a-zA-Z0-9]{5,50}$", var.name))
    error_message = "ACR names are 5-50 alphanumeric characters (no dashes)."
  }
}
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }

variable "sku" {
  type        = string
  description = "Basic for dev/staging; Premium is needed for private endpoints, geo-replication and retention policies."
  validation {
    condition     = contains(["Basic", "Standard", "Premium"], var.sku)
    error_message = "sku must be Basic, Standard or Premium."
  }
}

resource "azurerm_container_registry" "this" {
  name                = var.name
  location            = var.location
  resource_group_name = var.resource_group_name
  sku                 = var.sku
  admin_enabled       = false
  # Untagged manifests (superseded digests) are purged after 7 days. The
  # policy is Premium-only.
  retention_policy_in_days = var.sku == "Premium" ? 7 : null
  zone_redundancy_enabled  = var.sku == "Premium"
  tags                     = var.tags
}

output "id" { value = azurerm_container_registry.this.id }
output "login_server" { value = azurerm_container_registry.this.login_server }
