// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentENameMatchesUUIDv5(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"entropy":"www.widgets.com"}`))
	eName, err := deploymentEName("header."+payload+".signature", "6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	require.NoError(t, err)
	assert.Equal(t, "@21f7f8de-8051-5b89-8680-0195ef798b6a", eName)
}

func TestDeploymentENameRejectsInvalidEntropyToken(t *testing.T) {
	_, err := deploymentEName("not-a-token", "6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	assert.EqualError(t, err, "registry returned an invalid entropy token")
}
