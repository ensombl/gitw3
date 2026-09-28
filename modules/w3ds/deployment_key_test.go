// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"strings"
	"testing"

	"forgejo.org/modules/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentKeySignAndVerify(t *testing.T) {
	publicKey, keyFile, err := GenerateDeploymentKey()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(publicKey, "z"))
	file := DeploymentKeyFile{}
	require.NoError(t, json.Unmarshal([]byte(keyFile), &file))
	assert.Equal(t, DeploymentKeyFormat, file.Format)
	assert.Equal(t, publicKey, file.PublicKey)

	decoded, err := decodeBase58BTC(publicKey[1:])
	require.NoError(t, err)
	assert.Equal(t, publicKey[1:], encodeBase58BTC(decoded))

	payload, err := DeploymentVersionPayload("@deployment", "1.2.0", "v1.2.0", "ABC")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(payload, DeploymentVersionPayloadPrefix))
	signature, err := SignWithDeploymentKey(keyFile, payload)
	require.NoError(t, err)
	assert.True(t, VerifyDeploymentKeySignature(publicKey, payload, signature))

	other, err := DeploymentVersionPayload("@deployment", "1.3.0", "v1.3.0", "abc")
	require.NoError(t, err)
	assert.False(t, VerifyDeploymentKeySignature(publicKey, other, signature))
	otherKey, _, err := GenerateDeploymentKey()
	require.NoError(t, err)
	assert.False(t, VerifyDeploymentKeySignature(otherKey, payload, signature))

	_, err = DeploymentVersionPayload("", "1", "v1", "a")
	assert.Error(t, err)
}
