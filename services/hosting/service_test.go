// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forgejo.org/models/db"
	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	system_model "forgejo.org/models/system"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDigest = "sha256:" + strings.Repeat("c", 64)

func webSpec() *hosting_module.Target {
	spec := &hosting_module.Target{Name: "web", Kind: hosting_module.KindDockerfile, Port: 8080, Replicas: 2}
	if err := spec.Validate(); err != nil {
		panic(err)
	}
	return spec
}

func newTarget(t *testing.T) (*repo_model.Repository, *user_model.User, *hosting_model.Target) {
	t.Helper()
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	target, err := EnsureTarget(db.DefaultContext, repo, webSpec(), user, "")
	require.NoError(t, err)
	return repo, user, target
}

func queueDeployment(t *testing.T, target *hosting_model.Target, user *user_model.User, tag string) (*hosting_model.Deployment, *hosting_model.BuildJob) {
	t.Helper()
	ctx := db.DefaultContext
	deployment := &hosting_model.Deployment{
		TargetID: target.ID, RepoID: target.RepoID, ActorID: user.ID, ReleaseID: 1, TagName: tag,
		CommitSHA: strings.Repeat("a", 40), Trigger: hosting_model.TriggerManual,
	}
	require.NoError(t, hosting_model.CreateDeployment(ctx, deployment))
	job := &hosting_model.BuildJob{DeploymentID: deployment.ID, Nonce: hosting_module.NewNonce(), RunID: deployment.ID + 500}
	require.NoError(t, hosting_model.CreateBuildJob(ctx, job))
	return deployment, job
}

func signedCallback(t *testing.T, callback BuildCallback) ([]byte, string) {
	t.Helper()
	body, err := json.Marshal(callback)
	require.NoError(t, err)
	return body, hosting_module.SignCallback(setting.Hosting.CallbackSecret, body)
}

func reload(t *testing.T, id int64) *hosting_model.Deployment {
	t.Helper()
	deployment, err := hosting_model.GetDeployment(db.DefaultContext, id)
	require.NoError(t, err)
	return deployment
}

func TestEnsureTargetProvisions(t *testing.T) {
	f := setupFakes(t)
	_, _, target := newTarget(t)
	assert.NotEmpty(t, target.DokployAppID)
	assert.True(t, strings.HasPrefix(target.PublicKey, "z"))
	key, err := target.PrivateKey()
	require.NoError(t, err)
	assert.Contains(t, key, "privateKeyPkcs8")
	assert.Empty(t, f.dokploy.mounts, "no file mounts: they cannot reach worker nodes")
	assert.Equal(t, 2, f.dokploy.apps[target.DokployAppID].Replicas)

	pool := PoolDomain(db.DefaultContext, target)
	require.NotNil(t, pool)
	assert.True(t, strings.HasSuffix(pool.FQDN, ".apps.example.com"))
	assert.Equal(t, []string{pool.FQDN}, f.dokploy.hosts())
	assert.Equal(t, "https://"+pool.FQDN, PublicURL(db.DefaultContext, target))
}

