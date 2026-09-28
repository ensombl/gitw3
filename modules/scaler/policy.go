// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package scaler grows and shrinks the Docker Swarm worker pool of GitW3
// managed hosting on DigitalOcean. It runs as its own service on the swarm
// manager (cmd/gitw3-scaler) and keeps state in a small SQLite database.
package scaler

import "time"

// Decision is what the policy wants the loop to do this tick.
type Decision string

const (
	ScaleUp   Decision = "SCALE_UP"
	ScaleDown Decision = "SCALE_DOWN"
	Noop      Decision = "NOOP"
)

// NodeStat is one swarm node in a snapshot.
type NodeStat struct {
	ID             string
	Hostname       string
	Addr           string
	Role           string // worker or manager
	State          string // ready, down, unknown
	Availability   string // active, pause, drain
	Labels         map[string]string
	NanoCPUs       int64
	MemoryBytes    int64
	ReservedCPUs   int64
	ReservedMemory int64
	RunningTasks   int
}

// IsSchedulableWorker reports whether app tasks can land on the node.
func (n NodeStat) IsSchedulableWorker() bool {
	return n.Role == "worker" && n.State == "ready" && n.Availability == "active"
}

// ClusterSnapshot is the cluster as seen at one instant.
type ClusterSnapshot struct {
	Nodes []NodeStat
	// PendingTasks counts app tasks the scheduler could not place: the most
	// honest "we are out of room" signal.
	PendingTasks int
	// PendingServices lists services with pending tasks, force-updated once
	// new capacity joins.
	PendingServices []string
	At              time.Time
}

// Workers returns the schedulable workers.
func (s ClusterSnapshot) Workers() []NodeStat {
	workers := make([]NodeStat, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		if node.IsSchedulableWorker() {
			workers = append(workers, node)
		}
	}
	return workers
}

// Utilization is the highest of reserved CPU and reserved memory across the
// schedulable workers, from 0 to 1.
func (s ClusterSnapshot) Utilization() float64 {
	var cpus, reservedCPUs, memory, reservedMemory int64
	for _, node := range s.Workers() {
		cpus += node.NanoCPUs
		reservedCPUs += node.ReservedCPUs
		memory += node.MemoryBytes
		reservedMemory += node.ReservedMemory
	}
	utilization := 0.0
	if cpus > 0 {
		utilization = float64(reservedCPUs) / float64(cpus)
	}
	if memory > 0 {
		utilization = max(utilization, float64(reservedMemory)/float64(memory))
	}
	return utilization
}

// Policy decides when to add or remove workers.
type Policy struct {
	MinWorkers   int
	MaxWorkers   int
	CPUHigh      float64
	CPULow       float64
	CooldownUp   time.Duration
	CooldownDown time.Duration
	LowSustain   time.Duration
}

// PolicyState is what the policy remembers between ticks.
type PolicyState struct {
	LastScaleUp   time.Time
	LastScaleDown time.Time
	// LowSince is when utilization first dropped below CPULow, zero while it is above.
	LowSince time.Time
	// Busy is true while a node is provisioning, joining or draining; the
	// policy never stacks changes.
	Busy bool
}

// Decide returns the action for a snapshot and updates state.LowSince.
func (p Policy) Decide(snapshot ClusterSnapshot, state *PolicyState) Decision {
	workers := len(snapshot.Workers())
	utilization := snapshot.Utilization()
	now := snapshot.At

	if utilization < p.CPULow && snapshot.PendingTasks == 0 {
		if state.LowSince.IsZero() {
			state.LowSince = now
		}
	} else {
		state.LowSince = time.Time{}
	}
	if state.Busy {
		return Noop
	}
	if workers < p.MinWorkers {
		return ScaleUp
	}
	if (snapshot.PendingTasks > 0 || utilization > p.CPUHigh) && workers < p.MaxWorkers &&
		now.Sub(state.LastScaleUp) >= p.CooldownUp {
		return ScaleUp
	}
	if workers > p.MinWorkers && !state.LowSince.IsZero() && now.Sub(state.LowSince) >= p.LowSustain &&
		now.Sub(state.LastScaleDown) >= p.CooldownDown && now.Sub(state.LastScaleUp) >= p.CooldownUp {
		return ScaleDown
	}
	return Noop
}

// PickScaleDown chooses the worker to remove: fewest running tasks, then the
// newest. Only nodes the scaler created are candidates.
func PickScaleDown(workers []NodeStat, managed map[string]Node) (NodeStat, bool) {
	var best NodeStat
	var bestNode Node
	found := false
	for _, worker := range workers {
		node, ok := managed[worker.ID]
		if !ok || node.State != NodeReady {
			continue
		}
		if !found || worker.RunningTasks < best.RunningTasks ||
			(worker.RunningTasks == best.RunningTasks && node.CreatedAt.After(bestNode.CreatedAt)) {
			best, bestNode, found = worker, node, true
		}
	}
	return best, found
}
