// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"strings"
	"time"

	"forgejo.org/modules/log"
)

// Hosting scan policies decide what a failed vulnerability scan does to a build.
const (
	HostingScanPolicyBlock = "block"
	HostingScanPolicyWarn  = "warn"
)

// Hosting DNS providers that can keep the reserve domain pool populated.
const (
	HostingDNSProviderWildcard   = "wildcard"
	HostingDNSProviderCloudflare = "cloudflare"
)

// Hosting controls managed deployments onto the Dokploy-managed Docker Swarm.
var Hosting = struct {
	Enabled bool

	DokployURL    string
	DokployAPIKey string
	// DokployRegistryID is the Dokploy registry entry holding the pull token for the deployments org.
	DokployRegistryID string
	// DokployEnvironmentID is the Dokploy project environment that holds every managed app.
	DokployEnvironmentID string
	// DokployServerID pins applications to a Dokploy server; empty means the local manager.
	DokployServerID string
	// PlacementConstraints keep app containers off the control plane.
	PlacementConstraints []string
	// SwarmProxyURL is a restricted Docker socket proxy on the manager used for read-only service status.
	SwarmProxyURL string

	// RequireW3DS ties every managed deploy to a wallet-authorised W3DS
	// deployment record of a PPA-certified release.
	RequireW3DS bool

	BuilderRepo     string
	BuilderWorkflow string
	BuilderRef      string
	RegistryOwner   string
	RegistryHost    string
	CallbackSecret  string
	SourceURLTTL    time.Duration
	BuildQueueAlert time.Duration
	HealthTimeout   time.Duration
	KeepDigests     int
	// ScalerMetricsURL is the scaler's /metrics endpoint, used for "node count at max" alerts.
	ScalerMetricsURL string
	// RegistryAlertBytes raises an alert when the deployments registry grows past it (0 disables).
	RegistryAlertBytes int64
	HTTPTimeout        time.Duration

	Scan struct {
		Policy   string
		Severity string
	}

	Domains struct {
		BaseDomain         string
		Provider           string
		PoolSize           int
		TargetIP           string
		CloudflareAPIToken string
		CloudflareZoneID   string
		Proxied            bool
		AllowCustom        bool
	}
}{
	RequireW3DS:     true,
	BuilderRepo:     "platform/builder",
	BuilderWorkflow: "build.yml",
	RegistryOwner:   "deployments",
	SourceURLTTL:    30 * time.Minute,
	BuildQueueAlert: 15 * time.Minute,
	HealthTimeout:   10 * time.Minute,
	KeepDigests:     10,
	HTTPTimeout:     15 * time.Second,
}

