locals {
  tags = {
    control = "${var.name_prefix}-control"
    forgejo = "${var.name_prefix}-forgejo"
    manager = "${var.name_prefix}-manager"
    build   = "${var.name_prefix}-build"
    worker  = "${var.name_prefix}-worker"
  }
}

resource "digitalocean_tag" "all" {
  for_each = local.tags
  name     = each.value
}

resource "digitalocean_vpc" "platform" {
  name     = "${var.name_prefix}-vpc"
  region   = var.region
  ip_range = var.vpc_cidr
}

# ---------------------------------------------------------------- Forgejo ---

resource "digitalocean_volume" "forgejo_data" {
  name                    = "${var.name_prefix}-forgejo-data"
  region                  = var.region
  size                    = var.forgejo_volume_gb
  initial_filesystem_type = "ext4"
  description             = "GitW3 repositories and registry blobs"
}

resource "digitalocean_droplet" "forgejo" {
  name       = "${var.name_prefix}-forgejo-01"
  region     = var.region
  size       = var.forgejo_size
  image      = var.image
  vpc_uuid   = digitalocean_vpc.platform.id
  ssh_keys   = var.ssh_key_fingerprints
  monitoring = true
  tags       = [digitalocean_tag.all["control"].id, digitalocean_tag.all["forgejo"].id]
  user_data = templatefile("${path.module}/../cloud-init/forgejo.yaml.tftpl", {
    volume_name = digitalocean_volume.forgejo_data.name
  })
}

resource "digitalocean_volume_attachment" "forgejo_data" {
  droplet_id = digitalocean_droplet.forgejo.id
  volume_id  = digitalocean_volume.forgejo_data.id
}

# ---------------------------------------------------------------- Swarm ---

resource "digitalocean_droplet" "manager" {
  count      = var.manager_count
  name       = format("%s-manager-%02d", var.name_prefix, count.index + 1)
  region     = var.region
  size       = var.manager_size
  image      = var.image
  vpc_uuid   = digitalocean_vpc.platform.id
  ssh_keys   = var.ssh_key_fingerprints
  monitoring = true
  tags       = [digitalocean_tag.all["control"].id, digitalocean_tag.all["manager"].id]
  # The first manager initialises the swarm and installs Dokploy; further
  # managers are joined by hand with a manager token (see docs/gitw3/hosting.md).
  user_data = templatefile("${path.module}/../cloud-init/manager.yaml.tftpl", {
    primary = count.index == 0
  })
}

resource "digitalocean_reserved_ip" "ingress" {
  region = var.region
}

resource "digitalocean_reserved_ip_assignment" "ingress" {
  ip_address = digitalocean_reserved_ip.ingress.ip_address
  droplet_id = digitalocean_droplet.manager[0].id
}

resource "digitalocean_droplet" "worker" {
  count      = var.swarm_worker_join_token == "" ? 0 : var.initial_workers
  name       = format("%s-worker-seed-%02d", var.name_prefix, count.index + 1)
  region     = var.region
  size       = var.worker_size
  image      = var.image
  vpc_uuid   = digitalocean_vpc.platform.id
  ssh_keys   = var.ssh_key_fingerprints
  monitoring = true
  tags       = [digitalocean_tag.all["worker"].id]
  user_data = templatefile("${path.module}/../cloud-init/worker.yaml.tftpl", {
    join_token = var.swarm_worker_join_token
    manager_ip = digitalocean_droplet.manager[0].ipv4_address_private
  })
}

# ---------------------------------------------------------------- Build ---

resource "digitalocean_droplet" "builder" {
  name       = "${var.name_prefix}-builder-01"
  region     = var.region
  size       = var.builder_size
  image      = var.image
  vpc_uuid   = digitalocean_vpc.platform.id
  ssh_keys   = var.ssh_key_fingerprints
  monitoring = true
  tags       = [digitalocean_tag.all["build"].id]
  user_data = templatefile("${path.module}/../cloud-init/builder.yaml.tftpl", {
    forgejo_url  = var.forgejo_url
    runner_token = var.builder_runner_token
    runner_name  = "${var.name_prefix}-builder-01"
    job_image    = file("${path.module}/../../builder/image/Dockerfile")
  })
}

# ---------------------------------------------------------------- Data ---

resource "digitalocean_database_cluster" "forgejo" {
  name                 = "${var.name_prefix}-pg"
  engine               = "pg"
  version              = "16"
  size                 = var.postgres_size
  region               = var.region
  node_count           = 1
  private_network_uuid = digitalocean_vpc.platform.id
}

resource "digitalocean_database_db" "forgejo" {
  cluster_id = digitalocean_database_cluster.forgejo.id
  name       = "gitw3"
}

resource "digitalocean_database_user" "forgejo" {
  cluster_id = digitalocean_database_cluster.forgejo.id
  name       = "gitw3"
}

resource "digitalocean_database_firewall" "forgejo" {
  cluster_id = digitalocean_database_cluster.forgejo.id
  rule {
    type  = "tag"
    value = digitalocean_tag.all["forgejo"].name
  }
}

resource "digitalocean_spaces_bucket" "backups" {
  name   = "${var.name_prefix}-backups"
  region = var.region
  acl    = "private"

  versioning {
    enabled = true
  }

  lifecycle_rule {
    enabled = true
    expiration {
      days = 30
    }
    noncurrent_version_expiration {
      days = 7
    }
  }
}

# ---------------------------------------------------------------- DNS ---

resource "cloudflare_record" "apps_wildcard" {
  count   = var.cloudflare_zone_id == "" ? 0 : 1
  zone_id = var.cloudflare_zone_id
  name    = "*.${var.apps_domain}"
  type    = "A"
  content = digitalocean_reserved_ip.ingress.ip_address
  proxied = false
  comment = "GitW3 managed hosting ingress"
}
