// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchPublicationStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v1/status/42", request.URL.Path)
		assert.Equal(t, "Bearer internal", request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "application/json")
		response.Write([]byte(`{"status":"published","ename":"@platform.w3id"}`))
	}))
	defer server.Close()

	status, err := FetchPublicationStatus(context.Background(), server.Client(), server.URL, "internal", 42)
	require.NoError(t, err)
	assert.Equal(t, "published", status.Status)
	assert.Equal(t, "@platform.w3id", status.EName)
}

func TestFetchPublicationStatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := FetchPublicationStatus(context.Background(), server.Client(), server.URL, "internal", 42)
	assert.True(t, errors.Is(err, ErrPublicationStatusNotFound))
}
