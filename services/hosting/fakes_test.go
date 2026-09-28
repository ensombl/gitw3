// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	_ "forgejo.org/models/actions"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

type fakeDokploy struct {
	mu        sync.Mutex
	nextID    int
	apps      map[string]AppSpec
	env       map[string]map[string]string
	images    map[string]string
	stacks    map[string]string
	domains   map[string]DomainSpec
	mounts    map[string]string
	status    string
	deployErr error
}

func newFakeDokploy() *fakeDokploy {
	return &fakeDokploy{
		apps: map[string]AppSpec{}, env: map[string]map[string]string{}, images: map[string]string{},
		stacks: map[string]string{}, domains: map[string]DomainSpec{}, mounts: map[string]string{}, status: "done",
	}
}

func (f *fakeDokploy) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeDokploy) CreateApp(_ context.Context, spec AppSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id("app")
	f.apps[id] = spec
	return id, spec.AppName, nil
}

func (f *fakeDokploy) UpdateApp(_ context.Context, appID string, spec AppSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[appID] = spec
	return nil
}

func (f *fakeDokploy) DeleteApp(_ context.Context, appID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.apps, appID)
	return nil
}

func (f *fakeDokploy) SetEnv(_ context.Context, appID string, env map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.env[appID] = env
	return nil
}

func (f *fakeDokploy) DeployImage(_ context.Context, appID, imageRef, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deployErr != nil {
		return f.deployErr
	}
	f.images[appID] = imageRef
	return nil
}

func (f *fakeDokploy) AppState(_ context.Context, appID string) (*AppState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &AppState{AppName: f.apps[appID].AppName, Status: f.status}, nil
}

func (f *fakeDokploy) CreateCompose(_ context.Context, spec ComposeSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id("compose"), spec.AppName, nil
}

func (f *fakeDokploy) DeleteCompose(context.Context, string) error { return nil }

func (f *fakeDokploy) DeployStack(_ context.Context, composeID, compose string, env map[string]string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stacks[composeID] = compose
	f.env[composeID] = env
	return nil
}

func (f *fakeDokploy) ComposeState(context.Context, string) (*AppState, error) {
	return &AppState{Status: f.status}, nil
}

func (f *fakeDokploy) AddDomain(_ context.Context, spec DomainSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id("domain")
	f.domains[id] = spec
	return id, nil
}

func (f *fakeDokploy) RemoveDomain(_ context.Context, domainID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.domains, domainID)
	return nil
}

func (f *fakeDokploy) hosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	hosts := make([]string, 0, len(f.domains))
	for _, domain := range f.domains {
		hosts = append(hosts, domain.Host)
	}
	return hosts
}

type fakeBuilder struct {
	mu         sync.Mutex
	dispatched []BuildInputs
	cancelled  []int64
	states     map[int64]BuildState
}

func (f *fakeBuilder) Dispatch(_ context.Context, _ *user_model.User, inputs BuildInputs) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, inputs)
	return int64(1000 + len(f.dispatched)), nil
}

func (f *fakeBuilder) Cancel(_ context.Context, runID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, runID)
	return nil
}

func (f *fakeBuilder) Status(_ context.Context, runID int64) (BuildState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state, ok := f.states[runID]; ok {
		return state, nil
	}
	return BuildStateRunning, nil
}

func (f *fakeBuilder) RunLink(context.Context, int64) string { return "" }

type fakeRegistry struct {
	images map[string]bool
}

func (f *fakeRegistry) Exists(_ context.Context, image, digest string) (bool, error) {
	return f.images[image+"@"+digest], nil
}

type fakeDNS struct {
	mu      sync.Mutex
	records map[string]string
}

func (f *fakeDNS) CreateRecord(_ context.Context, fqdn string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("record-%d", len(f.records)+1)
	f.records[id] = fqdn
	return id, nil
}

func (f *fakeDNS) DeleteRecord(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, id)
	return nil
}

type fakeSwarm struct {
	services  []ServiceStatus
	diagnosis string
}

func (f *fakeSwarm) Diagnose(context.Context, string, string) (string, error) {
	return f.diagnosis, nil
}

func (f *fakeSwarm) Services(context.Context, string, string) ([]ServiceStatus, error) {
	return f.services, nil
}

type fakePublisher struct {
	calls     []string
	certified bool
}

func (f *fakePublisher) Call(_ context.Context, method, path string, input, output any) error {
	f.calls = append(f.calls, method+" "+path)
	if !f.certified && path != "/api/v1/platforms/deployment-certifications" {
		return &PublisherError{Status: http.StatusConflict, Message: "PPA certification is required"}
	}
	return nil
}

type fakes struct {
	dokploy   *fakeDokploy
	builder   *fakeBuilder
	registry  *fakeRegistry
	dns       *fakeDNS
	swarm     *fakeSwarm
	publisher *fakePublisher
}

func setupFakes(t *testing.T) *fakes {
	t.Helper()
	require := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	require(unittest.PrepareTestDatabase())
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Enabled, true))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.RequireW3DS, false))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.CallbackSecret, "callback-secret"))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.RegistryHost, "git.example.com"))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Domains.BaseDomain, "apps.example.com"))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Domains.PoolSize, 3))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Domains.AllowCustom, true))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Domains.TargetIP, "203.0.113.10"))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Scan.Policy, setting.HostingScanPolicyBlock))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.Scan.Severity, "CRITICAL"))
	t.Cleanup(test.MockVariableValue(&setting.Hosting.RegistryOwner, "deployments"))
	f := &fakes{
		dokploy: newFakeDokploy(), builder: &fakeBuilder{states: map[int64]BuildState{}},
		registry: &fakeRegistry{images: map[string]bool{}}, dns: &fakeDNS{records: map[string]string{}},
		swarm: &fakeSwarm{}, publisher: &fakePublisher{},
	}
	SetClients(Clients{Dokploy: f.dokploy, Builder: f.builder, Registry: f.registry, DNS: f.dns, Swarm: f.swarm, Publisher: f.publisher})
	t.Cleanup(func() { SetClients(Clients{}) })
	return f
}
