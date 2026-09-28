// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hosting holds the pure helpers behind GitW3 managed hosting: repo
// deploy config, compose validation, domain names and build callback signing.
package hosting

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ConfigPath is where a repository describes its managed deploy targets.
const ConfigPath = ".forgejo/deploy.yml"

// Target kinds.
const (
	KindDockerfile = "dockerfile"
	KindCompose    = "compose"
)

const (
	DefaultTargetName = "web"
	DefaultPort       = 3000
	MaxReplicas       = 20
	MaxTargets        = 10
)

var targetNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Config is the parsed `.forgejo/deploy.yml`.
type Config struct {
	Version int       `yaml:"version" json:"version"`
	Targets []*Target `yaml:"targets" json:"targets"`
}

// Target is one deployable unit of a repository.
type Target struct {
	Name       string `yaml:"name" json:"name"`
	Kind       string `yaml:"kind" json:"kind"`
	Dockerfile string `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	Context    string `yaml:"context,omitempty" json:"context,omitempty"`
	Compose    string `yaml:"compose,omitempty" json:"compose,omitempty"`
	// Service is the compose service that receives public HTTP traffic on Port.
	Service     string       `yaml:"service,omitempty" json:"service,omitempty"`
	Port        int          `yaml:"port,omitempty" json:"port,omitempty"`
	Replicas    int          `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Domain      string       `yaml:"domain,omitempty" json:"domain,omitempty"`
	Healthcheck *Healthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Resources   Resources    `yaml:"resources,omitempty" json:"resources"`
	AutoDeploy  bool         `yaml:"auto_deploy,omitempty" json:"autoDeploy"`
	// Branch is accepted for compatibility with the original design; GitW3
	// only deploys tagged releases so it has no effect.
	Branch string `yaml:"branch,omitempty" json:"-"`
}

