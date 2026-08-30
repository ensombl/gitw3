// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	manifest := w3ds.NewPlatformManifest(
		"guided-platform", "Guided Platform", "Initial description", "0.1.0",
		"https://guided.example.com", "", []string{"productivity", "work"},
	)
	manifest.PublicKey = "z0123456789"
	fake := &fakePlatformInfrastructure{
		manifest:       manifest,
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

func addSubmissionProof(t *testing.T, manifest *w3ds.PlatformManifest, repository string, repositoryID int64) {
	t.Helper()
	require.NotNil(t, manifest.EName)
	statement := w3ds.PPASubmissionStatement{
		Type:             w3ds.PPASubmissionStatementType,
		SchemaVersion:    1,
		RepositoryID:     repositoryID,
		Repository:       repository,
		PlatformEName:    *manifest.EName,
		PlatformName:     manifest.PlatformName,
		ReleaseTag:       "v" + manifest.Version,
		Version:          manifest.Version,
		ManifestCommitID: "commit-before-submission",
		Domains:          append([]string(nil), manifest.Domains...),
		SignerEName:      "@alice.w3id",
		IssuedAt:         time.Now().UTC().Format(time.RFC3339),
		Nonce:            "submission-nonce",
	}
	payload, err := statement.SigningPayload()
	require.NoError(t, err)
	manifest.InSubmission = true
	manifest.SubmissionVersion = manifest.Version
	manifest.SubmissionProof = &w3ds.PPASubmissionProof{
		Statement:             statement,
		Payload:               payload,
		Signature:             "wallet-signature",
		PublicKey:             "zpublic-key",
		KeyBindingCertificate: "registry-certificate",
		VerifiedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	manifest.SubmissionHistory = []w3ds.PPASubmissionProof{*manifest.SubmissionProof}
}

func TestProcessorPreparesAndFinalizesDeployment(t *testing.T) {
	entropyPayload, err := json.Marshal(map[string]string{"entropy": "test-entropy-1234567890abcdef"})
	require.NoError(t, err)
	entropyToken := "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(entropyPayload) + ".signature"
	var server *httptest.Server
	provisioned := false
	createdDocuments := 0
	profilePublished := false
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/entropy":
			_ = json.NewEncoder(response).Encode(map[string]string{"token": entropyToken})
		case "/platforms/certification":
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "platform-token"})
		case "/records/software-versions":
			assert.True(t, provisioned, "version registration must follow deployment provisioning")
			assert.Equal(t, 2, createdDocuments, "version registration must follow signed document publication")
			assert.True(t, profilePublished, "version registration must follow deployment profile publication")
			assert.Equal(t, "Bearer platform-token", request.Header.Get("Authorization"))
			_ = json.NewEncoder(response).Encode(map[string]string{"ename": "@c4cc7cd1-8670-5a37-8a7b-8ebc0b6022d8"})
		case "/resolve":
			if !provisioned {
				http.NotFound(response, request)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"uri": server.URL})
		case "/provision":
			var input map[string]string
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			w3id, deriveErr := deploymentEName(input["registryEntropy"], input["namespace"])
			require.NoError(t, deriveErr)
			provisioned = true
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "w3id": w3id})
		case "/graphql":
			var input map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			query := input["query"].(string)
			switch {
			case strings.Contains(query, "ExistingDeploymentBindings"):
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"bindingDocuments": map[string]any{"edges": []any{}}}})
			case strings.Contains(query, "CreateDeploymentBinding"):
				createdDocuments++
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"createBindingDocument": map[string]any{"metaEnvelopeId": fmt.Sprintf("document-%d", createdDocuments), "errors": []any{}}}})
			case strings.Contains(query, "PublishDeployment"):
				profilePublished = true
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"updateMetaEnvelopeById": map[string]any{"metaEnvelope": map[string]string{"id": "profile"}}}})
			}
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	store := openTestStore(t)
	processor := NewProcessor(testConfig(server.URL, ""), store, server.Client())
	job, err := processor.PrepareDeployment(context.Background(), PrepareDeploymentRequest{
		ID: "deployment-1", RepositoryID: 42,
		PlatformEName:  "@0699e093-2dd9-59cc-a416-7dc69623ebfd",
		DeploymentName: "Singapore", Environment: "production", DeployerEName: "@deployer",
		Version: "1.2.3", ReleaseTag: "v1.2.3", CommitSHA: strings.Repeat("a", 40), PublicKey: "zKey",
	})
	require.NoError(t, err)
	assert.Equal(t, DeploymentAwaitingSignature, job.Status)
	assert.False(t, provisioned, "preparing a signing request must not create W3DS resources")
	assert.Zero(t, createdDocuments, "preparing a signing request must not publish documents")
	expectedDeploymentEName, err := deploymentEName(entropyToken, job.Namespace)
	require.NoError(t, err)
	assert.Equal(t, expectedDeploymentEName, job.DeploymentEName)
	assert.Equal(t, "@c4cc7cd1-8670-5a37-8a7b-8ebc0b6022d8", job.VersionEName)
	assert.Contains(t, job.BundlePayload, w3ds.DeploymentAttestationType)

	require.NoError(t, processor.FinalizeDeployment(FinalizeDeploymentRequest{
		SignerEName: "@deployer", Signature: "wallet-signature", KeyBindingCertificate: "certificate",
	}, job))
	stored, err := store.GetDeployment(job.ID)
	require.NoError(t, err)
	assert.Equal(t, DeploymentPublishing, stored.Status)
	assert.Equal(t, "wallet-signature", stored.WalletSignature)

	require.NoError(t, processor.ReconcileDeployment(context.Background(), stored))
	stored, err = store.GetDeployment(job.ID)
	require.NoError(t, err)
	assert.Equal(t, DeploymentCompleted, stored.Status)
	assert.Equal(t, "document-1", stored.DeploymentKeyDocumentID)
	assert.Equal(t, "document-2", stored.SoftwareVersionDocumentID)
	assert.True(t, provisioned)
	assert.Empty(t, stored.RegistryEntropy)
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
	require.Len(t, job.Decisions, 2)
	assert.Equal(t, "denied", job.Decisions[0].Decision)
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
	assert.Empty(t, job.Decisions)

	fake.mu.Lock()
	fake.manifest.Description = "Updated description"
	addSubmissionProof(t, fake.manifest, "alice/platform", 42)
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
	assert.Equal(t, false, fake.published[1]["inSubmission"])
	assert.Equal(t, "0.2.0", fake.published[1]["version"])
	assert.Equal(t, "", fake.published[1]["submissionVersion"])
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

