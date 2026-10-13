variable "environment" {
  type = string
  validation {
    condition     = contains(["development", "staging", "production"], var.environment)
    error_message = "environment must be development, staging or production (the same names APP_ENV uses)."
  }
}

variable "location" {
  type    = string
  default = "centralindia"
}

variable "name_suffix" {
  type        = string
  description = "Short suffix that makes the ACR and Key Vault names globally unique."
  validation {
    condition     = can(regex("^[a-z0-9]{3,8}$", var.name_suffix))
    error_message = "name_suffix must be 3-8 lowercase letters or digits."
  }
}

variable "tags" {
  type    = map(string)
  default = {}
}

variable "address_space" { type = string }

variable "zones" {
  type    = list(string)
  default = []
}

variable "secrets_version" {
  type        = number
  default     = 1
  description = "Bump to generate and write new secrets (PostgreSQL password, JWT secret, Key Vault values). Write-only arguments are sent only when this changes."
}

variable "operator_ip_ranges" {
  type        = list(string)
  default     = []
  description = "Public IPs/CIDRs allowed to reach Key Vault (the machine or runner executing Terraform). Empty disables public access entirely."
}

variable "acr_sku" { type = string }

variable "kubernetes_version" {
  type    = string
  default = null
}
variable "aks_sku_tier" { type = string }
variable "aks_node_vm_size" { type = string }
variable "aks_node_count" { type = object({ min = number, max = number }) }
variable "aks_admin_group_object_ids" {
  type    = list(string)
  default = []
}

variable "postgres_sku" { type = string }
variable "postgres_storage_mb" {
  type    = number
  default = 32768
}
variable "postgres_backup_retention_days" { type = number }
variable "postgres_geo_redundant_backup" { type = bool }
variable "postgres_high_availability" { type = bool }

variable "redis_sku" { type = string }
variable "redis_high_availability" { type = bool }
