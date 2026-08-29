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

func TestFetchDomains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/domains", request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"schemaId":"domain-schema",
			"domains":[
				{"id":"identity","label":"Identity","description":"Profiles and credentials."},
				{"id":"public","label":"Public","description":"Public services."}
			]
		}`))
	}))
	defer server.Close()

	catalog, err := FetchDomains(t.Context(), server.Client(), server.URL)
	require.NoError(t, err)
	assert.Equal(t, "domain-schema", catalog.SchemaID)
	require.Len(t, catalog.Domains, 2)
	assert.Equal(t, "identity", catalog.Domains[0].ID)
	require.NoError(t, ValidateSelectedDomains([]string{"identity", "public"}, catalog))
	assert.Error(t, ValidateSelectedDomains([]string{"unknown"}, catalog))
}

func TestFetchDomainsRejectsInvalidCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"schemaId":"domain-schema","domains":[{"id":"Bad Domain","label":"Bad"}]}`))
	}))
	defer server.Close()

	_, err := FetchDomains(t.Context(), server.Client(), server.URL)
	assert.Error(t, err)
}
