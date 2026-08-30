// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	mu                     sync.Mutex
	manifest               *w3ds.PlatformManifest
	manifestExists         bool
	release                *platformRelease
	provisionCalls         int
	activationCalls        int
	manifestUpdateFailures int
	provisionedKeys        []string
	published              []map[string]any
	accreditations         []w3ds.AccreditationDecision
	server                 *httptest.Server
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
		f.mu.Lock()
		if f.manifestUpdateFailures > 0 {
			f.manifestUpdateFailures--
			f.mu.Unlock()
			http.Error(response, "temporary Forgejo failure", http.StatusServiceUnavailable)
			return
		}
		f.mu.Unlock()
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
		entropyPayload, err := json.Marshal(map[string]string{"entropy": "platform-test-entropy"})
		require.NoError(t, err)
		json.NewEncoder(response).Encode(map[string]string{
			"token": "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(entropyPayload) + ".signature",
		})
	case request.Method == http.MethodPost && request.URL.Path == "/provision":
		var input map[string]string
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		f.mu.Lock()
		f.provisionCalls++
		f.provisionedKeys = append(f.provisionedKeys, input["publicKey"])
		reserved := f.manifest.PublicKey == ""
		f.mu.Unlock()
		w3id := "@guided.w3id"
		if reserved {
			var deriveErr error
			w3id, deriveErr = deploymentEName(input["registryEntropy"], input["namespace"])
			require.NoError(t, deriveErr)
		}
		json.NewEncoder(response).Encode(map[string]any{"success": true, "w3id": w3id, "uri": f.server.URL})
	case request.Method == http.MethodGet && request.URL.Path == "/resolve":
		f.mu.Lock()
		expectedEName := "@guided.w3id"
		if f.manifest.EName != nil {
			expectedEName = *f.manifest.EName
		}
		f.mu.Unlock()
		assert.Equal(t, expectedEName, request.URL.Query().Get("w3id"))
		json.NewEncoder(response).Encode(map[string]string{"uri": f.server.URL})
	case request.Method == http.MethodPost && request.URL.Path == "/platforms/certification":
		json.NewEncoder(response).Encode(map[string]any{"token": "platform-token", "expiresAt": time.Now().Add(time.Hour).UnixMilli()})
	case request.Method == http.MethodPost && request.URL.Path == "/platforms/migrations/inspect-token":
		assert.Equal(t, "Bearer registry-secret", request.Header.Get("Authorization"))
		json.NewEncoder(response).Encode(map[string]string{"fingerprint": tokenFingerprint("legacy-token")})
	case request.Method == http.MethodPost && request.URL.Path == "/platforms/migrations/activate":
		assert.Equal(t, "Bearer registry-secret", request.Header.Get("Authorization"))
		f.mu.Lock()
		f.activationCalls++
		f.mu.Unlock()
		json.NewEncoder(response).Encode(map[string]any{"token": "manager-token"})
	case request.Method == http.MethodPost && request.URL.Path == "/platforms/management/token":
		assert.Equal(t, "Bearer registry-secret", request.Header.Get("Authorization"))
		json.NewEncoder(response).Encode(map[string]string{"token": "manager-token"})
	case request.Method == http.MethodPost && request.URL.Path == "/graphql":
		f.mu.Lock()
		expectedEName := "@guided.w3id"
		if f.manifest.EName != nil {
			expectedEName = *f.manifest.EName
		}
		f.mu.Unlock()
		assert.Equal(t, expectedEName, request.Header.Get("X-ENAME"))
		var input map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		if strings.Contains(input["query"].(string), "ExistingPlatformProfiles") {
			assert.Equal(t, "Bearer legacy-token", request.Header.Get("Authorization"))
			f.mu.Lock()
			profile := append([]byte(nil), f.manifest.Migration.SourceProfile...)
			f.mu.Unlock()
			var parsed any
			require.NoError(t, json.Unmarshal(profile, &parsed))
			json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"metaEnvelopes": map[string]any{"edges": []any{
				map[string]any{"node": map[string]any{"id": "existing-profile", "parsed": parsed}},
			}}}})
			return
		}
		f.mu.Lock()
		migrated := f.manifest.Migration != nil && f.manifest.Migration.Status == "active"
		f.mu.Unlock()
		if migrated {
			assert.Equal(t, "Bearer manager-token", request.Header.Get("Authorization"))
		} else {
			assert.Equal(t, "Bearer platform-token", request.Header.Get("Authorization"))
		}
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

