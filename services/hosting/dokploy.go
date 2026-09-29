// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
)

// Dokploy API shapes, pinned against Dokploy's tRPC/OpenAPI routers
// (application, compose, domain, mounts). Dokploy moves quickly: when
// upgrading it, re-check these procedure names and payloads.

// AppSpec describes a single-image Swarm service managed by Dokploy.
type AppSpec struct {
	Name        string
	AppName     string
	Description string
	Replicas    int
	NanoCPUs    int64
	MemoryBytes int64
	Port        int
	Healthcheck *HealthcheckSpec
}

// HealthcheckSpec is an in-container HTTP probe.
type HealthcheckSpec struct {
	Path     string
	Port     int
	Interval time.Duration
	Timeout  time.Duration
}

// ComposeSpec describes a Swarm stack managed by Dokploy.
type ComposeSpec struct {
	Name        string
	AppName     string
	Description string
}

// DomainSpec routes a host name through Traefik to an app or stack service.
type DomainSpec struct {
	Host        string
	Port        int
	AppID       string
	ComposeID   string
	ServiceName string
}

// AppState is Dokploy's view of an app or stack.
type AppState struct {
	AppName string
	Status  string // idle, running, done, error
	// LastDeployTitle and LastDeployStatus describe Dokploy's newest deploy
	// job of the app, which GitW3 titles with its deployment ID.
	LastDeployTitle  string
	LastDeployStatus string
}

// DeployFailed reports whether Dokploy's job for the given GitW3 deployment
// failed before handing the service to Swarm (for example a failed pull).
func (s *AppState) DeployFailed(deploymentID int64) bool {
	return s.LastDeployStatus == "error" && strings.HasSuffix(s.LastDeployTitle, "(#"+strconv.FormatInt(deploymentID, 10)+")")
}

type dokployDeploy struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}

// newestDeploy returns the most recently created deploy job; Dokploy lists
// them newest first.
func newestDeploy(deploys []dokployDeploy) dokployDeploy {
	if len(deploys) == 0 {
		return dokployDeploy{}
	}
	return deploys[0]
}

// ManagedResource is an app or stack in GitW3's Dokploy environment.
type ManagedResource struct {
	ID          string
	Compose     bool
	Description string
	CreatedAt   time.Time
}

// managedDescriptionPrefix marks the apps and stacks GitW3 creates.
const managedDescriptionPrefix = "Managed by GitW3 for "

// DokployClient is the subset of Dokploy the deploy service uses.
type DokployClient interface {
	// ListManaged returns the apps and stacks of the configured environment.
	ListManaged(ctx context.Context) ([]ManagedResource, error)
	CreateApp(ctx context.Context, spec AppSpec) (appID, appName string, err error)
	UpdateApp(ctx context.Context, appID string, spec AppSpec) error
	DeleteApp(ctx context.Context, appID string) error
	SetEnv(ctx context.Context, appID string, env map[string]string) error
	DeployImage(ctx context.Context, appID, imageRef, title string) error
	AppState(ctx context.Context, appID string) (*AppState, error)

	CreateCompose(ctx context.Context, spec ComposeSpec) (composeID, appName string, err error)
	DeleteCompose(ctx context.Context, composeID string) error
	DeployStack(ctx context.Context, composeID, compose string, env map[string]string, title string) error
	ComposeState(ctx context.Context, composeID string) (*AppState, error)

	AddDomain(ctx context.Context, spec DomainSpec) (domainID string, err error)
	RemoveDomain(ctx context.Context, domainID string) error
}

type dokployHTTPClient struct {
	baseURL       string
	apiKey        string
	environmentID string
	serverID      string
	http          *http.Client
}

// NewDokployClient returns a client for the configured Dokploy instance.
func NewDokployClient() DokployClient {
	return &dokployHTTPClient{
		baseURL:       setting.Hosting.DokployURL,
		apiKey:        setting.Hosting.DokployAPIKey,
		environmentID: setting.Hosting.DokployEnvironmentID,
		serverID:      setting.Hosting.DokployServerID,
		http:          &http.Client{Timeout: setting.Hosting.HTTPTimeout},
	}
}

// DokployError is a non-2xx answer from Dokploy.
type DokployError struct {
	Procedure string
	Status    int
	Message   string
}

func (e *DokployError) Error() string {
	return fmt.Sprintf("dokploy %s: HTTP %d: %s", e.Procedure, e.Status, e.Message)
}

