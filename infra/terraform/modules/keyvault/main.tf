# Key Vault for the application's runtime secrets (DATABASE_URL, REDIS_URL,
# JWT_SECRET). Access is Azure RBAC only (no legacy access policies). The
# API and worker read secrets through workload identity: a user-assigned
# identity federated with their Kubernetes service account, so no client
# secret exists anywhere. Secrets are written by the platform module with
# write-only arguments.

variable "name" {
  type = string
  validation {
    condition     = can(regex("^[a-zA-Z][a-zA-Z0-9-]{1,22}[a-zA-Z0-9]$", var.name))
    error_message = "Key Vault names are 3-24 characters: letters, digits and dashes."
  }
}
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }
variable "tenant_id" { type = string }
variable "private_endpoint_subnet_id" { type = string }
variable "private_dns_zone_id" { type = string }

variable "purge_protection" {
  type        = bool
  description = "Deleted secrets cannot be purged before the retention period ends. Irreversible once enabled, so production only."
}

variable "public_network_access" {
  type        = bool
  description = "Needed while Terraform itself runs outside the VNet (it writes the secrets). Restricted to allowed_ip_ranges."
}

variable "allowed_ip_ranges" {
  type    = list(string)
  default = []
}

variable "oidc_issuer_url" { type = string }
variable "workload_namespace" { type = string }
variable "workload_service_accounts" {
  type        = list(string)
  description = "Kubernetes service accounts (in workload_namespace) allowed to read the vault."
}

resource "azurerm_key_vault" "this" {
  name                          = var.name
  location                      = var.location
  resource_group_name           = var.resource_group_name
  tenant_id                     = var.tenant_id
  sku_name                      = "standard"
  rbac_authorization_enabled    = true
  purge_protection_enabled      = var.purge_protection
  soft_delete_retention_days    = 90
  public_network_access_enabled = var.public_network_access
  tags                          = var.tags

  network_acls {
    default_action = "Deny"
    bypass         = "AzureServices"
    ip_rules       = var.allowed_ip_ranges
  }
}

resource "azurerm_private_endpoint" "this" {
  name                = "pe-kv-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  subnet_id           = var.private_endpoint_subnet_id
  tags                = var.tags

  private_service_connection {
    name                           = "vault"
    private_connection_resource_id = azurerm_key_vault.this.id
    subresource_names              = ["vault"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "vault"
    private_dns_zone_ids = [var.private_dns_zone_id]
  }
}

# Workload identity: one Azure identity for the app, trusted only for
# tokens the cluster's OIDC issuer mints for the named service accounts.
resource "azurerm_user_assigned_identity" "app" {
  name                = "id-${var.name}-app"
  location            = var.location
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

resource "azurerm_federated_identity_credential" "app" {
  for_each  = toset(var.workload_service_accounts)
  name      = "k8s-${var.workload_namespace}-${each.value}"
  parent_id = azurerm_user_assigned_identity.app.id
  audience  = ["api://AzureADTokenExchange"]
  issuer    = var.oidc_issuer_url
  subject   = "system:serviceaccount:${var.workload_namespace}:${each.value}"
}

# Least privilege: the app can read secret values, nothing else.
resource "azurerm_role_assignment" "app_secrets_user" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = azurerm_user_assigned_identity.app.principal_id
}

output "id" { value = azurerm_key_vault.this.id }
output "uri" { value = azurerm_key_vault.this.vault_uri }
output "app_identity_client_id" { value = azurerm_user_assigned_identity.app.client_id }