func loadHostingFrom(rootCfg ConfigProvider) {
	section := rootCfg.Section("hosting")
	Hosting.Enabled = section.Key("ENABLED").MustBool(false)
	Hosting.DokployURL = strings.TrimRight(section.Key("DOKPLOY_URL").MustString(""), "/")
	Hosting.DokployAPIKey = section.Key("DOKPLOY_API_KEY").MustString("")
	Hosting.DokployRegistryID = section.Key("DOKPLOY_REGISTRY_ID").MustString("")
	Hosting.DokployEnvironmentID = section.Key("DOKPLOY_ENVIRONMENT_ID").MustString("")
	Hosting.DokployServerID = section.Key("DOKPLOY_SERVER_ID").MustString("")
	Hosting.PlacementConstraints = section.Key("PLACEMENT_CONSTRAINTS").Strings(",")
	if !section.HasKey("PLACEMENT_CONSTRAINTS") {
		Hosting.PlacementConstraints = []string{"node.role==worker"}
	}
	Hosting.SwarmProxyURL = strings.TrimRight(section.Key("SWARM_PROXY_URL").MustString(""), "/")
	Hosting.RequireW3DS = section.Key("REQUIRE_W3DS").MustBool(true)
	Hosting.BuilderRepo = section.Key("BUILDER_REPO").MustString("platform/builder")
	Hosting.BuilderWorkflow = section.Key("BUILDER_WORKFLOW").MustString("build.yml")
	Hosting.BuilderRef = section.Key("BUILDER_REF").MustString("")
	Hosting.RegistryOwner = section.Key("REGISTRY_OWNER").MustString("deployments")
	Hosting.RegistryHost = section.Key("REGISTRY_HOST").MustString("")
	Hosting.CallbackSecret = section.Key("CALLBACK_SECRET").MustString("")
	Hosting.SourceURLTTL = section.Key("SOURCE_URL_TTL").MustDuration(30 * time.Minute)
	Hosting.BuildQueueAlert = section.Key("BUILD_QUEUE_ALERT").MustDuration(15 * time.Minute)
	Hosting.HealthTimeout = section.Key("HEALTH_TIMEOUT").MustDuration(10 * time.Minute)
	Hosting.ScalerMetricsURL = section.Key("SCALER_METRICS_URL").MustString("")
	Hosting.RegistryAlertBytes = section.Key("REGISTRY_ALERT_BYTES").MustInt64(0)
	Hosting.KeepDigests = section.Key("KEEP_DIGESTS").MustInt(10)
	Hosting.HTTPTimeout = section.Key("HTTP_TIMEOUT").MustDuration(15 * time.Second)

	scan := rootCfg.Section("hosting.scan")
	Hosting.Scan.Policy = strings.ToLower(scan.Key("POLICY").In(HostingScanPolicyBlock, []string{HostingScanPolicyBlock, HostingScanPolicyWarn}))
	Hosting.Scan.Severity = strings.ToUpper(scan.Key("SEVERITY").MustString("CRITICAL"))

	domains := rootCfg.Section("hosting.domains")
	Hosting.Domains.BaseDomain = strings.Trim(strings.ToLower(domains.Key("BASE_DOMAIN").MustString("")), ".")
	Hosting.Domains.Provider = strings.ToLower(domains.Key("PROVIDER").In(HostingDNSProviderWildcard, []string{HostingDNSProviderWildcard, HostingDNSProviderCloudflare}))
	Hosting.Domains.PoolSize = domains.Key("POOL_SIZE").MustInt(10)
	Hosting.Domains.TargetIP = domains.Key("TARGET_IP").MustString("")
	Hosting.Domains.CloudflareAPIToken = domains.Key("CLOUDFLARE_API_TOKEN").MustString("")
	Hosting.Domains.CloudflareZoneID = domains.Key("CLOUDFLARE_ZONE_ID").MustString("")
	Hosting.Domains.Proxied = domains.Key("PROXIED").MustBool(false)
	Hosting.Domains.AllowCustom = domains.Key("ALLOW_CUSTOM").MustBool(true)

	if Hosting.Enabled {
		if Hosting.DokployURL == "" || Hosting.DokployAPIKey == "" || Hosting.DokployEnvironmentID == "" {
			log.Fatal("[hosting] DOKPLOY_URL, DOKPLOY_API_KEY and DOKPLOY_ENVIRONMENT_ID are required when hosting is enabled")
		}
		if Hosting.CallbackSecret == "" {
			log.Fatal("[hosting] CALLBACK_SECRET is required when hosting is enabled")
		}
		if Hosting.Domains.BaseDomain == "" {
			log.Fatal("[hosting.domains] BASE_DOMAIN is required when hosting is enabled")
		}
		if Hosting.Domains.Provider == HostingDNSProviderCloudflare &&
			(Hosting.Domains.CloudflareAPIToken == "" || Hosting.Domains.CloudflareZoneID == "" || Hosting.Domains.TargetIP == "") {
			log.Fatal("[hosting.domains] cloudflare provider needs CLOUDFLARE_API_TOKEN, CLOUDFLARE_ZONE_ID and TARGET_IP")
		}
	}
}

// HostingRegistryHost returns the registry host apps are pushed to and pulled from.
func HostingRegistryHost() string {
	if Hosting.RegistryHost != "" {
		return Hosting.RegistryHost
	}
	return Domain
}
