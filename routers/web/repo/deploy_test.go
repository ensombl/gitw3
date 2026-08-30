// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentSigningURIIncludesMatchingSessionIDs(t *testing.T) {
	const sessionID = "w3ds-deployment:v1:signed-payload"
	const callbackURL = "http://192.168.0.235:3000/w3ds/deploy/callback"
	walletURI, err := deploymentSigningURI(
		callbackURL, sessionID, "Deploy Example 1.0.0 as Production",
		"@deployment", "@version",
	)
	require.NoError(t, err)

	parsed, err := url.Parse(walletURI)
	require.NoError(t, err)
	assert.Equal(t, "w3ds", parsed.Scheme)
	assert.Equal(t, "sign", parsed.Host)
	assert.Equal(t, sessionID, parsed.Query().Get("session"))
	assert.Equal(t, callbackURL, parsed.Query().Get("redirect_uri"))

	displayJSON, err := base64.StdEncoding.DecodeString(parsed.Query().Get("data"))
	require.NoError(t, err)
	var display map[string]string
	require.NoError(t, json.Unmarshal(displayJSON, &display))
	assert.Equal(t, sessionID, display["sessionId"])
	assert.Equal(t, "@deployment", display["deploymentEName"])
	assert.Equal(t, "@version", display["versionEName"])
}
