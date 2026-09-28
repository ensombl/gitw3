// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting_test

import (
	"testing"

	"forgejo.org/models/db"
	hosting_model "forgejo.org/models/hosting"
	"forgejo.org/models/unittest"
	hosting_module "forgejo.org/modules/hosting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTarget(t *testing.T, name string) *hosting_model.Target {
	t.Helper()
	target := &hosting_model.Target{RepoID: 1, Name: name}
	require.NoError(t, target.SetSpec(&hosting_module.Target{Name: name, Kind: hosting_module.KindDockerfile, Replicas: 2, Port: 8080}))
	require.NoError(t, hosting_model.CreateTarget(db.DefaultContext, target))
	return target
}

func TestTargetSpecAndKey(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	target := createTarget(t, "web")
	target.SetPrivateKey("-----BEGIN PRIVATE KEY-----")
	require.NoError(t, hosting_model.UpdateTargetCols(ctx, target, "private_key_enc"))

	stored, err := hosting_model.GetTargetByRepoAndName(ctx, 1, "web")
	require.NoError(t, err)
	spec, err := stored.Spec()
	require.NoError(t, err)
	assert.Equal(t, 8080, spec.Port)
	assert.Equal(t, 2, stored.Replicas)
	key, err := stored.PrivateKey()
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN PRIVATE KEY-----", key)

	_, err = hosting_model.GetTargetByRepoAndName(ctx, 1, "missing")
	assert.ErrorIs(t, err, hosting_model.ErrTargetNotExist)
}

func TestDeploymentTransitions(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	target := createTarget(t, "api")
	deployment := &hosting_model.Deployment{TargetID: target.ID, RepoID: 1, ActorID: 1, ReleaseID: 1, TagName: "v1.0.0", CommitSHA: "abc", Trigger: hosting_model.TriggerManual}
	require.NoError(t, hosting_model.CreateDeployment(ctx, deployment))
	assert.Equal(t, hosting_model.StatusQueued, deployment.Status)

	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusLive)
	require.NoError(t, err)
	assert.False(t, ok, "queued cannot go straight to live")

	stale := *deployment
	ok, err = hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilding)
	require.NoError(t, err)
	assert.True(t, ok)
	deployment.ImageDigest = "sha256:x"
	ok, err = hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilt, "image_digest")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = hosting_model.Transition(ctx, &stale, hosting_model.StatusBuilding)
	require.NoError(t, err)
	assert.False(t, ok, "stale actor loses the race")

	require.NoError(t, deployment.SetEnvSnapshot(map[string]string{"A": "1"}))
	require.NoError(t, hosting_model.UpdateDeploymentCols(ctx, deployment, "env_snapshot_enc"))
	stored, err := hosting_model.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	assert.Equal(t, "sha256:x", stored.ImageDigest)
	env, err := stored.EnvSnapshot()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "1"}, env)

	pending, err := hosting_model.ListPendingDeployments(ctx, target.ID, 0)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.True(t, hosting_model.CanTransition(hosting_model.StatusDeploying, hosting_model.StatusRolledBack))
	assert.True(t, hosting_model.StatusCancelled.IsFinal())
}

func TestBuildJobNonce(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	job := &hosting_model.BuildJob{DeploymentID: 99, Nonce: "n1"}
	require.NoError(t, hosting_model.CreateBuildJob(ctx, job))
	ok, err := hosting_model.ConsumeNonce(ctx, job.ID, "wrong")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = hosting_model.ConsumeNonce(ctx, job.ID, "n1")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = hosting_model.ConsumeNonce(ctx, job.ID, "n1")
	require.NoError(t, err)
	assert.False(t, ok, "nonce is single use")
}

func TestEnvVars(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	target := createTarget(t, "env")
	require.NoError(t, hosting_model.SetEnvVar(ctx, target.ID, "API_KEY", "one", true))
	require.NoError(t, hosting_model.SetEnvVar(ctx, target.ID, "API_KEY", "two", true))
	require.NoError(t, hosting_model.SetEnvVar(ctx, target.ID, "MODE", "prod", false))
	require.Error(t, hosting_model.SetEnvVar(ctx, target.ID, "GITW3_URL", "x", false))
	require.Error(t, hosting_model.SetEnvVar(ctx, target.ID, "1BAD", "x", false))

	env, err := hosting_model.EnvMap(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"API_KEY": "two", "MODE": "prod"}, env)

	require.NoError(t, hosting_model.ReplaceEnv(ctx, target.ID, map[string]string{"API_KEY": "old"}))
	vars, err := hosting_model.ListEnvVars(ctx, target.ID)
	require.NoError(t, err)
	require.Len(t, vars, 1)
	assert.True(t, vars[0].IsSecret)
	value, err := vars[0].Value()
	require.NoError(t, err)
	assert.Equal(t, "old", value)
}

func TestPoolDomainClaim(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	_, err := hosting_model.ClaimPoolDomain(ctx, 1)
	require.ErrorIs(t, err, hosting_model.ErrPoolEmpty)

	for _, fqdn := range []string{"a-b.apps.test", "c-d.apps.test"} {
		require.NoError(t, hosting_model.CreateDomain(ctx, &hosting_model.Domain{FQDN: fqdn, Kind: hosting_model.DomainPool, Status: hosting_model.DomainAvailable}))
	}
	require.ErrorIs(t, hosting_model.CreateDomain(ctx, &hosting_model.Domain{FQDN: "a-b.apps.test", Kind: hosting_model.DomainCustom}), hosting_model.ErrDomainTaken)

	count, err := hosting_model.CountAvailablePoolDomains(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	first, err := hosting_model.ClaimPoolDomain(ctx, 7)
	require.NoError(t, err)
	second, err := hosting_model.ClaimPoolDomain(ctx, 8)
	require.NoError(t, err)
	assert.NotEqual(t, first.FQDN, second.FQDN)
	assert.True(t, first.IsRoutable())
	_, err = hosting_model.ClaimPoolDomain(ctx, 9)
	require.ErrorIs(t, err, hosting_model.ErrPoolEmpty)
	domains, err := hosting_model.ListTargetDomains(ctx, 7)
	require.NoError(t, err)
	assert.Len(t, domains, 1)
}
