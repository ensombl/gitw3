// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hosting runs GitW3 managed deployments: tagged releases are built
// by the central builder workflow on an untrusted build node, pushed to the
// registry by digest and rolled out to the Dokploy-managed Docker Swarm.
package hosting

import (
	"context"
	"errors"
	"strings"
	"sync"

	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	notify_service "forgejo.org/services/notify"
)

// Clients holds the external systems the deploy service talks to. Tests
// replace them with fakes through SetClients.
type Clients struct {
	Dokploy   DokployClient
	Swarm     SwarmClient
	Builder   BuilderClient
	Registry  RegistryClient
	DNS       DNSProvider
	Publisher Publisher
}

var (
	clientsMu sync.RWMutex
	clients   Clients
)

// ErrDisabled is returned when managed hosting is not configured.
var ErrDisabled = errors.New("managed hosting is not enabled on this instance")

// Init wires the deploy service when hosting is enabled.
func Init(ctx context.Context) error {
	if !setting.Hosting.Enabled {
		return nil
	}
	SetClients(Clients{
		Dokploy:   NewDokployClient(),
		Swarm:     NewSwarmClient(),
		Builder:   NewBuilderClient(),
		Registry:  NewRegistryClient(),
		DNS:       NewDNSProvider(),
		Publisher: NewPublisher(),
	})
	notify_service.RegisterNotifier(newNotifier())
	log.Info("Managed hosting enabled: Dokploy %s, builder %s", setting.Hosting.DokployURL, setting.Hosting.BuilderRepo)
	return nil
}

// SetClients replaces the external clients (used by Init and tests).
func SetClients(c Clients) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients = c
}

func current() Clients {
	clientsMu.RLock()
	defer clientsMu.RUnlock()
	return clients
}

// Enabled reports whether managed hosting can be used.
func Enabled() bool {
	return setting.Hosting.Enabled && current().Dokploy != nil
}

// callbackURL is where the builder reports a finished build.
func callbackURL() string {
	return strings.TrimRight(setting.AppURL, "/") + "/-/hosting/callback"
}

// sourceURL is the signed, expiring archive URL for one build job.
func sourceURL(jobID int64, query string) string {
	return strings.TrimRight(setting.AppURL, "/") + "/-/hosting/source/" + itoa(jobID) + "?" + query
}
