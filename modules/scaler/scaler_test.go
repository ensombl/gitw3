// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPolicy = Policy{
	MinWorkers: 1, MaxWorkers: 3, CPUHigh: 0.75, CPULow: 0.3,
	CooldownUp: 5 * time.Minute, CooldownDown: 15 * time.Minute, LowSustain: 20 * time.Minute,
}

func worker(id string, reservedCPU int64, tasks int) NodeStat {
	return NodeStat{
		ID: id, Addr: "10.0.0." + id, Role: "worker", State: "ready", Availability: "active",
		NanoCPUs: 2e9, MemoryBytes: 4 << 30, ReservedCPUs: reservedCPU, RunningTasks: tasks,
	}
}

func TestPolicyDecide(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		snapshot ClusterSnapshot
		state    PolicyState
		want     Decision
	}{
		{"below min", ClusterSnapshot{At: now}, PolicyState{}, ScaleUp},
		{"pending tasks", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 1e9, 2)}, PendingTasks: 1}, PolicyState{}, ScaleUp},
		{"cpu high", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 18e8, 3)}}, PolicyState{}, ScaleUp},
		{"cooldown", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 18e8, 3)}}, PolicyState{LastScaleUp: now.Add(-time.Minute)}, Noop},
		{"at max", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 2e9, 3), worker("2", 2e9, 3), worker("3", 2e9, 3)}, PendingTasks: 4}, PolicyState{}, Noop},
		{"busy", ClusterSnapshot{At: now, PendingTasks: 3}, PolicyState{Busy: true}, Noop},
		{"low not sustained", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 0, 0), worker("2", 0, 0)}}, PolicyState{LowSince: now.Add(-5 * time.Minute)}, Noop},
		{"low sustained", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 0, 0), worker("2", 0, 0)}}, PolicyState{LowSince: now.Add(-30 * time.Minute)}, ScaleDown},
		{"low but at min", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 0, 0)}}, PolicyState{LowSince: now.Add(-30 * time.Minute)}, Noop},
		{"low but pending", ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 0, 0), worker("2", 0, 0)}, PendingTasks: 1}, PolicyState{LowSince: now.Add(-30 * time.Minute)}, ScaleUp},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := c.state
			assert.Equal(t, c.want, testPolicy.Decide(c.snapshot, &state))
		})
	}

	state := PolicyState{}
	low := ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 0, 0), worker("2", 0, 0)}}
	testPolicy.Decide(low, &state)
	assert.Equal(t, now, state.LowSince, "low utilisation starts the sustain clock")
	testPolicy.Decide(ClusterSnapshot{At: now, Nodes: []NodeStat{worker("1", 2e9, 4)}}, &state)
	assert.True(t, state.LowSince.IsZero(), "load resets the sustain clock")
}

func TestPickScaleDown(t *testing.T) {
	older, newer := time.Unix(100, 0), time.Unix(200, 0)
	managed := map[string]Node{
		"a": {State: NodeReady, CreatedAt: older},
		"b": {State: NodeReady, CreatedAt: newer},
		"c": {State: NodeReady, CreatedAt: newer},
	}
	picked, ok := PickScaleDown([]NodeStat{worker("a", 0, 1), worker("b", 0, 1), worker("c", 0, 4), worker("manual", 0, 0)}, managed)
	require.True(t, ok)
	assert.Equal(t, "b", picked.ID, "fewest tasks, then newest; unmanaged nodes are never picked")
}

type fakeDO struct {
	droplets map[string]*Droplet
	next     int
	created  []DropletSpec
}

func (f *fakeDO) CreateDroplet(_ context.Context, spec DropletSpec) (string, error) {
	f.next++
	id := fmt.Sprint(f.next)
	f.droplets[id] = &Droplet{ID: id, Name: spec.Name, Status: "new"}
	f.created = append(f.created, spec)
	return id, nil
}

func (f *fakeDO) GetDroplet(_ context.Context, id string) (*Droplet, error) {
	if droplet, ok := f.droplets[id]; ok {
		return droplet, nil
	}
	return nil, ErrDropletNotFound
}

func (f *fakeDO) DeleteDroplet(_ context.Context, id string) error {
	delete(f.droplets, id)
	return nil
}

func (f *fakeDO) ListByTag(context.Context, string) ([]Droplet, error) {
	droplets := make([]Droplet, 0, len(f.droplets))
	for _, droplet := range f.droplets {
		droplets = append(droplets, *droplet)
	}
	return droplets, nil
}

