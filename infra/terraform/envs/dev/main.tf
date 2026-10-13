# CloudDeploy — development environment. All resources come from the platform
# module; this file only chooses sizes. Plan with: make terraform-plan ENV=dev
# (requires az login). There is deliberately no apply target.

terraform {
  required_version = ">= 1.11"
  required_providers {
    azurerm = { source = "hashicorp/azurerm", version = "~> 4.40" }
    random  = { source = "hashicorp/random", version = "~> 3.7" }
  }
  # Remote state: Azure Storage with Entra ID auth and blob locking. Settings
  # live in backend.hcl (git-ignored); see backend.hcl.example.
  backend "azurerm" {}
}

# The subscription comes from ARM_SUBSCRIPTION_ID or the active az login.
provider "azurerm" {
  storage_use_azuread = true
  features {
    resource_group {
      prevent_deletion_if_contains_resources = true
    }
    key_vault {
      purge_soft_delete_on_destroy    = false
      recover_soft_deleted_key_vaults = true
    }
  }
}

variable "name_suffix" { type = string }
variable "operator_ip_ranges" {
  type    = list(string)
  default = []
}
variable "aks_admin_group_object_ids" {
  type    = list(string)
  default = []
}
variable "secrets_version" {
  type    = number
  default = 1
}

module "platform" {
  source                     = "../../modules/platform"
  environment                = "development"
  name_suffix                = var.name_suffix
  operator_ip_ranges         = var.operator_ip_ranges
  aks_admin_group_object_ids = var.aks_admin_group_object_ids
  secrets_version            = var.secrets_version
  address_space              = "10.10.0.0/18"
  # Smallest workable footprint: 1-2 burstable nodes, Burstable PostgreSQL.
  acr_sku                        = "Basic"
  aks_sku_tier                   = "Free"
  aks_node_vm_size               = "Standard_B2s_v2"
  aks_node_count                 = { min = 1, max = 2 }
  postgres_sku                   = "B_Standard_B1ms"
  postgres_backup_retention_days = 7
  postgres_geo_redundant_backup  = false
  postgres_high_availability     = false
  redis_sku                      = "Balanced_B0"
  redis_high_availability        = false
}

output "platform" { value = module.platform }
