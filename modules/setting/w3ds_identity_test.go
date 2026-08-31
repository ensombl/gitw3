// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadW3DSIdentity(t *testing.T) {
	old := W3DSIdentity
	defer func() { W3DSIdentity = old }()

	cfg, err := NewConfigProviderFromData(`
[w3ds_identity]
AWARENESS_URL = https://awareness.example/
AWARENESS_API_KEY = aaas_test
AVATAR_ALLOWED_HOST_LIST = external,cdn.example
TIMEOUT = 3s
ONLY_AUTHENTICATION = false
`)
	require.NoError(t, err)

	loadW3DSIdentityFrom(cfg)
	assert.Equal(t, "https://awareness.example/", W3DSIdentity.AwarenessURL)
	assert.Equal(t, "aaas_test", W3DSIdentity.AwarenessAPIKey)
	assert.Equal(t, "external,cdn.example", W3DSIdentity.AvatarAllowedHostList)
	assert.Equal(t, 3*time.Second, W3DSIdentity.Timeout)
	assert.False(t, W3DSIdentity.OnlyAuthentication)
}

func TestLoadW3DSIdentityDefaultsToOnlyAuthentication(t *testing.T) {
	old := W3DSIdentity
	defer func() { W3DSIdentity = old }()

	cfg, err := NewConfigProviderFromData("")
	require.NoError(t, err)

	loadW3DSIdentityFrom(cfg)

	assert.True(t, W3DSIdentity.OnlyAuthentication)
}
