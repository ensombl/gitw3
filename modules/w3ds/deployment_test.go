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
	signingPayload, err := DeploymentSigningPayload(payload)
	require.NoError(t, err)
	assert.True(t, len(signingPayload) < 80)
	assert.Contains(t, signingPayload, DeploymentPayloadPrefix)
}

func TestSoftwareVersionEName(t *testing.T) {
	eName, err := SoftwareVersionEName("@0699e093-2dd9-59cc-a416-7dc69623ebfd", "1.2.3")
	require.NoError(t, err)
	assert.Equal(t, "@c4cc7cd1-8670-5a37-8a7b-8ebc0b6022d8", eName)

	_, err = SoftwareVersionEName("@not-a-uuid", "1.2.3")
	assert.Error(t, err)
	_, err = SoftwareVersionEName("@0699e093-2dd9-59cc-a416-7dc69623ebfd", "")
	assert.Error(t, err)
}
