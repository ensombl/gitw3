// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSlugFromDisplayName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"My Excellent App": "my-excellent-app",
		"  déjà---Vu!  ":   "deja-vu",
		"W3DS + Friends":   "w3ds-friends",
		"Straße":           "strasse",
		"你好":               "platform",
		"":                 "platform",
	}
	for input, expected := range tests {
		assert.Equal(t, expected, SlugFromDisplayName(input))
	}
	assert.LessOrEqual(t, len(SlugFromDisplayName(strings.Repeat("long name ", 30))), 100)
}
