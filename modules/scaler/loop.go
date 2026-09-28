// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Config holds everything the loop needs besides its clients.
type Config struct {
	Policy        Policy
	Droplet       DropletSpec // Name and UserData are filled per node
	WorkerTag     string
	ManagerAddr   string
	NamePrefix    string
	JoinTimeout   time.Duration
	TokenRotation time.Duration
}

// Loop reconciles DigitalOcean, the swarm and its own state every tick.
type Loop struct {
	config Config
	store  *NodeStore
	swarm  SwarmClient
	do     DOClient
	now    func() time.Time

	mu      sync.Mutex
	metrics Metrics
}

// Metrics is exposed on /metrics for alerting.
type Metrics struct {
	Workers      int
	PendingTasks int
	Utilization  float64
	AtMax        bool
	States       map[NodeState]int
	LastTick     time.Time
	LastError    string
	LastDecision Decision
}

func NewLoop(config Config, store *NodeStore, swarm SwarmClient, do DOClient) *Loop {
	return &Loop{config: config, store: store, swarm: swarm, do: do, now: func() time.Time { return time.Now().UTC() }}
}

// Metrics returns a copy of the latest tick's metrics.
func (l *Loop) Metrics() Metrics {
	l.mu.Lock()
	defer l.mu.Unlock()
	metrics := l.metrics
	metrics.States = make(map[NodeState]int, len(l.metrics.States))
	for state, count := range l.metrics.States {
		metrics.States[state] = count
	}
	return metrics
}

// Run ticks until the context ends.
func (l *Loop) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := l.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scaler tick", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one reconcile + decide + act cycle.
func (l *Loop) Tick(ctx context.Context) error {
	err := l.tick(ctx)
	l.mu.Lock()
	l.metrics.LastTick = l.now()
	if err != nil {
		l.metrics.LastError = err.Error()
	} else {
		l.metrics.LastError = ""
	}
	l.mu.Unlock()
	return err
}

