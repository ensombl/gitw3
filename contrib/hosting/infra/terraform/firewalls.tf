# Cloud firewalls are applied by tag, so droplets the scaler creates with the
# worker tag inherit their rules. DigitalOcean firewalls are allow-lists that
# also cover VPC traffic: the build node can reach nothing inside the cluster
# because no inbound rule on the manager or workers admits the build tag.

locals {
  swarm_rules = [
    { protocol = "tcp", port = "2377" },
    { protocol = "tcp", port = "7946" },
    { protocol = "udp", port = "7946" },
    { protocol = "udp", port = "4789" },
  ]
  any_ipv4 = ["0.0.0.0/0", "::/0"]
}

resource "digitalocean_firewall" "forgejo" {
  name = "${var.name_prefix}-forgejo"
  tags = [digitalocean_tag.all["forgejo"].id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.vpn_cidrs
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = var.vpn_cidrs
  }

  # Builders fetch sources and push images; workers and managers pull images.
  inbound_rule {
    protocol    = "tcp"
    port_range  = "443"
    source_tags = [digitalocean_tag.all["build"].id, digitalocean_tag.all["worker"].id, digitalocean_tag.all["manager"].id]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }
}

resource "digitalocean_firewall" "manager" {
  name = "${var.name_prefix}-manager"
  tags = [digitalocean_tag.all["manager"].id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.vpn_cidrs
  }

  # Dokploy UI: VPN only, never public.
  inbound_rule {
    protocol         = "tcp"
    port_range       = "3000"
    source_addresses = var.vpn_cidrs
  }

  # Dokploy API and the read-only Docker socket proxy, for GitW3 only.
  inbound_rule {
    protocol    = "tcp"
    port_range  = "3000"
    source_tags = [digitalocean_tag.all["forgejo"].id]
  }

  inbound_rule {
    protocol    = "tcp"
    port_range  = "2375"
    source_tags = [digitalocean_tag.all["forgejo"].id]
  }

  # Public app traffic through Traefik.
  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = local.any_ipv4
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = local.any_ipv4
  }

  dynamic "inbound_rule" {
    for_each = local.swarm_rules
    content {
      protocol    = inbound_rule.value.protocol
      port_range  = inbound_rule.value.port
      source_tags = [digitalocean_tag.all["worker"].id, digitalocean_tag.all["manager"].id]
    }
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }
}

resource "digitalocean_firewall" "build" {
  name = "${var.name_prefix}-build"
  tags = [digitalocean_tag.all["build"].id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.vpn_cidrs
  }

  # Builds need the internet for dependencies. Cluster nodes still refuse the
  # build node because their inbound rules never admit the build tag.
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "53"
    destination_addresses = local.any_ipv4
  }
}

resource "digitalocean_firewall" "worker" {
  name = "${var.name_prefix}-worker"
  tags = [digitalocean_tag.all["worker"].id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.vpn_cidrs
  }

  dynamic "inbound_rule" {
    for_each = [for rule in local.swarm_rules : rule if rule.port != "2377"]
    content {
      protocol    = inbound_rule.value.protocol
      port_range  = inbound_rule.value.port
      source_tags = [digitalocean_tag.all["worker"].id, digitalocean_tag.all["manager"].id]
    }
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = local.any_ipv4
  }
}
