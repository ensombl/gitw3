// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Compose keys that swarm stack mode ignores or that would let a workload
// escape its container. They are rejected at validation time so a compose
// file never fails half-way through a deploy.
var rejectedServiceKeys = map[string]string{
	"privileged":     "privileged containers are not allowed",
	"network_mode":   "network_mode is not supported in stack mode",
	"pid":            "pid namespaces are not allowed",
	"ipc":            "ipc namespaces are not allowed",
	"devices":        "host devices are not allowed",
	"cap_add":        "adding capabilities is not allowed",
	"security_opt":   "security_opt is not allowed",
	"userns_mode":    "userns_mode is not allowed",
	"cgroup_parent":  "cgroup_parent is not allowed",
	"container_name": "container_name is not supported in stack mode",
	"links":          "links are not supported in stack mode, use service names",
	"extends":        "extends is not supported, inline the service",
	"external_links": "external_links are not supported in stack mode",
	"volumes_from":   "volumes_from is not supported in stack mode",
	"volumes":        "volumes are not supported: managed hosting runs stateless services, use a managed database for state",
	"env_file":       "env_file is not supported, set environment variables in GitW3",
	"secrets":        "compose secrets are not supported, set secret environment variables in GitW3",
	"configs":        "compose configs are not supported",
}

var rejectedTopLevelKeys = map[string]string{
	"volumes": "top-level volumes are not supported: managed hosting runs stateless services",
	"secrets": "compose secrets are not supported, set secret environment variables in GitW3",
	"configs": "compose configs are not supported",
}

var serviceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ComposeBuild is one service the builder has to build and push.
type ComposeBuild struct {
	Service    string `json:"service"`
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}

// ComposeFile is a validated compose document.
type ComposeFile struct {
	root     map[string]any
	services map[string]map[string]any
}

// ParseCompose parses and validates a compose document for swarm stack mode.
// dir is the compose file's directory inside the repository; build contexts
// are resolved against it.
func ParseCompose(content []byte, dir string) (*ComposeFile, []ComposeBuild, error) {
	root := map[string]any{}
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, nil, fmt.Errorf("compose: %w", err)
	}
	for key, reason := range rejectedTopLevelKeys {
		if _, ok := root[key]; ok {
			return nil, nil, fmt.Errorf("compose: %s", reason)
		}
	}
	rawServices, ok := root["services"].(map[string]any)
	if !ok || len(rawServices) == 0 {
		return nil, nil, errors.New("compose: at least one service is required")
	}
	file := &ComposeFile{root: root, services: make(map[string]map[string]any, len(rawServices))}
	builds := make([]ComposeBuild, 0, len(rawServices))
	for name, raw := range rawServices {
		if !serviceNamePattern.MatchString(name) {
			return nil, nil, fmt.Errorf("compose: invalid service name %q", name)
		}
		service, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("compose: service %q must be a mapping", name)
		}
		for key, reason := range rejectedServiceKeys {
			if _, ok := service[key]; ok {
				return nil, nil, fmt.Errorf("compose: service %q: %s", name, reason)
			}
		}
		if err := validatePorts(name, service["ports"]); err != nil {
			return nil, nil, err
		}
		if deploy, ok := service["deploy"].(map[string]any); ok {
			if mode, _ := deploy["mode"].(string); mode == "global" {
				return nil, nil, fmt.Errorf("compose: service %q: global mode is not allowed on autoscaled workers", name)
			}
			if _, ok := deploy["placement"]; ok {
				return nil, nil, fmt.Errorf("compose: service %q: placement constraints are managed by GitW3", name)
			}
		}
		build, hasBuild := service["build"]
		_, hasImage := service["image"]
		switch {
		case hasBuild:
			spec, err := parseBuild(name, build, dir)
			if err != nil {
				return nil, nil, err
			}
			builds = append(builds, spec)
		case !hasImage:
			return nil, nil, fmt.Errorf("compose: service %q needs either build or image", name)
		}
		file.services[name] = service
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Service < builds[j].Service })
	return file, builds, nil
}

func parseBuild(service string, build any, dir string) (ComposeBuild, error) {
	spec := ComposeBuild{Service: service, Dockerfile: "Dockerfile"}
	context := "."
	switch value := build.(type) {
	case string:
		context = value
	case map[string]any:
		for key := range value {
			if !slices.Contains([]string{"context", "dockerfile", "args", "target"}, key) {
				return spec, fmt.Errorf("compose: service %q: build.%s is not supported", service, key)
			}
		}
		if raw, ok := value["context"].(string); ok {
			context = raw
		}
		if raw, ok := value["dockerfile"].(string); ok {
			spec.Dockerfile = raw
		}
	default:
		return spec, fmt.Errorf("compose: service %q: invalid build", service)
	}
	if strings.Contains(context, "://") {
		return spec, fmt.Errorf("compose: service %q: remote build contexts are not allowed", service)
	}
	joined, err := CleanRepoPath(strings.TrimPrefix(dir+"/"+context, "./"))
	if err != nil {
		return spec, fmt.Errorf("compose: service %q: build context %w", service, err)
	}
	spec.Context = joined
	dockerfile, err := CleanRepoPath(spec.Dockerfile)
	if err != nil {
		return spec, fmt.Errorf("compose: service %q: dockerfile %w", service, err)
	}
	spec.Dockerfile = dockerfile
	return spec, nil
}

func validatePorts(service string, ports any) error {
	if ports == nil {
		return nil
	}
	list, ok := ports.([]any)
	if !ok {
		return fmt.Errorf("compose: service %q: ports must be a list", service)
	}
	if len(list) > 0 {
		// Published ports would bypass Traefik and collide across apps; the
		// public port of a compose app is declared by the target's service and port.
		return fmt.Errorf("compose: service %q: published ports are not allowed, route HTTP with the target service and port in deploy.yml", service)
	}
	return nil
}

// Services returns the sorted service names.
func (c *ComposeFile) Services() []string {
	names := make([]string, 0, len(c.services))
	for name := range c.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PinImages replaces every built service's build section with its
// digest-pinned image reference and returns the rendered document. This runs
// inside GitW3 rather than on the untrusted builder, so the builder can never
// choose which images end up in a stack.
func (c *ComposeFile) PinImages(images map[string]string) ([]byte, error) {
	for name, service := range c.services {
		if _, ok := service["build"]; !ok {
			continue
		}
		ref, ok := images[name]
		if !ok || !IsDigestReference(ref) {
			return nil, fmt.Errorf("compose: no digest-pinned image for service %q", name)
		}
		delete(service, "build")
		service["image"] = ref
	}
	return yaml.Marshal(c.root)
}

var digestReferencePattern = regexp.MustCompile(`^[a-z0-9.:\-/_]+@sha256:[a-f0-9]{64}$`)

// IsDigestReference reports whether ref pins an image by sha256 digest.
func IsDigestReference(ref string) bool {
	return digestReferencePattern.MatchString(ref)
}

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// IsDigest reports whether value is a bare sha256 digest.
func IsDigest(value string) bool {
	return digestPattern.MatchString(value)
}