type fakeSwarm struct {
	snapshot  ClusterSnapshot
	labels    map[string]map[string]string
	removed   []string
	forced    []string
	rotations int
}

func (f *fakeSwarm) Snapshot(context.Context) (ClusterSnapshot, error) { return f.snapshot, nil }
func (f *fakeSwarm) JoinToken(context.Context) (string, error)         { return "SWMTKN-test", nil }
func (f *fakeSwarm) RotateWorkerToken(context.Context) error {
	f.rotations++
	return nil
}

func (f *fakeSwarm) Label(_ context.Context, id string, labels map[string]string) error {
	f.labels[id] = labels
	return nil
}

func (f *fakeSwarm) node(id string) *NodeStat {
	for i := range f.snapshot.Nodes {
		if f.snapshot.Nodes[i].ID == id {
			return &f.snapshot.Nodes[i]
		}
	}
	return nil
}

func (f *fakeSwarm) Drain(_ context.Context, id string) error {
	f.node(id).Availability = "drain"
	return nil
}

func (f *fakeSwarm) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	nodes := f.snapshot.Nodes[:0]
	for _, node := range f.snapshot.Nodes {
		if node.ID != id {
			nodes = append(nodes, node)
		}
	}
	f.snapshot.Nodes = nodes
	return nil
}

func (f *fakeSwarm) ForceUpdate(_ context.Context, ids []string) error {
	f.forced = append(f.forced, ids...)
	return nil
}

func newTestLoop(t *testing.T) (*Loop, *fakeDO, *fakeSwarm, *time.Time) {
	t.Helper()
	store, err := OpenNodeStore(filepath.Join(t.TempDir(), "scaler.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	do := &fakeDO{droplets: map[string]*Droplet{}}
	swarm := &fakeSwarm{labels: map[string]map[string]string{}, snapshot: ClusterSnapshot{
		Nodes: []NodeStat{{ID: "mgr", Role: "manager", State: "ready", Availability: "active"}},
	}}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	loop := NewLoop(Config{
		Policy: testPolicy, WorkerTag: "gitw3-worker", ManagerAddr: "10.10.0.2", NamePrefix: "gitw3",
		JoinTimeout: 10 * time.Minute, TokenRotation: 7 * 24 * time.Hour,
	}, store, swarm, do)
	loop.now = func() time.Time { return now }
	return loop, do, swarm, &now
}

func trackedState(t *testing.T, loop *Loop, dropletID string) NodeState {
	t.Helper()
	nodes, err := loop.store.List()
	require.NoError(t, err)
	for _, node := range nodes {
		if node.DropletID == dropletID {
			return node.State
		}
	}
	return NodeDestroyed
}

func TestLoopScaleUpJoinAndDown(t *testing.T) {
	loop, do, swarm, now := newTestLoop(t)
	ctx := context.Background()

	// No workers: scale up to MinWorkers.
	require.NoError(t, loop.Tick(ctx))
	require.Len(t, do.created, 1)
	assert.Contains(t, do.created[0].UserData, "--token SWMTKN-test 10.10.0.2:2377")
	assert.Contains(t, do.created[0].Tags, "gitw3-worker")
	assert.Equal(t, NodeProvisioning, trackedState(t, loop, "1"))

	// Busy while provisioning: no second droplet even though still below min.
	require.NoError(t, loop.Tick(ctx))
	assert.Len(t, do.created, 1)

	do.droplets["1"].Status, do.droplets["1"].PrivateIP = "active", "10.10.0.11"
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, NodeJoining, trackedState(t, loop, "1"))

	swarm.snapshot.Nodes = append(swarm.snapshot.Nodes, NodeStat{ID: "w1", Addr: "10.10.0.11", Role: "worker", State: "ready", Availability: "active", NanoCPUs: 2e9, MemoryBytes: 4 << 30})
	swarm.snapshot.PendingServices = []string{"svc-a"}
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, NodeReady, trackedState(t, loop, "1"))
	assert.Equal(t, "1", swarm.labels["w1"]["gitw3.droplet"])
	assert.Equal(t, []string{"svc-a"}, swarm.forced)

	// Pending tasks after the cooldown: add a second worker.
	swarm.snapshot.PendingTasks = 2
	*now = now.Add(6 * time.Minute)
	require.NoError(t, loop.Tick(ctx))
	require.Len(t, do.created, 2)
	do.droplets["2"].Status, do.droplets["2"].PrivateIP = "active", "10.10.0.12"
	swarm.snapshot.PendingTasks = 0
	swarm.snapshot.Nodes = append(swarm.snapshot.Nodes, NodeStat{ID: "w2", Addr: "10.10.0.12", Role: "worker", State: "ready", Availability: "active", NanoCPUs: 2e9, MemoryBytes: 4 << 30})
	require.NoError(t, loop.Tick(ctx))
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, NodeReady, trackedState(t, loop, "2"))

	// Idle long enough: drain the newest emptiest worker, then delete it.
	*now = now.Add(10 * time.Minute)
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, "active", swarm.node("w2").Availability, "low utilisation must be sustained")
	*now = now.Add(25 * time.Minute)
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, "drain", swarm.node("w2").Availability)
	assert.Equal(t, NodeDraining, trackedState(t, loop, "2"))

	require.NoError(t, loop.Tick(ctx)) // zero tasks → delete droplet
	assert.NotContains(t, do.droplets, "2")
	assert.Equal(t, NodeDeleting, trackedState(t, loop, "2"))
	swarm.node("w2").State = "down"
	require.NoError(t, loop.Tick(ctx))
	require.NoError(t, loop.Tick(ctx))
	assert.Contains(t, swarm.removed, "w2")
	assert.Equal(t, NodeDestroyed, trackedState(t, loop, "2"))
	assert.Equal(t, NodeReady, trackedState(t, loop, "1"), "MinWorkers is kept")

	metrics := loop.Metrics()
	assert.Equal(t, 1, metrics.Workers)
	assert.Empty(t, metrics.LastError)
}

