// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDeploymentAttestation(t *testing.T) {
	deployment, version, payload, err := BuildDeploymentAttestation(
		"@deployment", "Singapore", "production", "@deployer", "@platform",
		"@version", "1.2.3", "v1.2.3", "ABCDEF0123456789ABCDEF0123456789ABCDEF01", "zKey",
	)
	require.NoError(t, err)
	assert.Equal(t, "deployment_key", deployment.Type)
	assert.Equal(t, "software_version", version.Type)
	var bundle DeploymentAttestationBundle
	require.NoError(t, json.Unmarshal([]byte(payload), &bundle))
	assert.Equal(t, DeploymentAttestationType, bundle.Type)
	assert.Len(t, bundle.Documents, 2)
	assert.Len(t, bundle.Documents[0].Hash, 64)
	assert.NotEqual(t, bundle.Documents[0].Hash, bundle.Documents[1].Hash)
}