func (c *dokployHTTPClient) call(ctx context.Context, method, procedure string, input, output any) error {
	endpoint := c.baseURL + "/api/" + procedure
	var body io.Reader
	if method == http.MethodGet {
		if values, ok := input.(url.Values); ok {
			endpoint += "?" + values.Encode()
		}
	} else {
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
	request.Header.Set("x-api-key", c.apiKey)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("dokploy %s: %w", procedure, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		var parsed struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &parsed) == nil && parsed.Message != "" {
			message = parsed.Message
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return &DokployError{Procedure: procedure, Status: response.StatusCode, Message: message}
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, output)
}

func (c *dokployHTTPClient) CreateApp(ctx context.Context, spec AppSpec) (string, string, error) {
	input := map[string]any{
		"name": spec.Name, "appName": spec.AppName, "description": spec.Description,
		"environmentId": c.environmentID,
	}
	if c.serverID != "" {
		input["serverId"] = c.serverID
	}
	var created struct {
		ApplicationID string `json:"applicationId"`
		AppName       string `json:"appName"`
	}
	if err := c.call(ctx, http.MethodPost, "application.create", input, &created); err != nil {
		return "", "", err
	}
	if created.ApplicationID == "" {
		return "", "", errors.New("dokploy application.create returned no applicationId")
	}
	if err := c.UpdateApp(ctx, created.ApplicationID, spec); err != nil {
		return created.ApplicationID, created.AppName, err
	}
	return created.ApplicationID, created.AppName, nil
}

func (c *dokployHTTPClient) UpdateApp(ctx context.Context, appID string, spec AppSpec) error {
	input := map[string]any{
		"applicationId": appID,
		"replicas":      max(spec.Replicas, 1),
		// A bad release must never fully replace a good one: Swarm monitors
		// each updated task and rolls the service back on failure.
		"updateConfigSwarm": map[string]any{
			"Parallelism": 1, "Delay": int64(5 * time.Second), "FailureAction": "rollback",
			"Monitor": int64(30 * time.Second), "MaxFailureRatio": 0, "Order": "start-first",
		},
		"rollbackConfigSwarm": map[string]any{
			"Parallelism": 1, "Delay": int64(time.Second), "FailureAction": "pause",
			"Monitor": int64(10 * time.Second), "MaxFailureRatio": 0, "Order": "start-first",
		},
	}
	// A Dokploy registry on an app means "push the image there after pulling
	// it", which fails for digest references. Pull credentials go on the
	// Docker provider instead (DeployImage), so clear any registry set by
	// earlier versions.
	input["registryId"] = nil
	if len(setting.Hosting.PlacementConstraints) > 0 {
		input["placementSwarm"] = map[string]any{"Constraints": setting.Hosting.PlacementConstraints}
	}
	if spec.NanoCPUs > 0 {
		input["cpuLimit"] = strconv.FormatInt(spec.NanoCPUs, 10)
	}
	if spec.MemoryBytes > 0 {
		input["memoryLimit"] = strconv.FormatInt(spec.MemoryBytes, 10)
	}
	if check := spec.Healthcheck; check != nil {
		interval, timeout := check.Interval, check.Timeout
		if interval <= 0 {
			interval = 10 * time.Second
		}
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		probe := fmt.Sprintf("wget -q -O /dev/null http://127.0.0.1:%d%s || curl -fsS -o /dev/null http://127.0.0.1:%d%s || exit 1",
			check.Port, check.Path, check.Port, check.Path)
		input["healthCheckSwarm"] = map[string]any{
			"Test":     []string{"CMD-SHELL", probe},
			"Interval": int64(interval), "Timeout": int64(timeout),
			"StartPeriod": int64(15 * time.Second), "Retries": 3,
		}
	}
	return c.call(ctx, http.MethodPost, "application.update", input, nil)
}

func (c *dokployHTTPClient) ListManaged(ctx context.Context) ([]ManagedResource, error) {
	type resource struct {
		ApplicationID string    `json:"applicationId"`
		ComposeID     string    `json:"composeId"`
		Description   string    `json:"description"`
		CreatedAt     time.Time `json:"createdAt"`
	}
	var environment struct {
		Applications []resource `json:"applications"`
		Compose      []resource `json:"compose"`
	}
	if err := c.call(ctx, http.MethodGet, "environment.one", url.Values{"environmentId": {c.environmentID}}, &environment); err != nil {
		return nil, err
	}
	managed := make([]ManagedResource, 0, len(environment.Applications)+len(environment.Compose))
	for _, app := range environment.Applications {
		managed = append(managed, ManagedResource{ID: app.ApplicationID, Description: app.Description, CreatedAt: app.CreatedAt})
	}
	for _, stack := range environment.Compose {
		managed = append(managed, ManagedResource{ID: stack.ComposeID, Compose: true, Description: stack.Description, CreatedAt: stack.CreatedAt})
	}
	return managed, nil
}

func (c *dokployHTTPClient) DeleteApp(ctx context.Context, appID string) error {
	return c.call(ctx, http.MethodPost, "application.delete", map[string]any{"applicationId": appID}, nil)
}

func (c *dokployHTTPClient) SetEnv(ctx context.Context, appID string, env map[string]string) error {
	return c.call(ctx, http.MethodPost, "application.saveEnvironment", map[string]any{
		"applicationId": appID, "env": FormatEnv(env), "buildArgs": "", "buildSecrets": "", "createEnvFile": false,
	}, nil)
}

func (c *dokployHTTPClient) DeployImage(ctx context.Context, appID, imageRef, title string) error {
	// Dokploy pulls with these credentials and hands them to Swarm so workers
	// can pull too. It requires the fields to be present, even as null.
	provider := map[string]any{
		"applicationId": appID, "dockerImage": imageRef,
		"username": nil, "password": nil, "registryUrl": nil,
	}
	if setting.Hosting.RegistryPullToken != "" {
		provider["username"] = setting.Hosting.RegistryPullUser
		provider["password"] = setting.Hosting.RegistryPullToken
		provider["registryUrl"] = setting.HostingRegistryHost()
	}
	if err := c.call(ctx, http.MethodPost, "application.saveDockerProvider", provider, nil); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "application.deploy", map[string]any{
		"applicationId": appID, "title": title,
	}, nil)
}

