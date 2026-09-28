// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forgejo.org/modules/w3ds"

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

func TestAuthorDiscoveryUsesNativeMaintainersWithoutCommitHistory(t *testing.T) {
	var maintainerRequests atomic.Int32
	var commitRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/api/v1/repos/alice/platform/assignees":
			maintainerRequests.Add(1)
			assert.Equal(t, "token safe-test-token", request.Header.Get("Authorization"))
			_ = json.NewEncoder(response).Encode([]any{
				map[string]string{"login": "alice", "login_name": "@alice"},
				map[string]string{"login": "bob", "login_name": "@bob"},
				map[string]string{"login": "duplicate", "login_name": "@alice"},
				map[string]string{"login": "unlinked", "login_name": ""},
				nil,
			})
		case request.URL.Path == "/api/v1/repos/alice/platform/commits":
			commitRequests.Add(1)
			http.Error(response, "commit history must not be requested", http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	client := newForgejoClient(Config{ForgejoURL: server.URL, ForgejoToken: "safe-test-token"}, server.Client())
	result, err := client.authorENames(context.Background(), "alice/platform")
	require.NoError(t, err)
	assert.Equal(t, []string{"@alice", "@bob"}, result)
	assert.Equal(t, int32(1), maintainerRequests.Load())
	assert.Zero(t, commitRequests.Load(), "even a repository with a very large history must not trigger commit pagination")
}

func TestPlatformPublicationConvergesAfterAmbiguousWrite(t *testing.T) {
	manifest := testPlatformManifest()
	var stored map[string]any
	var mutations atomic.Int32
	server := newPlatformProfileServer(t, &stored, &mutations, true)
	client := newW3DSClient(testConfig(server.URL, ""), server.Client())

	err := client.publish(context.Background(), "profile-id", manifest, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), false, []string{"@alice"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), mutations.Load())
	require.NotNil(t, stored)
}

func TestPlatformPublicationAcceptsPreexistingMatchingEnvelope(t *testing.T) {
	manifest := testPlatformManifest()
	createdAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	stored, err := platformProfilePayload(manifest, createdAt, false, []string{"@alice"})
	require.NoError(t, err)
	stored["updatedAt"] = "an-earlier-successful-write"
	var mutations atomic.Int32
	server := newPlatformProfileServer(t, &stored, &mutations, false)
	client := newW3DSClient(testConfig(server.URL, ""), server.Client())

	require.NoError(t, client.publish(context.Background(), "profile-id", manifest, createdAt, false, []string{"@alice"}))
	assert.Zero(t, mutations.Load())
}

func TestGraphQLErrorsPreserveSafeMetadata(t *testing.T) {
	err := graphQLErrors("eVault mutation", []graphQLError{{
		Message:    "denied without exposing credentials",
		Path:       []any{"updateMetaEnvelope", "metaEnvelope"},
		Extensions: map[string]any{"code": "FORBIDDEN"},
	}})
	stage, code, path := publicationErrorMetadata(err)
	assert.Equal(t, "eVault mutation", stage)
	assert.Equal(t, "FORBIDDEN", code)
	assert.Equal(t, "updateMetaEnvelope.metaEnvelope", path)
	assert.Contains(t, err.Error(), "code=FORBIDDEN")

	err = mutationPayloadErrors("eVault mutation", []mutationPayloadError{{Field: "input.payload", Message: "invalid profile", Code: "INVALID"}})
	stage, code, path = publicationErrorMetadata(err)
	assert.Equal(t, "eVault mutation", stage)
	assert.Equal(t, "INVALID", code)
	assert.Equal(t, "input.payload", path)
}

func TestSensitiveValuesAreRedactedFromErrors(t *testing.T) {
	body := []byte(`{"error":"failed","token":"legacy-token-value","authorization":"Bearer secret-value","walletSignature":"wallet-value","keyBindingCertificate":"certificate-value","detail":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzZWNyZXQifQ.signature"}`)
	safe := safeResponseBody(body)
	for _, secret := range []string{"legacy-token-value", "secret-value", "wallet-value", "certificate-value", "eyJhbGciOiJIUzI1NiJ9"} {
		assert.NotContains(t, safe, secret)
	}
	assert.Contains(t, safe, "[REDACTED]")
}

func TestConfiguredPlatformTokenSkipsLegacyRegistryMint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(response, "legacy mint must not be called", http.StatusGone)
	}))
	t.Cleanup(server.Close)
	config := testConfig(server.URL, "")
	config.PlatformToken = "pre-issued-platform-token"
	client := newW3DSClient(config, server.Client())

	token, err := client.platformToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pre-issued-platform-token", token)
	assert.Zero(t, requests.Load())
}

func testPlatformManifest() *w3ds.PlatformManifest {
	manifest := w3ds.NewPlatformManifest("platform", "Platform", "Description", "1.0.0", "https://platform.example", "", []string{"work"})
	ename := "@platform"
	manifest.EName = &ename
	manifest.IsDraft = false
	return manifest
}

func newPlatformProfileServer(t *testing.T, stored *map[string]any, mutations *atomic.Int32, ambiguous bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/resolve":
			_ = json.NewEncoder(response).Encode(map[string]string{"uri": server.URL})
		case "/platforms/certification":
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "platform-token"})
		case "/graphql":
			var input map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			query, _ := input["query"].(string)
			if strings.Contains(query, "ExistingPlatformProfile") {
				if *stored == nil {
					_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"profile": nil}})
					return
				}
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"profile": map[string]any{
					"id": "profile-id", "ontology": w3ds.UserProfileOntology, "parsed": *stored,
				}}})
				return
			}
			mutations.Add(1)
			variables := input["variables"].(map[string]any)
			*stored = variables["input"].(map[string]any)["payload"].(map[string]any)
			if ambiguous {
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"update": nil}, "errors": []map[string]any{{
					"message": "Unexpected error.", "path": []string{"update"}, "extensions": map[string]string{"code": "INTERNAL_SERVER_ERROR"},
				}}})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"update": map[string]any{"metaEnvelope": map[string]string{"id": "profile-id"}, "errors": []any{}}}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
