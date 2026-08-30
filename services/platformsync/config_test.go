// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequiredConfigEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"PLATFORM_SYNC_FORGEJO_URL":     "http://localhost:3000",
		"PLATFORM_SYNC_FORGEJO_TOKEN":   "forgejo-token",
		"PLATFORM_SYNC_WEBHOOK_SECRET":  "webhook-secret",
		"PLATFORM_SYNC_INTERNAL_TOKEN":  "internal-token",
		"PLATFORM_SYNC_VERIFICATION_ID": "verification-id",
		"PLATFORM_SYNC_PUBLISHER_URL":   "http://localhost:8090",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestConfigDefaultsToProductionW3DS(t *testing.T) {
	setRequiredConfigEnvironment(t)
	t.Setenv("PLATFORM_SYNC_REGISTRY_URL", "")
	t.Setenv("PLATFORM_SYNC_PROVISIONER_URL", "")

	config, err := ConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, ProductionRegistryURL, config.RegistryURL)
	assert.Equal(t, ProductionProvisionerURL, config.ProvisionerURL)
}

func TestConfigAllowsW3DSOverride(t *testing.T) {
	setRequiredConfigEnvironment(t)
	t.Setenv("PLATFORM_SYNC_REGISTRY_URL", "http://localhost:4321/")
	t.Setenv("PLATFORM_SYNC_PROVISIONER_URL", "http://localhost:3001/")

	config, err := ConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:4321", config.RegistryURL)
	assert.Equal(t, "http://localhost:3001", config.ProvisionerURL)
}