func (c *dokployHTTPClient) AppState(ctx context.Context, appID string) (*AppState, error) {
	var app struct {
		AppName           string          `json:"appName"`
		ApplicationStatus string          `json:"applicationStatus"`
		Deployments       []dokployDeploy `json:"deployments"`
	}
	if err := c.call(ctx, http.MethodGet, "application.one", url.Values{"applicationId": {appID}}, &app); err != nil {
		return nil, err
	}
	last := newestDeploy(app.Deployments)
	return &AppState{AppName: app.AppName, Status: app.ApplicationStatus, LastDeployTitle: last.Title, LastDeployStatus: last.Status}, nil
}

func (c *dokployHTTPClient) CreateCompose(ctx context.Context, spec ComposeSpec) (string, string, error) {
	input := map[string]any{
		"name": spec.Name, "appName": spec.AppName, "description": spec.Description,
		"environmentId": c.environmentID, "composeType": "stack", "sourceType": "raw", "composeFile": "",
	}
	if c.serverID != "" {
		input["serverId"] = c.serverID
	}
	var created struct {
		ComposeID string `json:"composeId"`
		AppName   string `json:"appName"`
	}
	if err := c.call(ctx, http.MethodPost, "compose.create", input, &created); err != nil {
		return "", "", err
	}
	if created.ComposeID == "" {
		return "", "", errors.New("dokploy compose.create returned no composeId")
	}
	return created.ComposeID, created.AppName, nil
}

func (c *dokployHTTPClient) DeleteCompose(ctx context.Context, composeID string) error {
	return c.call(ctx, http.MethodPost, "compose.delete", map[string]any{"composeId": composeID, "deleteVolumes": false}, nil)
}

func (c *dokployHTTPClient) DeployStack(ctx context.Context, composeID, compose string, env map[string]string, title string) error {
	if err := c.call(ctx, http.MethodPost, "compose.update", map[string]any{
		"composeId": composeID, "composeFile": compose, "sourceType": "raw", "composeType": "stack", "env": FormatEnv(env),
	}, nil); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "compose.deploy", map[string]any{"composeId": composeID, "title": title}, nil)
}

func (c *dokployHTTPClient) ComposeState(ctx context.Context, composeID string) (*AppState, error) {
	var compose struct {
		AppName       string          `json:"appName"`
		ComposeStatus string          `json:"composeStatus"`
		Deployments   []dokployDeploy `json:"deployments"`
	}
	if err := c.call(ctx, http.MethodGet, "compose.one", url.Values{"composeId": {composeID}}, &compose); err != nil {
		return nil, err
	}
	last := newestDeploy(compose.Deployments)
	return &AppState{AppName: compose.AppName, Status: compose.ComposeStatus, LastDeployTitle: last.Title, LastDeployStatus: last.Status}, nil
}

func (c *dokployHTTPClient) AddDomain(ctx context.Context, spec DomainSpec) (string, error) {
	input := map[string]any{
		"host": spec.Host, "path": "/", "port": spec.Port, "https": true, "certificateType": "letsencrypt",
	}
	if spec.ComposeID != "" {
		input["domainType"] = "compose"
		input["composeId"] = spec.ComposeID
		input["serviceName"] = spec.ServiceName
	} else {
		input["domainType"] = "application"
		input["applicationId"] = spec.AppID
	}
	var created struct {
		DomainID string `json:"domainId"`
	}
	if err := c.call(ctx, http.MethodPost, "domain.create", input, &created); err != nil {
		return "", err
	}
	return created.DomainID, nil
}

func (c *dokployHTTPClient) RemoveDomain(ctx context.Context, domainID string) error {
	return c.call(ctx, http.MethodPost, "domain.delete", map[string]any{"domainId": domainID}, nil)
}

// FormatEnv renders variables in the dotenv format Dokploy stores, sorted for
// stable diffs. Values are always double-quoted and escaped.
func FormatEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "$", `\$`)
	for _, key := range keys {
		out.WriteString(key)
		out.WriteString(`="`)
		out.WriteString(replacer.Replace(env[key]))
		out.WriteString("\"\n")
	}
	return out.String()
}