func (l *Loop) tick(ctx context.Context) error {
	snapshot, err := l.swarm.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	snapshot.At = l.now()
	nodes, err := l.reconcile(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if err := l.rotateToken(ctx); err != nil {
		slog.Warn("rotate worker join token", "error", err)
	}

	state, err := l.store.LoadPolicyState()
	if err != nil {
		return err
	}
	managed := map[string]Node{}
	states := map[NodeState]int{}
	for _, node := range nodes {
		states[node.State]++
		switch node.State {
		case NodeProvisioning, NodeJoining, NodeDraining, NodeDeleting, NodeRemoving, NodeFailed:
			state.Busy = true
		}
		if node.SwarmNodeID != "" {
			managed[node.SwarmNodeID] = node
		}
	}
	decision := l.config.Policy.Decide(snapshot, &state)
	workers := len(snapshot.Workers())
	l.mu.Lock()
	l.metrics.Workers, l.metrics.PendingTasks, l.metrics.Utilization = workers, snapshot.PendingTasks, snapshot.Utilization()
	l.metrics.AtMax = workers >= l.config.Policy.MaxWorkers && (snapshot.PendingTasks > 0 || snapshot.Utilization() > l.config.Policy.CPUHigh)
	l.metrics.States, l.metrics.LastDecision = states, decision
	l.mu.Unlock()

	switch decision {
	case ScaleUp:
		if err := l.scaleUp(ctx); err != nil {
			return fmt.Errorf("scale up: %w", err)
		}
		state.LastScaleUp = l.now()
	case ScaleDown:
		worker, ok := PickScaleDown(snapshot.Workers(), managed)
		if ok {
			if err := l.scaleDown(ctx, managed[worker.ID]); err != nil {
				return fmt.Errorf("scale down: %w", err)
			}
			state.LastScaleDown = l.now()
			state.LowSince = time.Time{}
		}
	}
	return l.store.SavePolicyState(state)
}

// reconcile adopts droplets the store does not know (a crash mid-provision
// never leaves orphans), advances every node's state machine and returns the
// nodes still tracked.
func (l *Loop) reconcile(ctx context.Context, snapshot ClusterSnapshot) ([]Node, error) {
	droplets, err := l.do.ListByTag(ctx, l.config.WorkerTag)
	if err != nil {
		return nil, err
	}
	nodes, err := l.store.List()
	if err != nil {
		return nil, err
	}
	known := make(map[string]*Node, len(nodes))
	for i := range nodes {
		known[nodes[i].DropletID] = &nodes[i]
	}
	byID := make(map[string]Droplet, len(droplets))
	for _, droplet := range droplets {
		byID[droplet.ID] = droplet
		if _, ok := known[droplet.ID]; ok {
			continue
		}
		adopted := Node{DropletID: droplet.ID, Name: droplet.Name, State: NodeProvisioning, PrivateIP: droplet.PrivateIP, CreatedAt: droplet.CreatedAt, UpdatedAt: l.now()}
		if swarmNode := findSwarmNode(snapshot, droplet.PrivateIP); swarmNode != nil {
			adopted.State, adopted.SwarmNodeID = NodeJoining, swarmNode.ID
		}
		slog.Info("adopting worker droplet", "droplet", droplet.ID, "state", adopted.State)
		if err := l.store.Save(&adopted); err != nil {
			return nil, err
		}
		nodes = append(nodes, adopted)
		known[droplet.ID] = &nodes[len(nodes)-1]
	}

	result := make([]Node, 0, len(nodes))
	for i := range nodes {
		node := nodes[i]
		droplet, exists := byID[node.DropletID]
		if err := l.advance(ctx, snapshot, &node, droplet, exists); err != nil {
			slog.Warn("advance worker", "droplet", node.DropletID, "state", node.State, "error", err)
		}
		if node.State == NodeDestroyed {
			if err := l.store.Delete(node.DropletID); err != nil {
				return nil, err
			}
			continue
		}
		result = append(result, node)
	}
	l.removeGhostWorkers(ctx, snapshot, result)
	return result, nil
}

// advance moves one node through Requested → … → Destroyed (design §6.2).
func (l *Loop) advance(ctx context.Context, snapshot ClusterSnapshot, node *Node, droplet Droplet, exists bool) error {
	save := func(state NodeState, message string) error {
		node.State, node.Error, node.UpdatedAt = state, message, l.now()
		return l.store.Save(node)
	}
	swarmNode := findSwarmNodeByID(snapshot, node.SwarmNodeID)
	if swarmNode == nil && node.PrivateIP != "" {
		swarmNode = findSwarmNode(snapshot, node.PrivateIP)
	}
	if !exists && node.State != NodeDeleting && node.State != NodeRemoving && node.State != NodeFailed {
		// Someone deleted the droplet behind our back.
		return save(NodeRemoving, "droplet disappeared")
	}
	switch node.State {
	case NodeProvisioning:
		if droplet.Status == "active" && droplet.PrivateIP != "" {
			node.PrivateIP = droplet.PrivateIP
			return save(NodeJoining, "")
		}
		if l.now().Sub(node.CreatedAt) > l.config.JoinTimeout {
			return save(NodeFailed, "droplet never became active")
		}
	case NodeJoining:
		if swarmNode != nil && swarmNode.State == "ready" {
			node.SwarmNodeID = swarmNode.ID
			if err := l.swarm.Label(ctx, swarmNode.ID, map[string]string{
				"gitw3.pool": "apps", "gitw3.droplet": node.DropletID,
			}); err != nil {
				return err
			}
			if len(snapshot.PendingServices) > 0 {
				if err := l.swarm.ForceUpdate(ctx, snapshot.PendingServices); err != nil {
					slog.Warn("rebalance pending services", "error", err)
				}
			}
			slog.Info("worker ready", "droplet", node.DropletID, "node", swarmNode.ID)
			return save(NodeReady, "")
		}
		if l.now().Sub(node.CreatedAt) > l.config.JoinTimeout {
			return save(NodeFailed, "join timeout")
		}
	case NodeReady:
		if swarmNode != nil && swarmNode.Availability == "drain" {
			return save(NodeDraining, "")
		}
		// A dead worker is replaced: Swarm reschedules its tasks, and once it
		// has stayed down past the join timeout the scaler deletes it so the
		// policy can provision a fresh one.
		switch down := swarmNode == nil || swarmNode.State == "down"; {
		case down && node.Error != "down":
			return save(NodeReady, "down")
		case down && l.now().Sub(node.UpdatedAt) > l.config.JoinTimeout:
			return save(NodeFailed, "worker stayed down")
		case !down && node.Error == "down":
			return save(NodeReady, "")
		}
	case NodeDraining:
		if swarmNode == nil || swarmNode.RunningTasks == 0 {
			if err := l.do.DeleteDroplet(ctx, node.DropletID); err != nil {
				return err
			}
			return save(NodeDeleting, "")
		}
	case NodeDeleting:
		if !exists && (swarmNode == nil || swarmNode.State == "down") {
			return save(NodeRemoving, "")
		}
	case NodeRemoving:
		if swarmNode != nil {
			if swarmNode.State != "down" {
				return nil
			}
			if err := l.swarm.Remove(ctx, swarmNode.ID); err != nil {
				return err
			}
		}
		return save(NodeDestroyed, node.Error)
	case NodeFailed:
		if exists {
			if err := l.do.DeleteDroplet(ctx, node.DropletID); err != nil {
				return err
			}
		}
		if swarmNode != nil && swarmNode.State == "down" {
			_ = l.swarm.Remove(ctx, swarmNode.ID)
		}
		if !exists {
			return save(NodeDestroyed, node.Error)
		}
	}
	return nil
}

// removeGhostWorkers drops swarm workers that are down and belong to no
// tracked droplet, e.g. a worker whose droplet was destroyed during an outage.
func (l *Loop) removeGhostWorkers(ctx context.Context, snapshot ClusterSnapshot, nodes []Node) {
	tracked := map[string]bool{}
	for _, node := range nodes {
		tracked[node.SwarmNodeID] = true
		tracked[node.PrivateIP] = true
	}
	for _, swarmNode := range snapshot.Nodes {
		if swarmNode.Role != "worker" || swarmNode.State != "down" || tracked[swarmNode.ID] || tracked[swarmNode.Addr] {
			continue
		}
		if swarmNode.Labels["gitw3.droplet"] == "" {
			continue // added by hand; leave it to the operator
		}
		if err := l.swarm.Remove(ctx, swarmNode.ID); err != nil {
			slog.Warn("remove ghost worker", "node", swarmNode.ID, "error", err)
		}
	}
}

func (l *Loop) scaleUp(ctx context.Context) error {
	token, err := l.swarm.JoinToken(ctx)
	if err != nil {
		return err
	}
	spec := l.config.Droplet
	spec.Name = fmt.Sprintf("%s-worker-%s", l.config.NamePrefix, strings.ToLower(l.now().Format("20060102-150405")))
	spec.Tags = append(append([]string(nil), spec.Tags...), l.config.WorkerTag)
	spec.UserData = WorkerCloudInit(token, l.config.ManagerAddr)
	id, err := l.do.CreateDroplet(ctx, spec)
	if err != nil {
		return err
	}
	slog.Info("scaling up", "droplet", id, "name", spec.Name)
	return l.store.Save(&Node{DropletID: id, Name: spec.Name, State: NodeProvisioning, CreatedAt: l.now(), UpdatedAt: l.now()})
}

func (l *Loop) scaleDown(ctx context.Context, node Node) error {
	if err := l.swarm.Drain(ctx, node.SwarmNodeID); err != nil {
		return err
	}
	slog.Info("scaling down", "droplet", node.DropletID, "node", node.SwarmNodeID)
	node.State, node.UpdatedAt = NodeDraining, l.now()
	return l.store.Save(&node)
}

func (l *Loop) rotateToken(ctx context.Context) error {
	if l.config.TokenRotation <= 0 {
		return nil
	}
	last, err := l.store.LastTokenRotation()
	if err != nil {
		return err
	}
	if !last.IsZero() && l.now().Sub(last) < l.config.TokenRotation {
		return nil
	}
	if !last.IsZero() {
		if err := l.swarm.RotateWorkerToken(ctx); err != nil {
			return err
		}
		slog.Info("rotated worker join token")
	}
	return l.store.SetLastTokenRotation(l.now())
}

func findSwarmNode(snapshot ClusterSnapshot, privateIP string) *NodeStat {
	if privateIP == "" {
		return nil
	}
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Addr == privateIP && snapshot.Nodes[i].Role == "worker" {
			return &snapshot.Nodes[i]
		}
	}
	return nil
}

