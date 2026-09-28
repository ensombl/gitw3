// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig(t *testing.T) {
	config, err := ParseConfig([]byte(`version: 1
targets:
  - name: web
    branch: main
    kind: dockerfile
    dockerfile: ./docker/Dockerfile
    port: 8080
    replicas: 2
    domain: Web.Example.com.
    healthcheck:
      path: /healthz
      interval: 10s
      timeout: 3s
    resources:
      cpu: "0.5"
      memory: 512M
    auto_deploy: true
  - name: worker-stack
    kind: compose
    compose: deploy/compose.yml
    service: api
    port: 4000
`))
	require.NoError(t, err)
	web := config.Target("web")
	require.NotNil(t, web)
	assert.Equal(t, "docker/Dockerfile", web.Dockerfile)
	assert.Equal(t, ".", web.Context)
	assert.Equal(t, "web.example.com", web.Domain)
	assert.True(t, web.AutoDeploy)
	cpus, _ := web.Resources.NanoCPUs()
	assert.EqualValues(t, 500_000_000, cpus)
	memory, _ := web.Resources.MemoryBytes()
	assert.EqualValues(t, 512<<20, memory)

	stack := config.Target("worker-stack")
	require.NotNil(t, stack)
	assert.Equal(t, "deploy", stack.Context)
	assert.Equal(t, 1, stack.Replicas)
}

func TestParseConfigRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":  "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    privileged: true\n",
		"bad version":    "version: 2\ntargets:\n  - name: web\n    kind: dockerfile\n",
		"no targets":     "version: 1\ntargets: []\n",
		"bad name":       "version: 1\ntargets:\n  - name: Web_App\n    kind: dockerfile\n",
		"path escape":    "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    context: ../other\n",
		"absolute path":  "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    dockerfile: /etc/passwd\n",
		"duplicate":      "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n  - name: web\n    kind: dockerfile\n",
		"too many reps":  "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    replicas: 99\n",
		"bad kind":       "version: 1\ntargets:\n  - name: web\n    kind: helm\n",
		"compose port":   "version: 1\ntargets:\n  - name: web\n    kind: compose\n    compose: c.yml\n    port: 80\n",
		"bad memory":     "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    resources:\n      memory: lots\n",
		"bad healthpath": "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    healthcheck:\n      path: healthz\n",
		"bad domain":     "version: 1\ntargets:\n  - name: web\n    kind: dockerfile\n    domain: localhost\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(content))
			assert.Error(t, err)
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig([]byte("FROM node:22\nWORKDIR /app\nexpose 8080/tcp\n"))
	require.NoError(t, config.Validate())
	assert.Equal(t, 8080, config.Targets[0].Port)
	assert.Equal(t, DefaultPort, DefaultConfig([]byte("FROM scratch\n")).Targets[0].Port)
}

func TestParseCompose(t *testing.T) {
	file, builds, err := ParseCompose([]byte(`services:
  api:
    build:
      context: ./api
      dockerfile: Dockerfile.prod
    environment:
      MODE: prod
  worker:
    build: ../worker
  cache:
    image: redis:7
`), "deploy")
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "cache", "worker"}, file.Services())
	require.Len(t, builds, 2)
	assert.Equal(t, ComposeBuild{Service: "api", Context: "deploy/api", Dockerfile: "Dockerfile.prod"}, builds[0])
	assert.Equal(t, ComposeBuild{Service: "worker", Context: "worker", Dockerfile: "Dockerfile"}, builds[1])

	digest := "sha256:" + strings.Repeat("a", 64)
	_, err = file.PinImages(map[string]string{"api": "git.example.com/deployments/x-api@" + digest})
	require.Error(t, err, "worker has no pinned image")

	file.ApplyPlacement([]string{"node.role==worker"})
	rendered, err := file.PinImages(map[string]string{
		"api":    "git.example.com/deployments/x-api@" + digest,
		"worker": "git.example.com/deployments/x-worker@" + digest,
	})
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), "build")
	assert.Contains(t, string(rendered), "x-worker@"+digest)
	assert.Contains(t, string(rendered), "redis:7")
	assert.Equal(t, 3, strings.Count(string(rendered), "node.role==worker"))
}

func TestParseComposeRejects(t *testing.T) {
	cases := map[string]string{
		"privileged":    "services:\n  a:\n    image: x\n    privileged: true\n",
		"host network":  "services:\n  a:\n    image: x\n    network_mode: host\n",
		"volumes":       "services:\n  a:\n    image: x\n    volumes: [\"/:/host\"]\n",
		"top volumes":   "services:\n  a:\n    image: x\nvolumes:\n  data: {}\n",
		"ports":         "services:\n  a:\n    image: x\n    ports: [\"80:80\"]\n",
		"global":        "services:\n  a:\n    image: x\n    deploy:\n      mode: global\n",
		"no image":      "services:\n  a:\n    environment: {}\n",
		"remote build":  "services:\n  a:\n    build: https://github.com/x/y.git\n",
		"escape build":  "services:\n  a:\n    build: ../../..\n",
		"build secrets": "services:\n  a:\n    build:\n      context: .\n      secrets: [x]\n",
		"no services":   "version: \"3\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseCompose([]byte(content), "deploy")
			assert.Error(t, err)
		})
	}
}

func TestRandomName(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z]+-[a-z]+$`)
	for range 50 {
		assert.Regexp(t, pattern, RandomName())
	}
	assert.Equal(t, "admiring-lovelace.apps.example.com", PoolFQDN("admiring-lovelace", "apps.example.com"))
}

func TestCallbackSignature(t *testing.T) {
	body := []byte(`{"job_id":1}`)
	signature := SignCallback("secret", body)
	assert.True(t, VerifyCallback("secret", body, signature))
	assert.True(t, VerifyCallback("secret", body, "sha256="+signature))
	assert.False(t, VerifyCallback("other", body, signature))
	assert.False(t, VerifyCallback("secret", []byte(`{"job_id":2}`), signature))
	assert.False(t, VerifyCallback("", body, SignCallback("", body)))
	assert.Len(t, NewNonce(), 48)
}

func TestSourceURL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	query := SignSourceURL("secret", 7, now, time.Minute)
	var exp, sig string
	for part := range strings.SplitSeq(query, "&") {
		key, value, _ := strings.Cut(part, "=")
		if key == "exp" {
			exp = value
		} else {
			sig = value
		}
	}
	assert.True(t, VerifySourceURL("secret", 7, exp, sig, now))
	assert.False(t, VerifySourceURL("secret", 8, exp, sig, now))
	assert.False(t, VerifySourceURL("secret", 7, exp, sig, now.Add(2*time.Minute)))
	assert.False(t, VerifySourceURL("secret", 7, "nope", sig, now))
}

func TestImageName(t *testing.T) {
	assert.Equal(t, "alice-my-shop-web", ImageName("Alice", "My.Shop", "web"))
	assert.Equal(t, "alice-shop-stack-api", ImageName("alice", "shop", "stack", "api"))
	repository := ImageRepository("git.example.com", "Deployments", "alice-shop-web")
	assert.Equal(t, "git.example.com/deployments/alice-shop-web", repository)
	digest := "sha256:" + strings.Repeat("b", 64)
	assert.True(t, IsDigestReference(DigestReference(repository, digest)))
	assert.False(t, IsDigestReference(repository+":latest"))
	assert.True(t, IsDigest(digest))
	assert.False(t, IsDigest("sha256:xyz"))
}
