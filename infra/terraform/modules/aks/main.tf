# AKS cluster. Authentication is Entra ID only (local accounts disabled, so
# there is no static cluster-admin kubeconfig to leak), Azure RBAC governs
# kubectl access, and workloads reach Azure with workload identity (OIDC
# federation) instead of stored credentials.

variable "name" { type = string }
variable "location" { type = string }
variable "resource_group_name" { type = string }
variable "tags" { type = map(string) }
variable "subnet_id" { type = string }
variable "acr_id" { type = string }

variable "kubernetes_version" {
  type        = string
  default     = null
  description = "null lets AKS pick the default supported version."
}

variable "sku_tier" {
  type        = string
  description = "Free (no uptime SLA) for dev; Standard (financially backed API server SLA) for production."
  validation {
    condition     = contains(["Free", "Standard"], var.sku_tier)
    error_message = "sku_tier must be Free or Standard."
  }
}

variable "node_vm_size" { type = string }

variable "node_count" {
  type = object({ min = number, max = number })
  validation {
    condition     = var.node_count.min >= 1 && var.node_count.max >= var.node_count.min && var.node_count.max <= 20
    error_message = "node_count needs 1 <= min <= max <= 20."
  }
}

variable "zones" {
  type    = list(string)
  default = []
}

variable "admin_group_object_ids" {
  type        = list(string)
  default     = []
  description = "Entra ID groups granted cluster-admin. Empty means access is granted only through Azure RBAC role assignments."
}

variable "log_analytics_retention_days" {
  type    = number
  default = 30
}

resource "azurerm_log_analytics_workspace" "this" {
  name                = "log-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  sku                 = "PerGB2018"
  retention_in_days   = var.log_analytics_retention_days
  tags                = var.tags
}

# Explicit identities instead of AKS-generated ones: the kubelet identity
# (used to pull images) exists, and holds AcrPull, BEFORE the cluster does,
# so the first image pull never races the role assignment, and the grant
# survives rebuilding the cluster.
resource "azurerm_user_assigned_identity" "control_plane" {
  name                = "id-${var.name}-aks"
  location            = var.location
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

resource "azurerm_user_assigned_identity" "kubelet" {
  name                = "id-${var.name}-kubelet"
  location            = var.location
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

# The control plane assigns the kubelet identity to the node VMs.
resource "azurerm_role_assignment" "kubelet_operator" {
  scope                            = azurerm_user_assigned_identity.kubelet.id
  role_definition_name             = "Managed Identity Operator"
  principal_id                     = azurerm_user_assigned_identity.control_plane.principal_id
  skip_service_principal_aad_check = true
}

# Nodes pull images with the kubelet identity; no imagePullSecrets needed.
resource "azurerm_role_assignment" "acr_pull" {
  scope                            = var.acr_id
  role_definition_name             = "AcrPull"
  principal_id                     = azurerm_user_assigned_identity.kubelet.principal_id
  skip_service_principal_aad_check = true
}

resource "azurerm_kubernetes_cluster" "this" {
  name                = "aks-${var.name}"
  location            = var.location
  resource_group_name = var.resource_group_name
  dns_prefix          = "aks-${var.name}"
  kubernetes_version  = var.kubernetes_version
  sku_tier            = var.sku_tier
  tags                = var.tags

  local_account_disabled    = true
  oidc_issuer_enabled       = true
  workload_identity_enabled = true
  azure_policy_enabled      = true
  image_cleaner_enabled     = true
  automatic_upgrade_channel = "patch"
  node_os_upgrade_channel   = "NodeImage"

  default_node_pool {
    name                        = "system"
    vm_size                     = var.node_vm_size
    vnet_subnet_id              = var.subnet_id
    auto_scaling_enabled        = true
    min_count                   = var.node_count.min
    max_count                   = var.node_count.max
    zones                       = var.zones
    os_sku                      = "AzureLinux"
    temporary_name_for_rotation = "systemtmp"
    upgrade_settings {
      max_surge = "33%"
    }
  }

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.control_plane.id]
  }

  kubelet_identity {
    client_id                 = azurerm_user_assigned_identity.kubelet.client_id
    object_id                 = azurerm_user_assigned_identity.kubelet.principal_id
    user_assigned_identity_id = azurerm_user_assigned_identity.kubelet.id
  }

  azure_active_directory_role_based_access_control {
    azure_rbac_enabled     = true
    admin_group_object_ids = var.admin_group_object_ids
  }

  # Azure CNI Overlay: pods get addresses from a private overlay range, so
  # the VNet only needs room for nodes. Cilium enforces the chart's
  # NetworkPolicies.
  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    network_data_plane  = "cilium"
    network_policy      = "cilium"
    pod_cidr            = "10.244.0.0/16"
    service_cidr        = "10.245.0.0/16"
    dns_service_ip      = "10.245.0.10"
    load_balancer_sku   = "standard"
  }

  oms_agent {
    log_analytics_workspace_id      = azurerm_log_analytics_workspace.this.id
    msi_auth_for_monitoring_enabled = true
  }

  lifecycle {
    # The autoscaler owns the live node count.
    ignore_changes = [default_node_pool[0].node_count]
  }
  depends_on = [azurerm_role_assignment.kubelet_operator, azurerm_role_assignment.acr_pull]
}


output "id" { value = azurerm_kubernetes_cluster.this.id }
output "name" { value = azurerm_kubernetes_cluster.this.name }
output "oidc_issuer_url" { value = azurerm_kubernetes_cluster.this.oidc_issuer_url }
output "kubelet_object_id" { value = azurerm_user_assigned_identity.kubelet.principal_id }