// Healthcheck configures the HTTP probe Swarm uses to gate rolling updates.
type Healthcheck struct {
	Path     string `yaml:"path" json:"path"`
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Resources are Swarm limits for each replica.
type Resources struct {
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// ParseConfig decodes and validates a deploy.yml document.
func ParseConfig(content []byte) (*Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	config := &Config{}
	if err := decoder.Decode(config); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigPath, err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// DefaultConfig is used when a repository has a Dockerfile but no deploy.yml,
// so a first deploy needs no configuration at all.
func DefaultConfig(dockerfile []byte) *Config {
	target := &Target{
		Name:       DefaultTargetName,
		Kind:       KindDockerfile,
		Dockerfile: "Dockerfile",
		Context:    ".",
		Port:       ExposedPort(dockerfile),
		Replicas:   1,
	}
	return &Config{Version: 1, Targets: []*Target{target}}
}

var exposePattern = regexp.MustCompile(`(?im)^\s*EXPOSE\s+(\d{1,5})`)

// ExposedPort returns the first EXPOSEd port of a Dockerfile, or DefaultPort.
func ExposedPort(dockerfile []byte) int {
	match := exposePattern.FindSubmatch(dockerfile)
	if match == nil {
		return DefaultPort
	}
	port, err := strconv.Atoi(string(match[1]))
	if err != nil || port < 1 || port > 65535 {
		return DefaultPort
	}
	return port
}

// Target returns the named target or nil.
func (c *Config) Target(name string) *Target {
	for _, target := range c.Targets {
		if target.Name == name {
			return target
		}
	}
	return nil
}

// Validate checks the whole config and fills defaults in place.
func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("%s: unsupported version %d", ConfigPath, c.Version)
	}
	if len(c.Targets) == 0 {
		return fmt.Errorf("%s: at least one target is required", ConfigPath)
	}
	if len(c.Targets) > MaxTargets {
		return fmt.Errorf("%s: at most %d targets are allowed", ConfigPath, MaxTargets)
	}
	seen := make(map[string]bool, len(c.Targets))
	for _, target := range c.Targets {
		if target == nil {
			return fmt.Errorf("%s: empty target", ConfigPath)
		}
		if err := target.Validate(); err != nil {
			return fmt.Errorf("%s: target %q: %w", ConfigPath, target.Name, err)
		}
		if seen[target.Name] {
			return fmt.Errorf("%s: duplicate target %q", ConfigPath, target.Name)
		}
		seen[target.Name] = true
	}
	return nil
}

// Validate checks one target and fills defaults in place.
func (t *Target) Validate() error {
	if !targetNamePattern.MatchString(t.Name) {
		return errors.New("name must be lowercase letters, digits and dashes (max 32)")
	}
	if t.Replicas == 0 {
		t.Replicas = 1
	}
	if t.Replicas < 1 || t.Replicas > MaxReplicas {
		return fmt.Errorf("replicas must be between 1 and %d", MaxReplicas)
	}
	switch t.Kind {
	case KindDockerfile:
		if t.Compose != "" || t.Service != "" {
			return errors.New("compose and service are only valid for compose targets")
		}
		if t.Dockerfile == "" {
			t.Dockerfile = "Dockerfile"
		}
		if t.Context == "" {
			t.Context = "."
		}
		var err error
		if t.Dockerfile, err = CleanRepoPath(t.Dockerfile); err != nil {
			return fmt.Errorf("dockerfile: %w", err)
		}
		if t.Context, err = CleanRepoPath(t.Context); err != nil {
			return fmt.Errorf("context: %w", err)
		}
		if t.Port == 0 {
			t.Port = DefaultPort
		}
		if t.Port < 1 || t.Port > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	case KindCompose:
		if t.Dockerfile != "" || t.Healthcheck != nil {
			return errors.New("dockerfile and healthcheck belong in the compose file for compose targets")
		}
		if (t.Service == "") != (t.Port == 0) {
			return errors.New("compose targets set service and port together to receive public traffic")
		}
		if t.Port < 0 || t.Port > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
		if t.Compose == "" {
			return errors.New("compose path is required")
		}
		var err error
		if t.Compose, err = CleanRepoPath(t.Compose); err != nil {
			return fmt.Errorf("compose: %w", err)
		}
		if t.Context == "" {
			t.Context = path.Dir(t.Compose)
		}
		if t.Context, err = CleanRepoPath(t.Context); err != nil {
			return fmt.Errorf("context: %w", err)
		}
	default:
		return fmt.Errorf("kind must be %q or %q", KindDockerfile, KindCompose)
	}
	if t.Domain != "" {
		domain, err := NormalizeDomain(t.Domain)
		if err != nil {
			return err
		}
		t.Domain = domain
	}
	if t.Healthcheck != nil {
		if !strings.HasPrefix(t.Healthcheck.Path, "/") {
			return errors.New("healthcheck path must start with /")
		}
		for _, value := range []string{t.Healthcheck.Interval, t.Healthcheck.Timeout} {
			if value == "" {
				continue
			}
			if duration, err := time.ParseDuration(value); err != nil || duration <= 0 {
				return fmt.Errorf("invalid healthcheck duration %q", value)
			}
		}
	}
	if _, err := t.Resources.NanoCPUs(); err != nil {
		return err
	}
	if _, err := t.Resources.MemoryBytes(); err != nil {
		return err
	}
	return nil
}

// CleanRepoPath normalises a path inside the repository and refuses escapes.
func CleanRepoPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("must be a relative path inside the repository")
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("must stay inside the repository")
	}
	return cleaned, nil
}

// NanoCPUs converts the CPU limit to Docker's NanoCPUs (0 means unlimited).
func (r Resources) NanoCPUs() (int64, error) {
	if r.CPU == "" {
		return 0, nil
	}
	cpu, err := strconv.ParseFloat(r.CPU, 64)
	if err != nil || cpu <= 0 || cpu > 16 {
		return 0, fmt.Errorf("invalid cpu limit %q", r.CPU)
	}
	return int64(cpu * 1e9), nil
}

// MemoryBytes converts the memory limit (e.g. 512M, 1G) to bytes (0 means unlimited).
func (r Resources) MemoryBytes() (int64, error) {
	value := strings.ToUpper(strings.TrimSpace(r.Memory))
	if value == "" {
		return 0, nil
	}
	value = strings.TrimSuffix(value, "B")
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(value, "K"):
		multiplier, value = 1<<10, strings.TrimSuffix(value, "K")
	case strings.HasSuffix(value, "M"):
		multiplier, value = 1<<20, strings.TrimSuffix(value, "M")
	case strings.HasSuffix(value, "G"):
		multiplier, value = 1<<30, strings.TrimSuffix(value, "G")
	}
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid memory limit %q", r.Memory)
	}
	bytes := amount * multiplier
	if bytes < 16<<20 {
		return 0, fmt.Errorf("memory limit %q is below 16M", r.Memory)
	}
	return bytes, nil
}

var domainLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeDomain lowercases and validates a DNS host name.
func NormalizeDomain(value string) (string, error) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(domain) == 0 || len(domain) > 253 {
		return "", fmt.Errorf("invalid domain %q", value)
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("domain %q must include a top-level domain", value)
	}
	for _, label := range labels {
		if !domainLabelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid domain %q", value)
		}
	}
	return domain, nil
}
