// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SwarmClient is the scaler's view of the swarm manager.
type SwarmClient interface {
	Snapshot(ctx context.Context) (ClusterSnapshot, error)
	JoinToken(ctx context.Context) (string, error)
	RotateWorkerToken(ctx context.Context) error
	Label(ctx context.Context, nodeID string, labels map[string]string) error
	Drain(ctx context.Context, nodeID string) error
	Remove(ctx context.Context, nodeID string) error
	ForceUpdate(ctx context.Context, serviceIDs []string) error
}

// DockerSwarm talks to the Docker Engine API, normally through a socket
// proxy that only exposes the endpoints below.
type DockerSwarm struct {
	baseURL string
	http    *http.Client
}

// NewDockerSwarm accepts unix:///path, tcp://host:port or http(s)://host:port.
func NewDockerSwarm(host string, timeout time.Duration) (*DockerSwarm, error) {
	client := &http.Client{Timeout: timeout}
	switch {
	case strings.HasPrefix(host, "unix://"):
		socket := strings.TrimPrefix(host, "unix://")
		client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		return &DockerSwarm{baseURL: "http://docker/v1.43", http: client}, nil
	case strings.HasPrefix(host, "tcp://"):
		return &DockerSwarm{baseURL: "http://" + strings.TrimPrefix(host, "tcp://") + "/v1.43", http: client}, nil
	case strings.HasPrefix(host, "http://"), strings.HasPrefix(host, "https://"):
		return &DockerSwarm{baseURL: strings.TrimRight(host, "/") + "/v1.43", http: client}, nil
	}
	return nil, fmt.Errorf("unsupported DOCKER_HOST %q", host)
}

