// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"forgejo.org/modules/w3ds"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePlatformInfrastructure struct {
	mu             sync.Mutex
	manifest       *w3ds.PlatformManifest
	manifestExists bool
	provisionCalls int
	published      []map[string]any
	server         *httptest.Server
}

func newFakePlatformInfrastructure(t *testing.T) *fakePlatformInfrastructure {
	t.Helper()
	fake := &fakePlatformInfrastructure{
		manifest: w3ds.NewPlatformManifest(
			"guided-platform", "Guided Platform", "Initial description", "0.1.0",
			"https://guided.example.com", "", "Productivity", "z0123456789",
		),
		manifestExists: true,
	}
	fake.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fake.handle(t, response, request)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakePlatformInfrastructure) handle(t *testing.T, response http.ResponseWriter, request *http.Request) {
	t.Helper()
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/alice/platform/contents/.w3ds/platform.json":
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.manifestExists {
			http.NotFound(response, request)
			return
		}
		data, err := f.manifest.Marshal()
		require.NoError(t, err)
		content := base64.StdEncoding.EncodeToString(data)
		json.NewEncoder(response).Encode(map[string]any{"sha": "blob-sha", "content": content})
	case request.Method == http.MethodPut && request.URL.Path == "/api/v1/repos/alice/platform/contents/.w3ds/platform.json":
		var input struct {
			Content string `json:"content"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		data, err := base64.StdEncoding.DecodeString(input.Content)
		require.NoError(t, err)
		var manifest w3ds.PlatformManifest
		require.NoError(t, json.Unmarshal(data, &manifest))
		f.mu.Lock()
		f.manifest = &manifest
		f.mu.Unlock()
		json.NewEncoder(response).Encode(map[string]any{"content": map[string]string{"sha": "updated"}})
	case request.Method == http.MethodGet && request.URL.Path == "/entropy":
		json.NewEncoder(response).Encode(map[string]string{"token": "entropy"})
	case request.Method == http.MethodPost && request.URL.Path == "/provision":
		var input map[string]string
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		assert.Equal(t, "z0123456789", input["publicKey"])
		f.mu.Lock()
		f.provisionCalls++
		f.mu.Unlock()
		json.NewEncoder(response).Encode(map[string]any{"success": true, "w3id": "@guided.w3id", "uri": f.server.URL})
	case request.Method == http.MethodGet && request.URL.Path == "/resolve":
		assert.Equal(t, "@guided.w3id", request.URL.Query().Get("w3id"))
		json.NewEncoder(response).Encode(map[string]string{"uri": f.server.URL})
	case request.Method == http.MethodPost && request.URL.Path == "/platforms/certification":
		json.NewEncoder(response).Encode(map[string]any{"token": "platform-token", "expiresAt": time.Now().Add(time.Hour).UnixMilli()})
	case request.Method == http.MethodPost && request.URL.Path == "/graphql":
		assert.Equal(t, "Bearer platform-token", request.Header.Get("Authorization"))
		assert.Equal(t, "@guided.w3id", request.Header.Get("X-ENAME"))
		var input map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		assert.NotContains(t, input["query"], "errors { message }")
		variables := input["variables"].(map[string]any)
		profile := variables["input"].(map[string]any)["payload"].(map[string]any)
		f.mu.Lock()
		f.published = append(f.published, profile)
		f.mu.Unlock()
		json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"updateMetaEnvelopeById": map[string]any{"metaEnvelope": map[string]string{"id": variables["id"].(string)}}}})
	default:
		http.Error(response, request.Method+" "+request.URL.Path, http.StatusNotFound)
	}
}

func testConfig(baseURL, statePath string) Config {
	return Config{
		ListenAddr:      ":0",
		StatePath:       statePath,
		ForgejoURL:      baseURL,
		ForgejoToken:    "forgejo-token",
		WebhookSecret:   "webhook-secret",
		InternalToken:   "internal-token",
		RegistryURL:     baseURL,
		ProvisionerURL:  baseURL,
		VerificationID:  "verification",
		PublisherURL:    "https://gitw3.example.com",
		RequestTimeout:  time.Second,
		ReconcilePeriod: time.Millisecond,
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestProcessorCreatesUpdatesAndArchivesProfile(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	store := openTestStore(t)
	config := testConfig(fake.server.URL, "")
	processor := NewProcessor(config, store, &http.Client{Timeout: time.Second})

	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	job, err = store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, job.Status)
	assert.Equal(t, "@guided.w3id", job.EName)
	assert.NotEmpty(t, job.EnvelopeID)
	require.NotNil(t, fake.manifest.EName)
	assert.Equal(t, "@guided.w3id", *fake.manifest.EName)
	assert.Equal(t, 1, fake.provisionCalls)
	require.Len(t, fake.published, 1)
	assert.Equal(t, false, fake.published[0]["isArchived"])

	fake.mu.Lock()
	fake.manifest.Description = "Updated description"
	fake.mu.Unlock()
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))
	assert.Equal(t, 1, fake.provisionCalls)
	require.Len(t, fake.published, 2)
	assert.Equal(t, "Updated description", fake.published[1]["description"])

	fake.mu.Lock()
	fake.manifestExists = false
	fake.mu.Unlock()
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-3", true))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))
	job, err = store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusArchived, job.Status)
	require.Len(t, fake.published, 3)
	assert.Equal(t, true, fake.published[2]["isArchived"])
}

func TestProcessorIgnoresRepositoriesWithoutManifest(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	fake.manifestExists = false
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, &http.Client{Timeout: time.Second})
	require.NoError(t, store.Schedule(7, "alice/platform", "main", "commit", false))
	job, err := store.Get(7)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))
	job, err = store.Get(7)
	require.NoError(t, err)
	assert.Nil(t, job)
}