func addMigrationProof(t *testing.T, manifest *w3ds.PlatformManifest, status string) {
	t.Helper()
	ename := "@existing-platform"
	manifest.EName = &ename
	profile := json.RawMessage(`{"authorEnames":["@alice.w3id"],"description":"Initial description","displayName":"Guided Platform","domains":["productivity","work"],"ename":"@existing-platform","isDraft":false,"platformName":"guided-platform","url":"https://guided.example.com","version":"0.1.0"}`)
	digest := sha256.Sum256(profile)
	statement := w3ds.PlatformMigrationStatement{
		Type: w3ds.PlatformMigrationStatementType, SchemaVersion: 1, PlatformEName: ename,
		ProfileEnvelopeID: "existing-profile", ProfileDigest: hex.EncodeToString(digest[:]),
		TargetInstance: "https://gitw3.example.com", TargetOwner: "alice", TargetRepository: "platform",
		SignerEName: "@alice.w3id", IssuedAt: time.Now().UTC().Format(time.RFC3339), Nonce: "nonce",
	}
	payload, err := statement.SigningPayload()
	require.NoError(t, err)
	manifest.Migration = &w3ds.PlatformMigration{
		Status: status, ProfileEnvelopeID: "existing-profile", ProfileDigest: hex.EncodeToString(digest[:]),
		LegacyTokenFingerprint: tokenFingerprint("legacy-token"), SourceProfile: profile, SourceAuthorENames: []string{"@alice.w3id"},
		Proof: &w3ds.PlatformMigrationProof{Statement: statement, Payload: payload, Signature: "signature", PublicKey: "key", KeyBindingCertificate: "certificate", VerifiedAt: time.Now().UTC().Format(time.RFC3339)},
	}
}

func TestInspectPlatformMigrationFindsOneExactProfile(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/platforms/migrations/inspect-token":
			assert.Equal(t, "Bearer registry-secret", request.Header.Get("Authorization"))
			json.NewEncoder(response).Encode(map[string]string{"fingerprint": "accepted"})
		case "/resolve":
			json.NewEncoder(response).Encode(map[string]string{"uri": server.URL})
		case "/graphql":
			assert.Equal(t, "Bearer legacy-token", request.Header.Get("Authorization"))
			json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"metaEnvelopes": map[string]any{"edges": []any{
				map[string]any{"node": map[string]any{"id": "profile-1", "parsed": map[string]any{"ename": "@other", "platformName": "other"}}},
				map[string]any{"node": map[string]any{"id": "profile-2", "parsed": map[string]any{"ename": "@existing", "platformName": "existing", "authorEnames": []string{"@alice", "@bob"}}}},
			}}}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	config := testConfig(server.URL, "")
	config.RegistrySharedSecret = "registry-secret"
	processor := NewProcessor(config, openTestStore(t), server.Client())

	inspection, err := processor.InspectPlatformMigration(context.Background(), InspectPlatformMigrationRequest{EName: "@existing", Token: "legacy-token"})
	require.NoError(t, err)
	assert.Equal(t, "profile-2", inspection.ProfileEnvelopeID)
	assert.Equal(t, []string{"@alice", "@bob"}, inspection.AuthorENames)
	assert.Equal(t, tokenFingerprint("legacy-token"), inspection.TokenFingerprint)
}

func TestReconcileStagesMigrationWithoutPublishing(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	addMigrationProof(t, fake.manifest, "staged")
	store := openTestStore(t)
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, fake.server.Client())

	require.NoError(t, processor.Reconcile(context.Background(), job))
	staged, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusAwaitingCutover, staged.Status)
	assert.Equal(t, "@existing-platform", staged.EName)
	assert.Equal(t, "existing-profile", staged.EnvelopeID)
	assert.Empty(t, fake.published)
}

