// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordDokploy answers every procedure with {} (or respond[procedure]) and
// records the JSON bodies it was sent.
func recordDokploy(t *testing.T, respond map[string]string) (*dokployHTTPClient, map[string]map[string]any) {
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := strings.TrimPrefix(r.URL.Path, "/api/")
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			require.NoError(t, json.Unmarshal(data, &body))
			mu.Lock()
			bodies[procedure] = body
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		if answer, ok := respond[procedure]; ok {
			_, _ = w.Write([]byte(answer))
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)
	return &dokployHTTPClient{baseURL: server.URL, http: server.Client()}, bodies
}

func TestDokployDeployImagePullCredentials(t *testing.T) {
	defer test.MockVariableValue(&setting.Hosting.RegistryHost, "git.example.com")()
	defer test.MockVariableValue(&setting.Hosting.RegistryPullUser, "deployments-pull")()
	defer test.MockVariableValue(&setting.Hosting.RegistryPullToken, "pull-token")()
	client, bodies := recordDokploy(t, nil)

	require.NoError(t, client.DeployImage(t.Context(), "app1", "git.example.com/deployments/x@sha256:abc", "GitW3 v1 (#7)"))
	provider := bodies["application.saveDockerProvider"]
	assert.Equal(t, "deployments-pull", provider["username"])
	assert.Equal(t, "pull-token", provider["password"])
	assert.Equal(t, "git.example.com", provider["registryUrl"])
	assert.Equal(t, "GitW3 v1 (#7)", bodies["application.deploy"]["title"])
}

func TestDokployDeployImageWithoutPullToken(t *testing.T) {
	defer test.MockVariableValue(&setting.Hosting.RegistryPullToken, "")()
	client, bodies := recordDokploy(t, nil)

	require.NoError(t, client.DeployImage(t.Context(), "app1", "registry/x@sha256:abc", "t"))
	provider := bodies["application.saveDockerProvider"]
	for _, key := range []string{"username", "password", "registryUrl"} {
		value, present := provider[key]
		assert.True(t, present, "Dokploy rejects a missing %s", key)
		assert.Nil(t, value)
	}
}

func TestDokployUpdateAppClearsRegistry(t *testing.T) {
	client, bodies := recordDokploy(t, nil)

	require.NoError(t, client.UpdateApp(t.Context(), "app1", AppSpec{Replicas: 1}))
	registry, present := bodies["application.update"]["registryId"]
	assert.True(t, present, "a registry set by an earlier version must be cleared")
	assert.Nil(t, registry, "a Dokploy registry makes Dokploy push the image, which fails for digests")
}

func TestDokployAppStateLastDeploy(t *testing.T) {
	client, _ := recordDokploy(t, map[string]string{
		"application.one": `{"appName":"app-x","applicationStatus":"error","deployments":[` +
			`{"title":"GitW3 v1.0.1 (#12)","status":"error"},{"title":"GitW3 v1.0.0 (#11)","status":"done"}]}`,
	})

	state, err := client.AppState(t.Context(), "app1")
	require.NoError(t, err)
	assert.Equal(t, "app-x", state.AppName)
	assert.True(t, state.DeployFailed(12))
	assert.False(t, state.DeployFailed(11))
	assert.False(t, state.DeployFailed(1), "#1 must not match the suffix of #11 or #12")
}
