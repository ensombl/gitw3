// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signedWebhookRequest(t *testing.T, secret, event string, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", bytes.NewReader(body))
	request.Header.Set("X-Forgejo-Event", event)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	request.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}

func TestServerSchedulesDefaultBranchWebhooks(t *testing.T) {
	store := openTestStore(t)
	config := testConfig("https://gitw3.example.com", "")
	server := NewServer(config, store)
	payload := map[string]any{
		"ref": "refs/heads/main", "after": "commit-sha",
		"repository": map[string]any{"id": 42, "full_name": "alice/platform", "default_branch": "main"},
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, signedWebhookRequest(t, config.WebhookSecret, "push", payload))
	assert.Equal(t, http.StatusAccepted, response.Code)
	job, err := store.Get(42)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "commit-sha", job.TargetSHA)

	payload["ref"] = "refs/heads/feature"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, signedWebhookRequest(t, config.WebhookSecret, "push", payload))
	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestServerRejectsInvalidSignature(t *testing.T) {
	store := openTestStore(t)
	server := NewServer(testConfig("https://gitw3.example.com", ""), store)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", bytes.NewBufferString("{}"))
	request.Header.Set("X-Forgejo-Signature", "bad")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestServerProtectsPublicationStatus(t *testing.T) {
	store := openTestStore(t)
	config := testConfig("https://gitw3.example.com", "")
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit", false))
	server := NewServer(config, store)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status/42", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	request = httptest.NewRequest(http.MethodGet, "/api/v1/status/42", nil)
	request.Header.Set("Authorization", "Bearer "+config.InternalToken)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), string(StatusIdentityPending))
}
