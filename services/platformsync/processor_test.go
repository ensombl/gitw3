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
	"strings"
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
	release        *platformRelease
	provisionCalls int
	published      []map[string]any
	accreditations []w3ds.AccreditationDecision
	server         *httptest.Server
}

func newFakePlatformInfrastructure(t *testing.T) *fakePlatformInfrastructure {
	t.Helper()
	fake := &fakePlatformInfrastructure{
		manifest: w3ds.NewPlatformManifest(
			"guided-platform", "Guided Platform", "Initial description", "0.1.0",
			"https://guided.example.com", "", []string{"productivity", "work"}, "z0123456789",
		),
		manifestExists: true,
		release:        &platformRelease{TagName: "v0.1.0", Version: "0.1.0"},
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
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/alice/platform/commits":
		assert.Contains(t, []string{"commit-1", "commit-2"}, request.URL.Query().Get("sha"))
		if request.URL.Query().Get("page") == "1" {
			json.NewEncoder(response).Encode([]map[string]any{
				{"author": map[string]string{"login": "alice"}, "committer": map[string]string{"login": "alice"}},
				{"author": map[string]string{"login": "bob"}, "committer": map[string]string{"login": "platform-sync"}},
			})
		} else {
			json.NewEncoder(response).Encode([]any{})
		}
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/users/alice":
		json.NewEncoder(response).Encode(map[string]any{"login": "alice", "login_name": "@alice.w3id"})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/users/bob":
		json.NewEncoder(response).Encode(map[string]any{"login": "bob", "login_name": "@bob.w3id"})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/users/platform-sync":
		json.NewEncoder(response).Encode(map[string]any{"login": "platform-sync", "login_name": ""})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/alice/platform/releases/latest":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.release == nil {
			http.NotFound(response, request)
			return
		}
		json.NewEncoder(response).Encode(map[string]any{"tag_name": f.release.TagName})
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
		if strings.Contains(input["query"].(string), "PlatformAccreditations") {
			f.mu.Lock()
			edges := make([]map[string]any, 0, len(f.accreditations))
			for _, decision := range f.accreditations {
				edges = append(edges, map[string]any{"node": map[string]any{"parsed": decision}})
			}
			f.mu.Unlock()
			json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"metaEnvelopes": map[string]any{
				"edges": edges, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			}}})
			return
		}
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
		ListenAddr:          ":0",
		StatePath:           statePath,
		ForgejoURL:          baseURL,
		ForgejoToken:        "forgejo-token",
		WebhookSecret:       "webhook-secret",
		InternalToken:       "internal-token",
		RegistryURL:         baseURL,
		ProvisionerURL:      baseURL,
		VerificationID:      "verification",
		PublisherURL:        "https://gitw3.example.com",
		RequestTimeout:      time.Second,
		ReconcilePeriod:     time.Millisecond,
		AccreditationPeriod: time.Millisecond,
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
	assert.Equal(t, "v0.1.0", job.ReleaseTag)
	assert.Equal(t, "0.1.0", job.ReleaseVersion)
	assert.NotEmpty(t, job.EnvelopeID)
	require.NotNil(t, fake.manifest.EName)
	assert.Equal(t, "@guided.w3id", *fake.manifest.EName)
	assert.Equal(t, 1, fake.provisionCalls)
	require.Len(t, fake.published, 1)
	assert.Equal(t, false, fake.published[0]["isArchived"])
	assert.Equal(t, false, fake.published[0]["isActive"])
	assert.Equal(t, false, fake.published[0]["inSubmission"])
	assert.Equal(t, true, fake.published[0]["isDraft"])
	assert.Equal(t, []any{"@alice.w3id", "@bob.w3id"}, fake.published[0]["authorEnames"])
	assert.Equal(t, []any{"productivity", "work"}, fake.published[0]["domains"])
	assert.Equal(t, []any{"productivity", "work"}, fake.published[0]["requestedDomains"])
	assert.Equal(t, "", fake.published[0]["submissionVersion"])

	fake.mu.Lock()
	fake.accreditations = []w3ds.AccreditationDecision{
		{PlatformEName: "@guided.w3id", PlatformVersion: "0.0.9", Decision: "granted", Level: "L1", CreatedAt: "2026-08-27T00:00:00Z"},
		{PlatformEName: "@guided.w3id", PlatformVersion: "0.1.0", Decision: "denied", CreatedAt: "2026-08-28T00:00:00Z"},
		{PlatformEName: "@guided.w3id", PlatformVersion: "0.1.0", Decision: "granted", Level: "L2", CreatedAt: "2026-08-29T00:00:00Z"},
	}
	fake.mu.Unlock()
	require.NoError(t, processor.RefreshAccreditation(context.Background(), job))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NotNil(t, job.Decision)
	assert.Equal(t, "granted", job.Decision.Decision)
	assert.Equal(t, "L2", job.Decision.Level)
	assert.False(t, job.DecisionCheckedAt.IsZero())

	fake.mu.Lock()
	fake.accreditations = []w3ds.AccreditationDecision{
		{PlatformEName: "@guided.w3id", PlatformVersion: "0.0.9", Decision: "granted", Level: "L1", CreatedAt: "2026-08-29T00:00:00Z"},
	}
	fake.mu.Unlock()
	require.NoError(t, processor.RefreshAccreditation(context.Background(), job))
	job, err = store.Get(42)
	require.NoError(t, err)
	assert.Nil(t, job.Decision, "an older version's decision must not accredit the current version")

	fake.mu.Lock()
	fake.manifest.Description = "Updated description"
	fake.manifest.InSubmission = true
	fake.manifest.SubmissionVersion = "0.1.0"
	fake.manifest.IsDraft = false
	fake.release = &platformRelease{TagName: "v0.2.0", Version: "0.2.0"}
	fake.mu.Unlock()
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))
	assert.Equal(t, 1, fake.provisionCalls)
	require.Len(t, fake.published, 2)
	assert.Equal(t, "Updated description", fake.published[1]["description"])
	assert.Equal(t, true, fake.published[1]["isActive"])
	assert.Equal(t, true, fake.published[1]["inSubmission"])
	assert.Equal(t, "0.2.0", fake.published[1]["version"])
	assert.Equal(t, "0.2.0", fake.published[1]["submissionVersion"])
	assert.Equal(t, false, fake.published[1]["isDraft"])

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
	assert.Equal(t, false, fake.published[2]["isActive"])
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

func TestProcessorDoesNotCarryDecidedSubmissionToNewRelease(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, &http.Client{Timeout: time.Second})
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	fake.mu.Lock()
	fake.manifest.InSubmission = true
	fake.manifest.SubmissionVersion = "0.1.0"
	fake.release = &platformRelease{TagName: "v0.2.0", Version: "0.2.0"}
	fake.accreditations = []w3ds.AccreditationDecision{
		{PlatformEName: "@guided.w3id", PlatformVersion: "0.1.0", Decision: "granted", Level: "L2", CreatedAt: "2026-08-29T00:00:00Z"},
	}
	fake.mu.Unlock()
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Equal(t, "0.2.0", fake.manifest.Version)
	assert.False(t, fake.manifest.InSubmission)
	assert.Empty(t, fake.manifest.SubmissionVersion)
}
