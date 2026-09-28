// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"forgejo.org/modules/setting"
)

// Rollout is the aggregate state of a Swarm rolling update.
type Rollout string

const (
	RolloutUnknown    Rollout = "unknown"
	RolloutInProgress Rollout = "in_progress"
	RolloutHealthy    Rollout = "healthy"
	RolloutRolledBack Rollout = "rolled_back"
	RolloutFailed     Rollout = "failed"
)

// ServiceStatus is one Swarm service as seen through the socket proxy.
type ServiceStatus struct {
	Name        string
	Image       string
	UpdateState string
	Desired     int
	Running     int
}

// SwarmClient reads service state from the manager's restricted Docker
// socket proxy. It is read-only: all writes go through Dokploy.
type SwarmClient interface {
	Services(ctx context.Context, name, stackNamespace string) ([]ServiceStatus, error)
}

// EvaluateRollout decides whether a deploy of the given image digests is done.
// digests maps service name (or "" for a single-service app) to the digest
// that should be running; services not listed only need to be healthy.
func EvaluateRollout(services []ServiceStatus, digests map[string]string) Rollout {
	if len(services) == 0 {
		return RolloutUnknown
	}
	result := RolloutHealthy
	for _, service := range services {
		switch service.UpdateState {
		case "rollback_completed", "rollback_paused":
			return RolloutRolledBack
		case "paused", "rollback_started":
			result = RolloutFailed
			continue
		case "updating":
			if result == RolloutHealthy {
				result = RolloutInProgress
			}
			continue
		}
		if digest := digestFor(service.Name, digests); digest != "" && !strings.Contains(service.Image, digest) {
			if result == RolloutHealthy {
				result = RolloutInProgress
			}
			continue
		}
		if service.Running < service.Desired && result == RolloutHealthy {
			result = RolloutInProgress
		}
	}
	return result
}

func digestFor(serviceName string, digests map[string]string) string {
	if digest, ok := digests[""]; ok {
		return digest
	}
	for name, digest := range digests {
		// Stack services are named <namespace>_<service>.
		if strings.HasSuffix(serviceName, "_"+name) {
			return digest
		}
	}
	return ""
}

type swarmProxyClient struct {
	baseURL string
	http    *http.Client
}

// NewSwarmClient returns a client for the configured socket proxy, or nil
// when none is configured (status then relies on Dokploy alone).
func NewSwarmClient() SwarmClient {
	if setting.Hosting.SwarmProxyURL == "" {
		return nil
	}
	return &swarmProxyClient{baseURL: setting.Hosting.SwarmProxyURL, http: &http.Client{Timeout: setting.Hosting.HTTPTimeout}}
}

func (c *swarmProxyClient) get(ctx context.Context, path string, query url.Values, output any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("docker %s: HTTP %d: %s", path, response.StatusCode, strings.TrimSpace(string(message)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(output)
}

type dockerService struct {
	ID   string `json:"ID"`
	Spec struct {
		Name string `json:"Name"`
		Mode struct {
			Replicated *struct {
				Replicas int `json:"Replicas"`
			} `json:"Replicated"`
		} `json:"Mode"`
		TaskTemplate struct {
			ContainerSpec struct {
				Image string `json:"Image"`
			} `json:"ContainerSpec"`
		} `json:"TaskTemplate"`
	} `json:"Spec"`
	UpdateStatus *struct {
		State string `json:"State"`
	} `json:"UpdateStatus"`
}

type dockerTask struct {
	ServiceID string `json:"ServiceID"`
	Status    struct {
		State string `json:"State"`
	} `json:"Status"`
}

func (c *swarmProxyClient) Services(ctx context.Context, name, stackNamespace string) ([]ServiceStatus, error) {
	filters := map[string][]string{}
	if stackNamespace != "" {
		filters["label"] = []string{"com.docker.stack.namespace=" + stackNamespace}
	} else {
		filters["name"] = []string{name}
	}
	encoded, _ := json.Marshal(filters)
	var services []dockerService
	if err := c.get(ctx, "/services", url.Values{"filters": {string(encoded)}}, &services); err != nil {
		return nil, err
	}
	result := make([]ServiceStatus, 0, len(services))
	for _, service := range services {
		// The name filter is a prefix match; keep exact matches only.
		if stackNamespace == "" && service.Spec.Name != name {
			continue
		}
		status := ServiceStatus{Name: service.Spec.Name, Image: service.Spec.TaskTemplate.ContainerSpec.Image}
		if service.Spec.Mode.Replicated != nil {
			status.Desired = service.Spec.Mode.Replicated.Replicas
		}
		if service.UpdateStatus != nil {
			status.UpdateState = service.UpdateStatus.State
		}
		taskFilters, _ := json.Marshal(map[string][]string{"service": {service.ID}, "desired-state": {"running"}})
		var tasks []dockerTask
		if err := c.get(ctx, "/tasks", url.Values{"filters": {string(taskFilters)}}, &tasks); err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.Status.State == "running" {
				status.Running++
			}
		}
		result = append(result, status)
	}
	return result, nil
}
