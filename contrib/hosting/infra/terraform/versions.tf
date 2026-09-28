terraform {
  required_version = ">= 1.6"

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.40"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.40"
    }
  }
}

provider "digitalocean" {
  token             = var.do_token
  spaces_access_id  = var.spaces_access_id
  spaces_secret_key = var.spaces_secret_key
}

provider "cloudflare" {
  # Only used when cloudflare_zone_id is set; a dummy token keeps the provider
  # configurable otherwise.
  api_token = var.cloudflare_api_token != "" ? var.cloudflare_api_token : "0000000000000000000000000000000000000000"
}