func TestLoopAdoptsOrphansAndReplacesDeadWorkers(t *testing.T) {
	loop, do, swarm, now := newTestLoop(t)
	ctx := context.Background()
	// A droplet created before a crash, already joined.
	do.droplets["77"] = &Droplet{ID: "77", Name: "gitw3-worker-old", Status: "active", PrivateIP: "10.10.0.77", CreatedAt: now.Add(-time.Hour)}
	swarm.snapshot.Nodes = append(swarm.snapshot.Nodes, NodeStat{ID: "w77", Addr: "10.10.0.77", Role: "worker", State: "ready", Availability: "active", NanoCPUs: 2e9, MemoryBytes: 4 << 30})
	require.NoError(t, loop.Tick(ctx))
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, NodeReady, trackedState(t, loop, "77"))
	assert.Empty(t, do.created, "adopted worker satisfies MinWorkers")

	// The worker dies and stays down past the join timeout: replace it.
	swarm.node("w77").State = "down"
	require.NoError(t, loop.Tick(ctx))
	require.Len(t, do.created, 1, "capacity below MinWorkers is replaced right away")
	do.droplets["1"].Status, do.droplets["1"].PrivateIP = "active", "10.10.0.21"
	require.NoError(t, loop.Tick(ctx))
	swarm.snapshot.Nodes = append(swarm.snapshot.Nodes, NodeStat{ID: "w1", Addr: "10.10.0.21", Role: "worker", State: "ready", Availability: "active", NanoCPUs: 2e9, MemoryBytes: 4 << 30})
	*now = now.Add(11 * time.Minute)
	require.NoError(t, loop.Tick(ctx))
	assert.Equal(t, NodeFailed, trackedState(t, loop, "77"))
	require.NoError(t, loop.Tick(ctx))
	assert.NotContains(t, do.droplets, "77")
	require.NoError(t, loop.Tick(ctx))
	assert.Contains(t, swarm.removed, "w77")
	require.NoError(t, loop.Tick(ctx))
	assert.Len(t, do.created, 1, "the dead worker is not replaced twice")
	assert.Equal(t, NodeReady, trackedState(t, loop, "1"))
}

func TestTokenRotation(t *testing.T) {
	loop, _, swarm, now := newTestLoop(t)
	ctx := context.Background()
	require.NoError(t, loop.rotateToken(ctx))
	assert.Zero(t, swarm.rotations, "first run only records the time")
	*now = now.Add(8 * 24 * time.Hour)
	require.NoError(t, loop.rotateToken(ctx))
	assert.Equal(t, 1, swarm.rotations)
	require.NoError(t, loop.rotateToken(ctx))
	assert.Equal(t, 1, swarm.rotations)
}

func TestWorkerCloudInitMatchesTerraform(t *testing.T) {
	rendered := WorkerCloudInit("TOKEN", "10.0.0.1")
	assert.True(t, strings.HasPrefix(rendered, "#cloud-config\n"))
	assert.Contains(t, rendered, "docker swarm join")
	assert.Contains(t, rendered, "--token TOKEN 10.0.0.1:2377")
}
