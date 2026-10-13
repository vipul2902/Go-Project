# Offline tests: `terraform test` with a MOCKED azurerm provider. No Azure
# account, credentials or network calls; plans are computed against fake
# provider responses, and the assertions check what we would ask Azure for.
# The random provider is real (it runs locally).

mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      tenant_id = "00000000-0000-0000-0000-000000000001"
      object_id = "00000000-0000-0000-0000-000000000002"
    }
  }
  # Computed nested blocks are empty lists in a mocked plan unless given
  # defaults; override_during = plan applies them at plan time.
  mock_resource "azurerm_kubernetes_cluster" {
    override_during = plan
    defaults = {
      oidc_issuer_url = "https://issuer.example"
    }
  }
  mock_resource "azurerm_managed_redis" {
    override_during = plan
    defaults = {
      id               = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Cache/redisEnterprise/redis-x"
      hostname         = "redis.example"
      default_database = { port = 10000, primary_access_key = "mock-key" }
    }
  }
}

variables {
  name_suffix                    = "test1"
  address_space                  = "10.10.0.0/18"
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

# ---- platform composition ----------------------------------------------

run "only_the_worker_can_build" {
  command = plan
  variables { environment = "development" }

  assert {
    condition     = toset(keys(module.keyvault.workload_client_ids)) == toset(["clouddeploy-api", "clouddeploy-worker", "clouddeploy-migrate"])
    error_message = "Each service account needs its own identity."
  }
}

run "development_has_no_delete_lock" {
  command = plan
  variables { environment = "development" }

  assert {
    condition     = length(azurerm_management_lock.no_delete) == 0
    error_message = "Development must not get a delete lock (it is meant to be torn down)."
  }
  assert {
    condition     = azurerm_resource_group.this.name == "rg-clouddeploy-development"
    error_message = "Unexpected resource group name."
  }
  assert {
    condition     = azurerm_resource_group.this.tags["managed-by"] == "terraform" && azurerm_resource_group.this.tags["environment"] == "development"
    error_message = "Resources must be tagged with environment and managed-by."
  }
}

run "secrets_are_write_only" {
  command = plan
  variables { environment = "development" }

  assert {
    condition     = toset(keys(azurerm_key_vault_secret.app)) == toset(["database-url", "redis-url", "jwt-secret"])
    error_message = "Expected exactly the three app secrets."
  }
  # The plain `value` argument would put the secret into state; only the
  # write-only `value_wo` may be used.
  assert {
    condition     = alltrue([for s in azurerm_key_vault_secret.app : s.value == null && s.value_wo == null && s.value_wo_version == 1])
    error_message = "Secrets must use value_wo (absent from plan and state), versioned by secrets_version."
  }
}

run "production_gets_delete_lock" {
  command = plan
  variables {
    environment                   = "production"
    zones                         = ["1", "2", "3"]
    aks_sku_tier                  = "Standard"
    postgres_sku                  = "GP_Standard_D2ds_v5"
    postgres_geo_redundant_backup = true
    postgres_high_availability    = true
    redis_high_availability       = true
  }

  assert {
    condition     = length(azurerm_management_lock.no_delete) == 1 && azurerm_management_lock.no_delete[0].lock_level == "CanNotDelete"
    error_message = "Production resource group must carry a CanNotDelete lock."
  }
}

run "fragile_production_is_flagged" {
  command = plan
  variables { environment = "production" } # dev-sized inputs

  expect_failures = [check.production_is_resilient]
}

run "unknown_environment_is_rejected" {
  command = plan
  variables { environment = "qa" }

  expect_failures = [var.environment]
}

# ---- individual modules ------------------------------------------------

run "acr_has_no_admin_user" {
  command = plan
  module { source = "../acr" }
  variables {
    name                = "cddevtest1acr"
    location            = "centralindia"
    resource_group_name = "rg"
    sku                 = "Basic"
    tags                = {}
  }

  assert {
    condition     = azurerm_container_registry.this.admin_enabled == false
    error_message = "ACR admin user must be disabled (AKS pulls with its managed identity)."
  }
  assert {
    condition = (
      contains(azurerm_role_definition.task_runner.permissions[0].actions, "Microsoft.ContainerRegistry/registries/scheduleRun/action") &&
      alltrue([for a in azurerm_role_definition.task_runner.permissions[0].actions : !strcontains(a, "*") && !endswith(a, "/delete") && !endswith(a, "/write")])
    )
    error_message = "The build role may schedule runs but must have no wildcards, writes or deletes."
  }
  assert {
    condition     = azurerm_container_registry.this.retention_policy_in_days == null
    error_message = "Retention policy is Premium-only and must not be set on Basic."
  }
}

run "acr_name_is_validated" {
  command = plan
  module { source = "../acr" }
  variables {
    name                = "cd-dev-acr"
    location            = "centralindia"
    resource_group_name = "rg"
    sku                 = "Basic"
    tags                = {}
  }
  expect_failures = [var.name]
}

run "aks_is_identity_only" {
  command = plan
  module { source = "../aks" }
  variables {
    name                = "clouddeploy-development"
    location            = "centralindia"
    resource_group_name = "rg"
    subnet_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    acr_id              = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/cdtestacr"
    sku_tier            = "Free"
    node_vm_size        = "Standard_B2s_v2"
    node_count          = { min = 1, max = 2 }
    tags                = {}
  }

  assert {
    condition     = azurerm_kubernetes_cluster.this.local_account_disabled && azurerm_kubernetes_cluster.this.workload_identity_enabled && azurerm_kubernetes_cluster.this.oidc_issuer_enabled
    error_message = "AKS must disable local accounts and enable workload identity."
  }
  assert {
    condition     = azurerm_kubernetes_cluster.this.network_profile[0].network_policy == "cilium"
    error_message = "A network policy engine is required for the chart's NetworkPolicies."
  }
  assert {
    condition     = azurerm_kubernetes_cluster.this.key_vault_secrets_provider[0].secret_rotation_enabled
    error_message = "The Key Vault CSI driver must be enabled, with rotation."
  }
  assert {
    condition     = azurerm_role_assignment.acr_pull.role_definition_name == "AcrPull"
    error_message = "The kubelet identity needs AcrPull, nothing broader."
  }
}

run "aks_node_count_is_validated" {
  command = plan
  module { source = "../aks" }
  variables {
    name                = "x"
    location            = "centralindia"
    resource_group_name = "rg"
    subnet_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    acr_id              = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/cdtestacr"
    sku_tier            = "Free"
    node_vm_size        = "Standard_B2s_v2"
    node_count          = { min = 3, max = 2 }
    tags                = {}
  }
  expect_failures = [var.node_count]
}

run "postgres_is_private_with_write_only_password" {
  command = plan
  module { source = "../postgres" }
  variables {
    name                   = "clouddeploy-development"
    location               = "centralindia"
    resource_group_name    = "rg"
    subnet_id              = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    private_dns_zone_id    = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/privateDnsZones/x.private.postgres.database.azure.com"
    administrator_password = "not-a-real-password-test-only"
    password_version       = 1
    sku_name               = "B_Standard_B1ms"
    backup_retention_days  = 7
    geo_redundant_backup   = false
    high_availability      = false
    tags                   = {}
  }

  assert {
    condition     = azurerm_postgresql_flexible_server.this.public_network_access_enabled == false
    error_message = "PostgreSQL must not be reachable from the internet."
  }
  assert {
    condition     = azurerm_postgresql_flexible_server.this.administrator_password == null && azurerm_postgresql_flexible_server.this.administrator_password_wo == null
    error_message = "The password must go through the write-only argument and never appear in the plan."
  }
  assert {
    condition     = azurerm_postgresql_flexible_server_configuration.require_tls.value == "on"
    error_message = "TLS must be enforced."
  }
  assert {
    condition     = length(azurerm_postgresql_flexible_server.this.high_availability) == 0
    error_message = "Development must not pay for a standby."
  }
}

run "postgres_rejects_ha_on_burstable" {
  command = plan
  module { source = "../postgres" }
  variables {
    name                   = "x"
    location               = "centralindia"
    resource_group_name    = "rg"
    subnet_id              = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    private_dns_zone_id    = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/privateDnsZones/x.private.postgres.database.azure.com"
    administrator_password = "not-a-real-password-test-only"
    password_version       = 1
    sku_name               = "B_Standard_B1ms"
    backup_retention_days  = 7
    geo_redundant_backup   = false
    high_availability      = true
    tags                   = {}
  }
  expect_failures = [azurerm_postgresql_flexible_server.this]
}

run "redis_is_private_and_encrypted" {
  command = plan
  module { source = "../redis" }
  variables {
    name                       = "x"
    location                   = "centralindia"
    resource_group_name        = "rg"
    private_endpoint_subnet_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    private_dns_zone_id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/privateDnsZones/privatelink.example"
    sku_name                   = "Balanced_B0"
    high_availability          = false
    tags                       = {}
  }

  assert {
    condition     = azurerm_managed_redis.this.public_network_access == "Disabled"
    error_message = "Redis must not be reachable from the internet."
  }
  assert {
    condition     = azurerm_managed_redis.this.default_database[0].client_protocol == "Encrypted"
    error_message = "Redis must require TLS."
  }
}

run "keyvault_is_rbac_and_least_privilege" {
  command = plan
  module { source = "../keyvault" }
  variables {
    name                       = "cddevtest1kv"
    location                   = "centralindia"
    resource_group_name        = "rg"
    tenant_id                  = "00000000-0000-0000-0000-000000000001"
    private_endpoint_subnet_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet"
    private_dns_zone_id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/privateDnsZones/privatelink.example"
    purge_protection           = false
    public_network_access      = false
    oidc_issuer_url            = "https://issuer.example"
    workload_namespace         = "clouddeploy"
    workload_service_accounts  = ["clouddeploy-api", "clouddeploy-worker", "clouddeploy-migrate"]
    tags                       = {}
  }

  assert {
    condition     = azurerm_key_vault.this.rbac_authorization_enabled && azurerm_key_vault.this.network_acls[0].default_action == "Deny"
    error_message = "Key Vault must use RBAC and deny network access by default."
  }
  assert {
    condition     = alltrue([for r in azurerm_role_assignment.secrets_user : r.role_definition_name == "Key Vault Secrets User"])
    error_message = "Workload identities may only read secrets."
  }
  assert {
    condition     = azurerm_federated_identity_credential.workload["clouddeploy-api"].subject == "system:serviceaccount:clouddeploy:clouddeploy-api"
    error_message = "Federation must be scoped to the exact service account."
  }
}
