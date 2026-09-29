// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchPersonProfile(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "Bearer aaas_test", r.Header.Get("Authorization"))
		assert.Equal(t, "@person-1", r.URL.Query().Get("evault"))
		assert.Equal(t, UserProfileOntology, r.URL.Query().Get("ontology"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"packets":[{"data":{"displayName":"Old name","avatarUrl":"https://example.com/old.png"}},{"data":{"platformName":"not-a-person","displayName":"Wrong name"}}],"hasMore":true,"nextCursor":"page-2"}`))
			return
		}
		assert.Equal(t, "page-2", r.URL.Query().Get("cursor"))
		_, _ = w.Write([]byte(`{"packets":[{"data":{"name":"Current name","avatar":"https://example.com/current.png"}}],"hasMore":false,"nextCursor":null}`))
	}))
	t.Cleanup(server.Close)

	profile, err := FetchPersonProfile(t.Context(), server.Client(), server.URL, "aaas_test", "person-1")
	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, "Current name", profile.DisplayName)
	assert.Equal(t, "https://example.com/current.png", profile.AvatarURL)
	assert.Equal(t, 2, requests)
}

func TestFetchPersonProfileReturnsNilWithoutPersonPacket(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"packets":[{"data":{"platformName":"app","displayName":"An app"}}],"hasMore":false,"nextCursor":null}`))
	}))
	t.Cleanup(server.Close)

	profile, err := FetchPersonProfile(t.Context(), server.Client(), server.URL, "aaas_test", "@person-1")
	require.NoError(t, err)
	assert.Nil(t, profile)
}

func TestFetchPersonProfileKeepsNewestUsableAvatar(t *testing.T) {
	t.Parallel()

	// As seen in production: a platform wrote an embedded photo, then a later
	// update with a path relative to itself, which GitW3 cannot resolve.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"packets":[` +
			`{"data":{"displayName":"Ada","avatarUrl":"data:image/png;base64,iVBORw0KGgo="}},` +
			`{"data":{"displayName":"Ada L","avatarUrl":"/api/w3ds/avatar/622e3eb37ba4fa4e8389eecd4588ec85"}}` +
			`],"hasMore":false,"nextCursor":null}`))
	}))
	t.Cleanup(server.Close)

	profile, err := FetchPersonProfile(t.Context(), server.Client(), server.URL, "aaas_test", "@person-1")
	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, "Ada L", profile.DisplayName, "the name still follows the newest update")
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo=", profile.AvatarURL)
}

func TestUsableAvatar(t *testing.T) {
	t.Parallel()

	for source, usable := range map[string]bool{
		"https://cdn.example.com/a.png":           true,
		"http://cdn.example.com/a.png":            true,
		"data:image/webp;base64,UklGRiL0AABX":     true,
		"data:image/jpeg;base64,/9j/4AAQSkZJRg==": true,
		"/assets/twitter-avatar.jpg":              false,
		"/api/w3ds/avatar/faa38a29b941854e":       false,
		"data:text/html;base64,PGgxPg==":          false,
		"data:image/png,not-base64":               false,
		"javascript:alert(1)":                     false,
		"":                                        false,
	} {
		assert.Equal(t, usable, UsableAvatar(source), source)
	}

	data, ok := DecodeDataImage("data:image/png;base64,iVBORw0KGgo=")
	require.True(t, ok)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\n"), data)
}