func findSwarmNodeByID(snapshot ClusterSnapshot, id string) *NodeStat {
	if id == "" {
		return nil
	}
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].ID == id {
			return &snapshot.Nodes[i]
		}
	}
	return nil
}

// WorkerCloudInit renders the worker bootstrap; it matches
// contrib/hosting/infra/cloud-init/worker.yaml.tftpl.
func WorkerCloudInit(joinToken, managerAddr string) string {
	return `#cloud-config
package_update: true
packages:
  - ca-certificates
  - curl

write_files:
  - path: /etc/ssh/sshd_config.d/10-gitw3.conf
    content: |
      PasswordAuthentication no
      KbdInteractiveAuthentication no
      PermitRootLogin prohibit-password
  - path: /etc/docker/daemon.json
    content: |
      {"log-driver": "json-file", "log-opts": {"max-size": "20m", "max-file": "3"}, "live-restore": false}

runcmd:
  - systemctl restart ssh || systemctl restart sshd
  - curl -fsSL https://get.docker.com | sh
  - systemctl enable --now docker
  - PRIVATE_IP=$(curl -fsS http://169.254.169.254/metadata/v1/interfaces/private/0/ipv4/address)
  - docker swarm join --advertise-addr "$PRIVATE_IP" --listen-addr "$PRIVATE_IP:2377" --token ` + joinToken + ` ` + managerAddr + `:2377
`
}