func TestBuildCallbackDeploysDigest(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	require.NoError(t, hosting_model.SetEnvVar(ctx, target.ID, "API_KEY", "secret", true))
	deployment, job := queueDeployment(t, target, user, "v1.0.0")
	f.registry.images["user2-repo1-web@"+testDigest] = true

	body, signature := signedCallback(t, BuildCallback{JobID: job.ID, Nonce: job.Nonce, Status: "success", Digests: map[string]string{"": testDigest}})
	require.ErrorIs(t, HandleBuildCallback(ctx, body, "sha256=deadbeef"), ErrCallbackUnauthorized)
	require.NoError(t, HandleBuildCallback(ctx, body, signature))
	require.ErrorIs(t, HandleBuildCallback(ctx, body, signature), ErrCallbackReplayed)

	deployment = reload(t, deployment.ID)
	assert.Equal(t, hosting_model.StatusDeploying, deployment.Status)
	assert.Equal(t, testDigest, deployment.ImageDigest)
	assert.Equal(t, "git.example.com/deployments/user2-repo1-web@"+testDigest, f.dokploy.images[target.DokployAppID])
	env := f.dokploy.env[target.DokployAppID]
	assert.Equal(t, "secret", env["API_KEY"])
	assert.Equal(t, "8080", env["PORT"])
	assert.Equal(t, "v1.0.0", env["GITW3_RELEASE"])
	stored, err := hosting_model.GetTarget(ctx, target.ID)
	require.NoError(t, err)
	key, err := stored.PrivateKey()
	require.NoError(t, err)
	assert.Equal(t, key, env[deploymentKeyJSONEnvVar], "the deployment key reaches the app")
	snapshot, err := deployment.EnvSnapshot()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"API_KEY": "secret"}, snapshot)

	// The swarm still runs the old image: not live yet.
	f.swarm.services = []ServiceStatus{{Name: "svc", Image: "old@sha256:" + strings.Repeat("0", 64), Desired: 2, Running: 2}}
	require.NoError(t, SyncDeployments(ctx))
	assert.Equal(t, hosting_model.StatusDeploying, reload(t, deployment.ID).Status)

	f.swarm.services = []ServiceStatus{{Name: "svc", Image: "x@" + testDigest, UpdateState: "completed", Desired: 2, Running: 2}}
	require.NoError(t, SyncDeployments(ctx))
	assert.Equal(t, hosting_model.StatusLive, reload(t, deployment.ID).Status)
	target, err = hosting_model.GetTarget(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, deployment.ID, target.LiveDeploymentID)
}

func TestBuildCallbackRejectsUnknownDigest(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	deployment, job := queueDeployment(t, target, user, "v1.0.0")
	// The digest exists, but under another app's image name.
	f.registry.images["someone-else-web@"+testDigest] = true
	body, signature := signedCallback(t, BuildCallback{JobID: job.ID, Nonce: job.Nonce, Status: "success", Digests: map[string]string{"": testDigest}})
	require.ErrorIs(t, HandleBuildCallback(ctx, body, signature), ErrCallbackRejected)
	assert.Equal(t, hosting_model.StatusBuildFailed, reload(t, deployment.ID).Status)
	assert.Empty(t, f.dokploy.images)
}

func TestBuildCallbackScanPolicy(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	f.registry.images["user2-repo1-web@"+testDigest] = true
	scan := &ScanResult{Critical: 1, High: 3}

	blocked, job := queueDeployment(t, target, user, "v1.0.0")
	body, signature := signedCallback(t, BuildCallback{JobID: job.ID, Nonce: job.Nonce, Status: "success", Digests: map[string]string{"": testDigest}, Scan: scan})
	require.NoError(t, HandleBuildCallback(ctx, body, signature))
	blocked = reload(t, blocked.ID)
	assert.Equal(t, hosting_model.StatusBuildFailed, blocked.Status)
	assert.Contains(t, blocked.Error, "1 critical")

	defer test.MockVariableValue(&setting.Hosting.Scan.Policy, setting.HostingScanPolicyWarn)()
	warned, job := queueDeployment(t, target, user, "v1.0.1")
	body, signature = signedCallback(t, BuildCallback{JobID: job.ID, Nonce: job.Nonce, Status: "success", Digests: map[string]string{"": testDigest}, Scan: scan})
	require.NoError(t, HandleBuildCallback(ctx, body, signature))
	warned = reload(t, warned.ID)
	assert.Equal(t, hosting_model.StatusDeploying, warned.Status)
	assert.Contains(t, warned.Warning, "1 critical")
}

func TestBuildFailureAndSupersede(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	older, olderJob := queueDeployment(t, target, user, "v1.0.0")
	olderJob.Status = hosting_model.BuildRunning
	require.NoError(t, hosting_model.UpdateBuildJobCols(ctx, olderJob, "status"))
	newer, _ := queueDeployment(t, target, user, "v1.1.0")
	supersedePending(ctx, target, newer.ID)
	assert.Equal(t, hosting_model.StatusCancelled, reload(t, older.ID).Status)
	assert.Equal(t, []int64{olderJob.RunID}, f.builder.cancelled)
	assert.Equal(t, hosting_model.StatusQueued, reload(t, newer.ID).Status)

	job, err := hosting_model.GetBuildJobByDeployment(ctx, newer.ID)
	require.NoError(t, err)
	f.builder.states[job.RunID] = BuildStateFailed
	require.NoError(t, SyncDeployments(ctx))
	assert.Equal(t, hosting_model.StatusBuildFailed, reload(t, newer.ID).Status)
}

