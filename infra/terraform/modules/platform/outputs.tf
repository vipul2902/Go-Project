# Outputs are what the Helm release and CI/CD need: names and endpoints,
# never secret values.

output "resource_group" { value = azurerm_resource_group.this.name }
output "aks_name" { value = module.aks.name }
output "acr_login_server" { value = module.acr.login_server }
output "key_vault_uri" { value = module.keyvault.uri }
output "workload_client_ids" {
  value       = module.keyvault.workload_client_ids
  description = "Per service account; set as azure.workload.identity/client-id annotations by the Helm values."
}
output "key_vault_name" { value = module.keyvault.name }
output "tenant_id" { value = data.azurerm_client_config.current.tenant_id }
output "acr_id" { value = module.acr.id }
output "postgres_fqdn" { value = module.postgres.fqdn }
output "redis_hostname" { value = module.redis.hostname }
output "key_vault_secret_names" { value = sort(keys(azurerm_key_vault_secret.app)) }
