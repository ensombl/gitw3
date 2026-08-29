// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validManifest() *PlatformManifest {
	return NewPlatformManifest(
		"my-platform", "My Platform", "A helpful W3DS platform", "0.1.0",
		"https://platform.example.com", "https://platform.example.com/logo.png", "Productivity", "z0123456789",
	)
}

func TestPlatformManifestValidate(t *testing.T) {
	require.NoError(t, validManifest().Validate(false))

	tests := []struct {
		name   string
		mutate func(*PlatformManifest)
	}{
		{"slug", func(m *PlatformManifest) { m.PlatformName = "Not A Slug" }},
		{"version", func(m *PlatformManifest) { m.Version = "first" }},
		{"submission version", func(m *PlatformManifest) { m.SubmissionVersion = "first" }},
		{"url", func(m *PlatformManifest) { m.URL = "http://example.com" }},
		{"category", func(m *PlatformManifest) { m.Category = "Unknown" }},
		{"public key", func(m *PlatformManifest) { m.PublicKey = "plain-key" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(manifest)
			assert.Error(t, manifest.Validate(false))
		})
	}
}

func TestPlatformManifestAllowsLocalHTTPInDevelopment(t *testing.T) {
	manifest := validManifest()
	manifest.URL = "http://localhost:5173"
	require.NoError(t, manifest.Validate(true))
	require.Error(t, manifest.Validate(false))
}

func TestPlatformManifestAllowsMissingURL(t *testing.T) {
	manifest := validManifest()
	manifest.URL = ""
	require.NoError(t, manifest.Validate(false))
}

func TestPlatformManifestMarshal(t *testing.T) {
	data, err := validManifest().Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), `"schemaVersion": 1`)
	assert.Contains(t, string(data), `"ename": null`)
	assert.Contains(t, string(data), `"inSubmission": false`)
	assert.NotContains(t, string(data), `"submissionVersion"`)
	assert.Contains(t, string(data), `"isDraft": true`)
	assert.Equal(t, byte('\n'), data[len(data)-1])
}

func TestPlatformManifestLifecycleDefaultsAreBackwardsCompatible(t *testing.T) {
	manifest := validManifest()
	assert.False(t, manifest.InSubmission)
	assert.True(t, manifest.IsDraft)

	var legacy PlatformManifest
	require.NoError(t, json.Unmarshal([]byte(`{
		"schemaVersion": 1,
		"platformName": "legacy",
		"displayName": "Legacy",
		"description": "An existing platform manifest",
		"version": "1.0.0",
		"ename": null,
		"url": "",
		"logoUrl": "",
		"category": "Other",
		"publicKey": "z0123456789"
	}`), &legacy))
	assert.False(t, legacy.InSubmission)
	assert.Empty(t, legacy.SubmissionVersion)
	assert.False(t, legacy.IsDraft)
	require.NoError(t, legacy.Validate(false))
}