func TestRollbackRestoresImageAndEnv(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	previous := &hosting_model.Deployment{
		TargetID: target.ID, RepoID: target.RepoID, ActorID: user.ID, ReleaseID: 1, TagName: "v0.9.0",
		CommitSHA: strings.Repeat("b", 40), ImageDigest: testDigest, Trigger: hosting_model.TriggerManual,
		Status: hosting_model.StatusSuperseded,
	}
	require.NoError(t, hosting_model.CreateDeployment(ctx, previous))
	require.NoError(t, previous.SetEnvSnapshot(map[string]string{"MODE": "old"}))
	require.NoError(t, hosting_model.UpdateDeploymentCols(ctx, previous, "env_snapshot_enc"))
	require.NoError(t, hosting_model.SetEnvVar(ctx, target.ID, "MODE", "new", false))

	_, err := Rollback(ctx, target, previous, user)
	require.Error(t, err, "image no longer in the registry")

	f.registry.images["user2-repo1-web@"+testDigest] = true
	rollback, err := Rollback(ctx, target, previous, user)
	require.NoError(t, err)
	rollback = reload(t, rollback.ID)
	assert.Equal(t, hosting_model.StatusDeploying, rollback.Status)
	assert.Equal(t, previous.ID, rollback.RollbackOf)
	assert.Equal(t, "old", f.dokploy.env[target.DokployAppID]["MODE"])
	env, err := hosting_model.EnvMap(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "old", env["MODE"])
}

func TestRolloutRolledBack(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	deployment, job := queueDeployment(t, target, user, "v2.0.0")
	f.registry.images["user2-repo1-web@"+testDigest] = true
	body, signature := signedCallback(t, BuildCallback{JobID: job.ID, Nonce: job.Nonce, Status: "success", Digests: map[string]string{"": testDigest}})
	require.NoError(t, HandleBuildCallback(ctx, body, signature))
	f.swarm.diagnosis = "svc: failed (task: non-zero exit (1)), exit code 1\nLast log lines:\nError: Cannot find module 'express'"
	f.swarm.services = []ServiceStatus{{Name: "svc", Image: "x@" + testDigest, UpdateState: "rollback_completed", Desired: 1, Running: 1}}
	require.NoError(t, SyncDeployments(ctx))
	assert.Equal(t, hosting_model.StatusRolledBack, reload(t, deployment.ID).Status)
	assert.Contains(t, reload(t, deployment.ID).Error, "Cannot find module", "the crash reason reaches the user")
}

func TestW3DSVersionGate(t *testing.T) {
	f := setupFakes(t)
	defer test.MockVariableValue(&setting.Hosting.RequireW3DS, true)()
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	deployment, _ := queueDeployment(t, target, user, "v1.0.0")
	_, _ = hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilding)
	deployment.ImageDigest = testDigest
	_, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilt, "image_digest")
	require.NoError(t, err)

	goLive(ctx, deployment)
	assert.Equal(t, hosting_model.StatusAwaitingSignature, reload(t, deployment.ID).Status, "no wallet signature yet")
	assert.Empty(t, f.dokploy.images)
}