func (d *DockerSwarm) do(ctx context.Context, method, path string, query url.Values, input, output any) error {
	endpoint := d.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := d.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("docker %s %s: HTTP %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(output)
}

type versioned struct {
	ID      string `json:"ID"`
	Version struct {
		Index uint64 `json:"Index"`
	} `json:"Version"`
	Spec map[string]any `json:"Spec"`
}

type dockerNode struct {
	ID   string `json:"ID"`
	Spec struct {
		Role         string            `json:"Role"`
		Availability string            `json:"Availability"`
		Labels       map[string]string `json:"Labels"`
	} `json:"Spec"`
	Description struct {
		Hostname  string `json:"Hostname"`
		Resources struct {
			NanoCPUs    int64 `json:"NanoCPUs"`
			MemoryBytes int64 `json:"MemoryBytes"`
		} `json:"Resources"`
	} `json:"Description"`
	Status struct {
		State string `json:"State"`
		Addr  string `json:"Addr"`
	} `json:"Status"`
}

type resources struct {
	NanoCPUs    int64 `json:"NanoCPUs"`
	MemoryBytes int64 `json:"MemoryBytes"`
}

type dockerTask struct {
	NodeID    string `json:"NodeID"`
	ServiceID string `json:"ServiceID"`
	Status    struct {
		State string `json:"State"`
		Err   string `json:"Err"`
	} `json:"Status"`
	Spec struct {
		Resources struct {
			Reservations resources `json:"Reservations"`
			Limits       resources `json:"Limits"`
		} `json:"Resources"`
	} `json:"Spec"`
}

func (d *DockerSwarm) Snapshot(ctx context.Context) (ClusterSnapshot, error) {
	snapshot := ClusterSnapshot{At: time.Now().UTC()}
	var nodes []dockerNode
	if err := d.do(ctx, http.MethodGet, "/nodes", nil, nil, &nodes); err != nil {
		return snapshot, err
	}
	filters, _ := json.Marshal(map[string][]string{"desired-state": {"running"}})
	var tasks []dockerTask
	if err := d.do(ctx, http.MethodGet, "/tasks", url.Values{"filters": {string(filters)}}, nil, &tasks); err != nil {
		return snapshot, err
	}
	stats := make(map[string]*NodeStat, len(nodes))
	for _, node := range nodes {
		stat := &NodeStat{
			ID: node.ID, Hostname: node.Description.Hostname, Addr: node.Status.Addr,
			Role: node.Spec.Role, State: node.Status.State, Availability: node.Spec.Availability,
			Labels: node.Spec.Labels, NanoCPUs: node.Description.Resources.NanoCPUs,
			MemoryBytes: node.Description.Resources.MemoryBytes,
		}
		stats[node.ID] = stat
	}
	pendingServices := map[string]bool{}
	for _, task := range tasks {
		switch task.Status.State {
		case "pending":
			snapshot.PendingTasks++
			pendingServices[task.ServiceID] = true
			continue
		case "running", "starting", "preparing", "assigned", "accepted", "ready":
		default:
			continue
		}
		stat, ok := stats[task.NodeID]
		if !ok {
			continue
		}
		stat.RunningTasks++
		// Reservations describe what a task needs; Dokploy sets limits, so
		// fall back to them as the conservative estimate.
		cpu, memory := task.Spec.Resources.Reservations.NanoCPUs, task.Spec.Resources.Reservations.MemoryBytes
		if cpu == 0 {
			cpu = task.Spec.Resources.Limits.NanoCPUs
		}
		if memory == 0 {
			memory = task.Spec.Resources.Limits.MemoryBytes
		}
		stat.ReservedCPUs += cpu
		stat.ReservedMemory += memory
	}
	for _, node := range nodes {
		snapshot.Nodes = append(snapshot.Nodes, *stats[node.ID])
	}
	for id := range pendingServices {
		snapshot.PendingServices = append(snapshot.PendingServices, id)
	}
	return snapshot, nil
}

func (d *DockerSwarm) swarm(ctx context.Context) (*versioned, string, error) {
	var info struct {
		versioned
		JoinTokens struct {
			Worker string `json:"Worker"`
		} `json:"JoinTokens"`
	}
	if err := d.do(ctx, http.MethodGet, "/swarm", nil, nil, &info); err != nil {
		return nil, "", err
	}
	return &info.versioned, info.JoinTokens.Worker, nil
}

// JoinToken is fetched fresh for every provision so a leaked, since-rotated
// token is useless.
func (d *DockerSwarm) JoinToken(ctx context.Context) (string, error) {
	_, token, err := d.swarm(ctx)
	return token, err
}

func (d *DockerSwarm) RotateWorkerToken(ctx context.Context) error {
	info, _, err := d.swarm(ctx)
	if err != nil {
		return err
	}
	return d.do(ctx, http.MethodPost, "/swarm/update", url.Values{
		"version": {fmt.Sprint(info.Version.Index)}, "rotateWorkerToken": {"true"},
	}, info.Spec, nil)
}

func (d *DockerSwarm) updateNode(ctx context.Context, nodeID string, mutate func(spec map[string]any)) error {
	var node versioned
	if err := d.do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(nodeID), nil, nil, &node); err != nil {
		return err
	}
	mutate(node.Spec)
	return d.do(ctx, http.MethodPost, "/nodes/"+url.PathEscape(nodeID)+"/update",
		url.Values{"version": {fmt.Sprint(node.Version.Index)}}, node.Spec, nil)
}

func (d *DockerSwarm) Label(ctx context.Context, nodeID string, labels map[string]string) error {
	return d.updateNode(ctx, nodeID, func(spec map[string]any) {
		current, _ := spec["Labels"].(map[string]any)
		if current == nil {
			current = map[string]any{}
		}
		for key, value := range labels {
			current[key] = value
		}
		spec["Labels"] = current
	})
}

func (d *DockerSwarm) Drain(ctx context.Context, nodeID string) error {
	return d.updateNode(ctx, nodeID, func(spec map[string]any) { spec["Availability"] = "drain" })
}

func (d *DockerSwarm) Remove(ctx context.Context, nodeID string) error {
	return d.do(ctx, http.MethodDelete, "/nodes/"+url.PathEscape(nodeID), nil, nil, nil)
}

// ForceUpdate reschedules services that had pending tasks so they spread
// onto a freshly joined worker.
func (d *DockerSwarm) ForceUpdate(ctx context.Context, serviceIDs []string) error {
	for _, id := range serviceIDs {
		var service versioned
		if err := d.do(ctx, http.MethodGet, "/services/"+url.PathEscape(id), nil, nil, &service); err != nil {
			return err
		}
		template, _ := service.Spec["TaskTemplate"].(map[string]any)
		if template == nil {
			continue
		}
		force, _ := template["ForceUpdate"].(float64)
		template["ForceUpdate"] = force + 1
		if err := d.do(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/update",
			url.Values{"version": {fmt.Sprint(service.Version.Index)}}, service.Spec, nil); err != nil {
			return err
		}
	}
	return nil
}
