// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"errors"
	"os"
	"strings"
	"time"
)

// Config contains all deployment-specific platform publisher settings.
type Config struct {
	ListenAddr      string
	StatePath       string
	ForgejoURL      string
	ForgejoToken    string
	WebhookSecret   string
	InternalToken   string
	RegistryURL     string
	ProvisionerURL  string
	VerificationID  string
	PublisherURL    string
	RequestTimeout  time.Duration
	ReconcilePeriod time.Duration
}

// ConfigFromEnv loads the standalone service configuration.
func ConfigFromEnv() (Config, error) {
	config := Config{
		ListenAddr:      envOr("PLATFORM_SYNC_LISTEN_ADDR", ":8090"),
		StatePath:       envOr("PLATFORM_SYNC_STATE_PATH", "data/platform-manifest-sync.db"),
		ForgejoURL:      strings.TrimRight(os.Getenv("PLATFORM_SYNC_FORGEJO_URL"), "/"),
		ForgejoToken:    os.Getenv("PLATFORM_SYNC_FORGEJO_TOKEN"),
		WebhookSecret:   os.Getenv("PLATFORM_SYNC_WEBHOOK_SECRET"),
		InternalToken:   os.Getenv("PLATFORM_SYNC_INTERNAL_TOKEN"),
		RegistryURL:     strings.TrimRight(os.Getenv("PLATFORM_SYNC_REGISTRY_URL"), "/"),
		ProvisionerURL:  strings.TrimRight(os.Getenv("PLATFORM_SYNC_PROVISIONER_URL"), "/"),
		VerificationID:  os.Getenv("PLATFORM_SYNC_VERIFICATION_ID"),
		PublisherURL:    os.Getenv("PLATFORM_SYNC_PUBLISHER_URL"),
		RequestTimeout:  20 * time.Second,
		ReconcilePeriod: 2 * time.Second,
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate rejects configurations that would make authentication or publication unsafe.
func (c Config) Validate() error {
	required := map[string]string{
		"PLATFORM_SYNC_FORGEJO_URL":     c.ForgejoURL,
		"PLATFORM_SYNC_FORGEJO_TOKEN":   c.ForgejoToken,
		"PLATFORM_SYNC_WEBHOOK_SECRET":  c.WebhookSecret,
		"PLATFORM_SYNC_INTERNAL_TOKEN":  c.InternalToken,
		"PLATFORM_SYNC_REGISTRY_URL":    c.RegistryURL,
		"PLATFORM_SYNC_PROVISIONER_URL": c.ProvisionerURL,
		"PLATFORM_SYNC_VERIFICATION_ID": c.VerificationID,
		"PLATFORM_SYNC_PUBLISHER_URL":   c.PublisherURL,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return errors.New(name + " is required")
		}
	}
	if c.RequestTimeout <= 0 || c.ReconcilePeriod <= 0 {
		return errors.New("request timeout and reconcile period must be positive")
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
