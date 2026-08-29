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
		response.Write([]byte(`{"status":"published","ename":"@platform.w3id","decision":{"platformEName":"@platform.w3id","platformVersion":"1.2.3","decision":"granted","level":"L2","createdAt":"2026-08-29T00:00:00Z"}}`))
	}))
	defer server.Close()

	status, err := FetchPublicationStatus(context.Background(), server.Client(), server.URL, "internal", 42)
	require.NoError(t, err)
	assert.Equal(t, "published", status.Status)
	assert.Equal(t, "@platform.w3id", status.EName)
	require.NotNil(t, status.Decision)
	assert.Equal(t, "1.2.3", status.Decision.PlatformVersion)
}

func TestFetchPublicationStatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := FetchPublicationStatus(context.Background(), server.Client(), server.URL, "internal", 42)
	assert.True(t, errors.Is(err, ErrPublicationStatusNotFound))
}
