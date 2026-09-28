output "ingress_ip" {
  description = "Point *.apps_domain here (done automatically with Cloudflare). Also [hosting.domains] TARGET_IP."
  value       = digitalocean_reserved_ip.ingress.ip_address
}

output "forgejo_private_ip" {
  value = digitalocean_droplet.forgejo.ipv4_address_private
}

output "manager_private_ips" {
  description = "The first one is the scaler's MANAGER_PRIVATE_IP and the Dokploy/socket proxy host for GitW3."
  value       = digitalocean_droplet.manager[*].ipv4_address_private
}

output "builder_private_ip" {
  value = digitalocean_droplet.builder.ipv4_address_private
}

output "vpc_uuid" {
  description = "Scaler VPC_UUID."
  value       = digitalocean_vpc.platform.id
}

output "worker_tag" {
  description = "Scaler WORKER_TAG."
  value       = digitalocean_tag.all["worker"].name
}

output "postgres" {
  value = {
    host     = digitalocean_database_cluster.forgejo.private_host
    port     = digitalocean_database_cluster.forgejo.port
    database = digitalocean_database_db.forgejo.name
    user     = digitalocean_database_user.forgejo.name
  }
}

output "postgres_password" {
  value     = digitalocean_database_user.forgejo.password
  sensitive = true
}

output "backup_bucket" {
  value = digitalocean_spaces_bucket.backups.bucket_domain_name
}
