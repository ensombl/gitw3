// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validManifest() *PlatformManifest {
	return NewPlatformManifest(
		"my-platform", "My Platform", "A helpful W3DS platform", "0.1.0",
		"https://platform.example.com", "https://platform.example.com/logo.png", []string{"productivity", "work"}, "z0123456789",
	)
}

func submitManifest(t *testing.T, manifest *PlatformManifest) {
	t.Helper()
	eName := "@my-platform"
	manifest.EName = &eName
	statement := PPASubmissionStatement{
		Type:             PPASubmissionStatementType,
		SchemaVersion:    1,
		RepositoryID:     42,
		Repository:       "alice/my-platform",
		PlatformEName:    eName,
		PlatformName:     manifest.PlatformName,
		ReleaseTag:       "v" + manifest.Version,
		Version:          manifest.Version,
		ManifestCommitID: "0123456789abcdef",
		Domains:          append([]string(nil), manifest.Domains...),
		SignerEName:      "@alice",
		IssuedAt:         time.Now().UTC().Format(time.RFC3339),
		Nonce:            "nonce",
	}
	payload, err := statement.SigningPayload()
	require.NoError(t, err)
	manifest.InSubmission = true
	manifest.SubmissionVersion = manifest.Version
	manifest.SubmissionProof = &PPASubmissionProof{
		Statement:             statement,
		Payload:               payload,
		Signature:             "wallet-signature",
		PublicKey:             "zpublic-key",
		KeyBindingCertificate: "registry-certificate",
		VerifiedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	manifest.SubmissionHistory = []PPASubmissionProof{*manifest.SubmissionProof}
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
		{"domains missing", func(m *PlatformManifest) { m.Domains = nil }},
		{"domain malformed", func(m *PlatformManifest) { m.Domains = []string{"Not Valid"} }},
		{"domain duplicate", func(m *PlatformManifest) { m.Domains = []string{"work", "work"} }},
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

func TestPlatformManifestValidatesSubmissionProof(t *testing.T) {
	manifest := validManifest()
	submitManifest(t, manifest)
	require.NoError(t, manifest.Validate(false))

	t.Run("signed response to denial", func(t *testing.T) {
		reapplication := *manifest
		proof := *manifest.SubmissionProof
		proof.Statement.PreviousDecision = "denied"
		proof.Statement.PreviousDecisionAt = time.Now().UTC().Format(time.RFC3339)
		proof.Statement.ResponseToDecision = "We addressed the review feedback."
		payload, err := proof.Statement.SigningPayload()
		require.NoError(t, err)
		proof.Payload = payload
		reapplication.SubmissionProof = &proof
		require.NoError(t, reapplication.Validate(false))
	})

	t.Run("missing proof", func(t *testing.T) {
		invalid := *manifest
		invalid.SubmissionProof = nil
		assert.ErrorContains(t, invalid.Validate(false), "wallet signature proof")
	})
	t.Run("wrong version", func(t *testing.T) {
		invalid := *manifest
		invalid.SubmissionVersion = "0.2.0"
		assert.ErrorContains(t, invalid.Validate(false), "current platform version")
	})
	t.Run("changed domains", func(t *testing.T) {
		invalid := *manifest
		invalid.Domains = []string{"identity"}
		assert.ErrorContains(t, invalid.Validate(false), "application domains")
	})
	t.Run("changed statement", func(t *testing.T) {
		invalid := *manifest
		proof := *manifest.SubmissionProof
		proof.Statement.Repository = "mallory/platform"
		invalid.SubmissionProof = &proof
		assert.ErrorContains(t, invalid.Validate(false), "payload does not match")
	})
	t.Run("proof without submission", func(t *testing.T) {
		invalid := *manifest
		invalid.InSubmission = false
		assert.ErrorContains(t, invalid.Validate(false), "require inSubmission")
	})
	t.Run("response without denial", func(t *testing.T) {
		invalid := *manifest
		proof := *manifest.SubmissionProof
		proof.Statement.ResponseToDecision = "Please reconsider."
		payload, err := proof.Statement.SigningPayload()
		require.NoError(t, err)
		proof.Payload = payload
		invalid.SubmissionProof = &proof
		assert.ErrorContains(t, invalid.Validate(false), "response requires a previous denial")
	})
	t.Run("duplicate history proof", func(t *testing.T) {
		invalid := *manifest
		invalid.SubmissionHistory = append(invalid.SubmissionHistory, invalid.SubmissionHistory[0])
		assert.ErrorContains(t, invalid.Validate(false), "duplicate signed submissions")
	})
}

func TestPlatformManifestMarshal(t *testing.T) {
	data, err := validManifest().Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), `"schemaVersion": 1`)
	assert.Contains(t, string(data), `"ename": null`)
	assert.Contains(t, string(data), `"domains": [`)
	assert.NotContains(t, string(data), `"category"`)
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

func TestNormalizeReleaseVersion(t *testing.T) {
	for _, test := range []struct {
		tag     string
		version string
		valid   bool
	}{
		{"v1.2.3", "1.2.3", true},
		{"1.2.3-beta.1", "1.2.3-beta.1", true},
		{"V2.0.0", "2.0.0", true},
		{"latest", "latest", false},
	} {
		version, valid := NormalizeReleaseVersion(test.tag)
		assert.Equal(t, test.version, version)
		assert.Equal(t, test.valid, valid)
	}
}
