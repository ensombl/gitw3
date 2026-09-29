// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3dsidentity

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dataURIAvatar(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: shade, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestApplyAvatar(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// Profiles embed photos as data URIs; GitW3 used to drop them.
	first := dataURIAvatar(t, 10)
	changed, err := ApplyAvatar(ctx, u, first)
	require.NoError(t, err)
	assert.True(t, changed)
	u = unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	assert.True(t, u.UseCustomAvatar)
	avatar := u.Avatar

	changed, err = ApplyAvatar(ctx, u, first)
	require.NoError(t, err)
	assert.False(t, changed, "an unchanged profile photo is not stored again")

	changed, err = ApplyAvatar(ctx, u, dataURIAvatar(t, 200))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotEqual(t, avatar, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}).Avatar)

	// Relative paths point at whichever platform wrote the profile.
	changed, err = ApplyAvatar(ctx, u, "/api/w3ds/avatar/622e3eb37ba4fa4e8389eecd4588ec85")
	require.NoError(t, err)
	assert.False(t, changed)
}