func TestProcessorDefersIdentityUntilFirstDeployment(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	fake.manifest.PublicKey = ""
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, &http.Client{Timeout: time.Second})

	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	job, err = store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusAwaitingDeploy, job.Status)
	assert.Empty(t, job.EName)
	assert.Zero(t, fake.provisionCalls)

	job, err = processor.BootstrapPlatformIdentity(context.Background(), BootstrapPlatformRequest{
		RepositoryID:  42,
		FullName:      "alice/platform",
		DefaultBranch: "main",
		PublicKey:     "z0123456789",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, job.Status)
	assert.Equal(t, "@guided.w3id", job.EName)
	assert.Equal(t, "z0123456789", fake.manifest.PublicKey)
	assert.Equal(t, 1, fake.provisionCalls)
	assert.Empty(t, job.ProvisioningKey)
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

func TestProcessorClearsSubmissionOnNewRelease(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, &http.Client{Timeout: time.Second})
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	fake.mu.Lock()
	addSubmissionProof(t, fake.manifest, "alice/platform", 42)
	fake.release = &platformRelease{TagName: "v0.2.0", Version: "0.2.0"}
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
	assert.Nil(t, fake.manifest.SubmissionProof)
}

func TestProcessorPublishesSubmissionProof(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, &http.Client{Timeout: time.Second})
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	fake.mu.Lock()
	addSubmissionProof(t, fake.manifest, "alice/platform", 42)
	fake.mu.Unlock()
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.published, 2)
	proof, ok := fake.published[1]["submissionProof"].(map[string]any)
	require.True(t, ok)
	statement, ok := proof["statement"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "0.1.0", statement["version"])
	assert.Equal(t, "@alice.w3id", statement["signerEName"])
	assert.Equal(t, "@alice.w3id", fake.published[1]["submittedBy"])
	history, ok := fake.published[1]["submissionHistory"].([]any)
	require.True(t, ok)
	require.Len(t, history, 1)
}