func TestActivateMigrationReusesOriginalProfileEnvelope(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	addMigrationProof(t, fake.manifest, "staged")
	store := openTestStore(t)
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, _ := store.Get(42)
	config := testConfig(fake.server.URL, "")
	config.RegistrySharedSecret = "registry-secret"
	processor := NewProcessor(config, store, fake.server.Client())
	require.NoError(t, processor.Reconcile(context.Background(), job))

	activated, err := processor.ActivatePlatformMigration(context.Background(), ActivatePlatformMigrationRequest{
		RepositoryID: 42, EName: "@existing-platform", ProfileEnvelopeID: "existing-profile",
		ProfileDigest: fake.manifest.Migration.ProfileDigest, Token: "legacy-token",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, activated.Status)
	assert.Equal(t, "existing-profile", activated.EnvelopeID)
	require.Len(t, fake.published, 1)
	assert.Equal(t, "@existing-platform", fake.published[0]["ename"])
}

func TestActivateMigrationResumesAfterManifestCommitFailure(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	addMigrationProof(t, fake.manifest, "staged")
	fake.manifestUpdateFailures = 1
	store := openTestStore(t)
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, _ := store.Get(42)
	config := testConfig(fake.server.URL, "")
	config.RegistrySharedSecret = "registry-secret"
	processor := NewProcessor(config, store, fake.server.Client())
	require.NoError(t, processor.Reconcile(context.Background(), job))

	_, err := processor.ActivatePlatformMigration(context.Background(), ActivatePlatformMigrationRequest{
		RepositoryID: 42, EName: "@existing-platform", ProfileEnvelopeID: "existing-profile",
		ProfileDigest: fake.manifest.Migration.ProfileDigest, Token: "legacy-token",
	})
	require.ErrorContains(t, err, "temporary Forgejo failure")
	interrupted, err := store.Get(42)
	require.NoError(t, err)
	assert.True(t, interrupted.MigrationActivated)
	assert.Empty(t, fake.published)

	require.NoError(t, processor.Reconcile(context.Background(), interrupted))
	resumed, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, resumed.Status)
	assert.Equal(t, "active", fake.manifest.Migration.Status)
	assert.Equal(t, 1, fake.activationCalls)
	require.Len(t, fake.published, 1)
}

func TestProcessorPreparesAndFinalizesDeployment(t *testing.T) {
	entropyPayload, err := json.Marshal(map[string]string{"entropy": "test-entropy-1234567890abcdef"})
	require.NoError(t, err)
	entropyToken := "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(entropyPayload) + ".signature"
	var server *httptest.Server
	provisioned := false
	createdDocuments := 0
	profilePublished := false
	certificationDecision := w3ds.AccreditationDecision{
		PlatformEName:   "@0699e093-2dd9-59cc-a416-7dc69623ebfd",
		PlatformVersion: "1.2.3", Decision: "granted", Level: "L2", CreatedAt: "2026-08-30T00:00:00Z",
	}
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
			if request.URL.Query().Get("w3id") == certificationDecision.PlatformEName {
				_ = json.NewEncoder(response).Encode(map[string]string{"uri": server.URL})
				return
			}
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
			case strings.Contains(query, "PlatformAccreditations"):
				_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"metaEnvelopes": map[string]any{
					"edges":    []any{map[string]any{"node": map[string]any{"parsed": certificationDecision}}},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
				}}})
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
	require.NoError(t, store.Save(&Job{
		RepositoryID: 42, EName: certificationDecision.PlatformEName, Status: StatusPublished,
	}))
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
	stored.Status = DeploymentFailed
	stored.LastError = "temporary infrastructure failure"
	stored.NextAttempt = time.Now().Add(time.Hour)
	require.NoError(t, store.SaveDeployment(stored))
	require.NoError(t, processor.FinalizeDeployment(FinalizeDeploymentRequest{
		SignerEName: "@deployer", Signature: "wallet-signature", KeyBindingCertificate: "certificate",
	}, stored))
	stored, err = store.GetDeployment(job.ID)
	require.NoError(t, err)
	assert.Equal(t, DeploymentPublishing, stored.Status)
	assert.Empty(t, stored.LastError)
	assert.False(t, stored.NextAttempt.After(time.Now()))

	certificationDecision.Decision = "denied"
	err = processor.ReconcileDeployment(context.Background(), stored)
	require.ErrorIs(t, err, ErrDeploymentCertificationRequired)
	assert.False(t, provisioned, "a revoked certificate must stop publication before W3DS resources are created")
	certificationDecision.Decision = "granted"
	require.NoError(t, processor.ReconcileDeployment(context.Background(), stored))
	stored, err = store.GetDeployment(job.ID)
	require.NoError(t, err)
	assert.Equal(t, DeploymentCompleted, stored.Status)
	assert.Equal(t, "document-1", stored.DeploymentKeyDocumentID)
	assert.Equal(t, "document-2", stored.SoftwareVersionDocumentID)
	assert.True(t, provisioned)
	assert.Empty(t, stored.RegistryEntropy)
}

