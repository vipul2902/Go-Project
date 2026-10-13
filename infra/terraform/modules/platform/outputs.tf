# Outputs are what the Helm release and CI/CD need: names and endpoints,
# never secret values.

output "resource_group" { value = azurerm_resource_group.this.name }
output "aks_name" { value = module.aks.name }
output "acr_login_server" { value = module.acr.login_server }
output "key_vault_uri" { value = module.keyvault.uri }
output "app_identity_client_id" {
  value       = module.keyvault.app_identity_client_id
  description = "Annotate the API and worker service accounts with azure.workload.identity/client-id."
}
output "postgres_fqdn" { value = module.postgres.fqdn }
output "redis_hostname" { value = module.redis.hostname }
output "key_vault_secret_names" { value = sort(keys(azurerm_key_vault_secret.app)) }