func TestDomainPoolAndCustomDomains(t *testing.T) {
	f := setupFakes(t)
	ctx := context.Background()
	require.NoError(t, RefillDomainPool(ctx))
	available, err := hosting_model.CountAvailablePoolDomains(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 3, available)
	assert.Len(t, f.dns.records, 3)

	_, user, target := newTarget(t)
	available, err = hosting_model.CountAvailablePoolDomains(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, available, "the target took a ready domain")
	require.NoError(t, RefillDomainPool(ctx))
	available, _ = hosting_model.CountAvailablePoolDomains(ctx)
	assert.EqualValues(t, 3, available)

	_, err = AddCustomDomain(ctx, target, "evil.apps.example.com", user.ID)
	require.Error(t, err, "pool names cannot be claimed")
	domain, err := AddCustomDomain(ctx, target, "Shop.Example.org", user.ID)
	require.NoError(t, err)
	assert.Equal(t, "shop.example.org", domain.FQDN)

	pool := PoolDomain(ctx, target)
	resolver := &fakeResolver{cnames: map[string]string{}, hosts: map[string][]string{}}
	defer test.MockVariableValue[DNSResolver](&Resolver, resolver)()
	require.Error(t, VerifyCustomDomain(ctx, target, domain, user.ID))
	resolver.cnames["shop.example.org"] = pool.FQDN + "."
	require.NoError(t, VerifyCustomDomain(ctx, target, domain, user.ID))
	assert.Contains(t, f.dokploy.hosts(), "shop.example.org")
	assert.Equal(t, "https://shop.example.org", PublicURL(ctx, target))

	require.NoError(t, RemoveDomain(ctx, target, domain, user.ID))
	assert.NotContains(t, f.dokploy.hosts(), "shop.example.org")
	require.Error(t, RemoveDomain(ctx, target, pool, user.ID), "pool domains stay attached")

	recordsBefore := len(f.dns.records)
	require.NoError(t, DeleteTarget(ctx, target, user))
	assert.Len(t, f.dns.records, recordsBefore-1)
	assert.Empty(t, f.dokploy.apps)
}

type fakeResolver struct {
	cnames map[string]string
	hosts  map[string][]string
}

func (f *fakeResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if cname, ok := f.cnames[host]; ok {
		return cname, nil
	}
	return host + ".", nil
}

func (f *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return f.hosts[host], nil
}

func TestEvaluateRollout(t *testing.T) {
	digest := map[string]string{"": testDigest}
	healthy := ServiceStatus{Image: "x@" + testDigest, UpdateState: "completed", Desired: 2, Running: 2}
	assert.Equal(t, RolloutHealthy, EvaluateRollout([]ServiceStatus{healthy}, digest))
	assert.Equal(t, RolloutUnknown, EvaluateRollout(nil, digest))
	partial := healthy
	partial.Running = 1
	assert.Equal(t, RolloutInProgress, EvaluateRollout([]ServiceStatus{partial}, digest))
	updating := healthy
	updating.UpdateState = "updating"
	assert.Equal(t, RolloutInProgress, EvaluateRollout([]ServiceStatus{updating}, digest))
	paused := healthy
	paused.UpdateState = "paused"
	assert.Equal(t, RolloutFailed, EvaluateRollout([]ServiceStatus{paused}, digest))

	stack := map[string]string{"api": testDigest}
	api := ServiceStatus{Name: "shop_api", Image: "x@" + testDigest, Desired: 1, Running: 1}
	cache := ServiceStatus{Name: "shop_cache", Image: "redis:7", Desired: 1, Running: 1}
	assert.Equal(t, RolloutHealthy, EvaluateRollout([]ServiceStatus{api, cache}, stack))
	api.Image = "x@sha256:" + strings.Repeat("0", 64)
	assert.Equal(t, RolloutInProgress, EvaluateRollout([]ServiceStatus{api, cache}, stack))
}

func TestFormatEnv(t *testing.T) {
	assert.Equal(t, "A=\"1\"\nB=\"say \\\"hi\\\"\\n\\$HOME\"\n", FormatEnv(map[string]string{"B": "say \"hi\"\n$HOME", "A": "1"}))
}

func TestKeptDigests(t *testing.T) {
	setupFakes(t)
	defer test.MockVariableValue(&setting.Hosting.KeepDigests, 2)()
	ctx := db.DefaultContext
	_, user, target := newTarget(t)
	digest := func(n int) string { return "sha256:" + strings.Repeat(string(rune('a'+n)), 64) }
	create := func(n int, status hosting_model.Status) *hosting_model.Deployment {
		deployment := &hosting_model.Deployment{
			TargetID: target.ID, RepoID: target.RepoID, ActorID: user.ID, TagName: "v1.0." + string(rune('0'+n)),
			CommitSHA: "c", ImageDigest: digest(n), Trigger: hosting_model.TriggerManual, Status: status,
		}
		require.NoError(t, hosting_model.CreateDeployment(ctx, deployment))
		return deployment
	}
	create(0, hosting_model.StatusSuperseded)
	create(1, hosting_model.StatusSuperseded)
	create(2, hosting_model.StatusBuildFailed)
	create(3, hosting_model.StatusSuperseded)
	create(4, hosting_model.StatusSuperseded)
	live := create(5, hosting_model.StatusLive)
	create(6, hosting_model.StatusBuilding)
	target.LiveDeploymentID = live.ID
	require.NoError(t, hosting_model.UpdateTargetCols(ctx, target, "live_deployment_id"))

	keep, err := keptDigests(ctx)
	require.NoError(t, err)
	for n, want := range []bool{false, false, false, true, true, true, true} {
		assert.Equal(t, want, keep[digest(n)], "digest %d", n)
	}
}