func TestProcessorRejectsUncertifiedDeploymentVersion(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, fake.server.Client())
	require.NoError(t, store.Save(&Job{RepositoryID: 42, EName: "@guided.w3id", Status: StatusPublished}))

	input := PrepareDeploymentRequest{
		ID: "uncertified", RepositoryID: 42, PlatformEName: "@guided.w3id",
		DeploymentName: "Production", Environment: "production", DeployerEName: "@deployer",
		Version: "1.2.3", ReleaseTag: "v1.2.3", CommitSHA: strings.Repeat("a", 40), PublicKey: "zKey",
	}
	_, err := processor.PrepareDeployment(context.Background(), input)
	require.ErrorIs(t, err, ErrDeploymentCertificationRequired)

	fake.mu.Lock()
	fake.accreditations = []w3ds.AccreditationDecision{
		{PlatformEName: "@guided.w3id", PlatformVersion: "1.2.3", Decision: "granted", CreatedAt: "2026-08-29T00:00:00Z"},
		{PlatformEName: "@guided.w3id", PlatformVersion: "1.2.3", Decision: "denied", CreatedAt: "2026-08-30T00:00:00Z"},
	}
	fake.mu.Unlock()
	_, err = processor.PrepareDeployment(context.Background(), input)
	require.ErrorIs(t, err, ErrDeploymentCertificationRequired, "the latest decision must override an older grant")
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

func TestProcessorProvisionsPlatformIdentityWithoutKey(t *testing.T) {
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
	assert.Equal(t, StatusPublished, job.Status)
	assert.NotEmpty(t, job.EName)
	assert.NotEmpty(t, job.RegistryEntropy)
	assert.NotEmpty(t, job.Namespace)
	assert.True(t, job.IdentityProvisioned)
	assert.Equal(t, "v0.1.0", job.ReleaseTag)
	assert.Equal(t, "0.1.0", job.ReleaseVersion)
	require.NotNil(t, fake.manifest.EName)
	assert.Equal(t, job.EName, *fake.manifest.EName)
	assert.Empty(t, fake.manifest.PublicKey)
	assert.Equal(t, []string{""}, fake.provisionedKeys)
	require.Len(t, fake.published, 1)

	addSubmissionProof(t, fake.manifest, "alice/platform", 42)
	fake.release = &platformRelease{TagName: "v0.1.1", Version: "0.1.1"}
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	job, err = store.Get(42)
	require.NoError(t, err)
	require.NoError(t, processor.Reconcile(context.Background(), job))
	job, err = store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, job.Status)
	assert.Equal(t, "v0.1.1", job.ReleaseTag)
	assert.Equal(t, "0.1.1", job.ReleaseVersion)
	assert.Equal(t, job.EName, *fake.manifest.EName)
	assert.Equal(t, "0.1.1", fake.manifest.Version)
	assert.False(t, fake.manifest.InSubmission)
	assert.Empty(t, fake.manifest.SubmissionVersion)
	assert.Nil(t, fake.manifest.SubmissionProof)
	assert.NotEmpty(t, fake.manifest.SubmissionHistory)
	assert.Equal(t, 1, fake.provisionCalls)

	fake.accreditations = []w3ds.AccreditationDecision{{
		PlatformEName: job.EName, PlatformVersion: "0.1.1", Decision: "granted", CreatedAt: "2026-08-30T00:00:00Z",
	}}
	deployment, err := processor.PrepareDeployment(context.Background(), PrepareDeploymentRequest{
		ID: "first-deployment", RepositoryID: 42, PlatformEName: job.EName,
		DeploymentName: "Production", Environment: "production", DeployerEName: "@deployer",
		Version: "0.1.1", ReleaseTag: "v0.1.1", CommitSHA: strings.Repeat("a", 40), PublicKey: "z0123456789",
	})
	require.NoError(t, err)
	assert.False(t, deployment.ActivatesPlatform)
	assert.Equal(t, 1, fake.provisionCalls)
	assert.Empty(t, fake.manifest.PublicKey)
}

func TestProcessorResumesPreviouslyReservedIdentityWithoutKey(t *testing.T) {
	fake := newFakePlatformInfrastructure(t)
	fake.manifest.PublicKey = ""
	store := openTestStore(t)
	processor := NewProcessor(testConfig(fake.server.URL, ""), store, fake.server.Client())
	identity, err := processor.w3ds.prepareIdentity(context.Background())
	require.NoError(t, err)
	require.NoError(t, store.Save(&Job{
		RepositoryID: 42, FullName: "alice/platform", DefaultBranch: "main", TargetSHA: "commit-1", Status: StatusAwaitingDeploy,
		EName: identity.EName, RegistryEntropy: identity.RegistryEntropy, Namespace: identity.Namespace,
	}))

	ready, err := store.Ready(time.Now().UTC(), 10)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.NoError(t, processor.Reconcile(context.Background(), ready[0]))

	job, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, job.Status)
	assert.True(t, job.IdentityProvisioned)
	assert.Empty(t, fake.manifest.PublicKey)
	assert.Equal(t, []string{""}, fake.provisionedKeys)
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
