variable "do_token" {
  description = "DigitalOcean API token used by Terraform (not the scaler's scoped token)."
  type        = string
  sensitive   = true
}

variable "spaces_access_id" {
  description = "Spaces access key for the backup bucket."
  type        = string
  sensitive   = true
}

variable "spaces_secret_key" {
  description = "Spaces secret key for the backup bucket."
  type        = string
  sensitive   = true
}

variable "region" {
  description = "Single DigitalOcean region for the whole platform."
  type        = string
  default     = "ams3"
}

variable "name_prefix" {
  description = "Prefix for every resource name."
  type        = string
  default     = "gitw3"
}

variable "vpc_cidr" {
  type    = string
  default = "10.10.0.0/16"
}

variable "vpn_cidrs" {
  description = "Team VPN / Tailscale egress ranges allowed to reach SSH, Forgejo and the Dokploy UI."
  type        = list(string)
}

variable "ssh_key_fingerprints" {
  description = "SSH keys installed on every droplet."
  type        = list(string)
}

variable "image" {
  type    = string
  default = "ubuntu-24-04-x64"
}

variable "forgejo_size" {
  type    = string
  default = "s-4vcpu-8gb"
}

variable "forgejo_volume_gb" {
  description = "Block volume for repositories and registry blobs."
  type        = number
  default     = 200
}

variable "manager_size" {
  type    = string
  default = "s-2vcpu-4gb"
}

variable "manager_count" {
  description = "Swarm managers. 1 to start; 3 survives the loss of one manager (P5)."
  type        = number
  default     = 1

  validation {
    condition     = contains([1, 3, 5], var.manager_count)
    error_message = "Swarm needs an odd number of managers: 1, 3 or 5."
  }
}

variable "builder_size" {
  type    = string
  default = "c-4"
}

variable "initial_workers" {
  description = "Workers created by Terraform before the scaler takes over (P4)."
  type        = number
  default     = 1
}

variable "worker_size" {
  type    = string
  default = "s-2vcpu-4gb"
}

variable "forgejo_url" {
  description = "Public HTTPS URL of GitW3, e.g. https://git.example.com (reached over the VPC by builders and workers)."
  type        = string
}

variable "builder_runner_token" {
  description = "Runner registration token of the platform/builder repository (see bootstrap.sh). Empty skips registration."
  type        = string
  default     = ""
  sensitive   = true
}

variable "swarm_worker_join_token" {
  description = "Worker join token, only needed for initial_workers > 0 after the manager exists (two-phase apply)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "postgres_size" {
  type    = string
  default = "db-s-1vcpu-2gb"
}

variable "apps_domain" {
  description = "Base domain for app URLs; *.apps_domain points at the manager's reserved IP."
  type        = string
}

variable "cloudflare_api_token" {
  type      = string
  default   = ""
  sensitive = true
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone for apps_domain. Empty skips the wildcard record."
  type        = string
  default     = ""
}