func TestParseMetrics(t *testing.T) {
	metrics := parseMetrics(strings.NewReader("# HELP x y\n# TYPE x gauge\ngitw3_scaler_at_max 1\ngitw3_scaler_workers 5\ngitw3_scaler_nodes{state=\"ready\"} 5\n"))
	assert.Equal(t, map[string]float64{"gitw3_scaler_at_max": 1, "gitw3_scaler_workers": 5}, metrics)
}

func TestCheckAlertsReportsFailedDeploys(t *testing.T) {
	setupFakes(t)
	ctx := db.DefaultContext
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer manager.Close()
	defer test.MockVariableValue(&setting.Hosting.DokployURL, manager.URL)()
	_, user, target := newTarget(t)
	deployment, _ := queueDeployment(t, target, user, "v9.9.9")
	failBuild(ctx, target, deployment, "compile error")

	require.NoError(t, CheckAlerts(ctx))
	notices, err := system_model.Notices(ctx, 1, 50)
	require.NoError(t, err)
	found := false
	for _, notice := range notices {
		found = found || strings.Contains(notice.Description, "v9.9.9")
	}
	assert.True(t, found, "failed deploy raises an admin notice")
}

func TestClaimSubdomain(t *testing.T) {
	f := setupFakes(t)
	ctx := db.DefaultContext
	repo, user, target := newTarget(t)
	automatic := PoolDomain(ctx, target)
	require.NotNil(t, automatic)

	fqdn, err := CheckSubdomain(ctx, "My-Shop", 0)
	require.NoError(t, err)
	assert.Equal(t, "my-shop.apps.example.com", fqdn)
	_, err = CheckSubdomain(ctx, "infra", 0)
	require.ErrorIs(t, err, hosting_module.ErrSubdomainReserved)

	domain, err := ClaimSubdomain(ctx, target, "https://my-shop.apps.example.com", user.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-shop.apps.example.com", domain.FQDN)
	assert.Equal(t, "https://my-shop.apps.example.com", PublicURL(ctx, target))
	assert.Contains(t, f.dokploy.hosts(), "my-shop.apps.example.com")
	assert.NotContains(t, f.dokploy.hosts(), automatic.FQDN, "the old automatic address is released")
	_, err = hosting_model.GetDomainByFQDN(ctx, automatic.FQDN)
	require.ErrorIs(t, err, hosting_model.ErrDomainNotExist)

	again, err := ClaimSubdomain(ctx, target, "my-shop", user.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ID, again.ID, "claiming your own name again is a no-op")

	other, err := EnsureTarget(ctx, repo, &hosting_module.Target{Name: "api", Kind: hosting_module.KindDockerfile, Port: 80, Replicas: 1}, user, "")
	require.NoError(t, err)
	_, err = ClaimSubdomain(ctx, other, "my-shop", user.ID)
	require.ErrorIs(t, err, ErrSubdomainTaken)
	_, err = CheckSubdomain(ctx, "my-shop", other.ID)
	require.ErrorIs(t, err, ErrSubdomainTaken)

	third, err := EnsureTarget(ctx, repo, &hosting_module.Target{Name: "docs", Kind: hosting_module.KindDockerfile, Port: 80, Replicas: 1}, user, "cool-docs")
	require.NoError(t, err)
	assert.Equal(t, "https://cool-docs.apps.example.com", PublicURL(ctx, third), "first deploy uses the chosen address")
}
