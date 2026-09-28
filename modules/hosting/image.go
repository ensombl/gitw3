// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"regexp"
	"strings"
)

var imageNameInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// ImageName derives the registry image name for a target inside the
// deployments org, e.g. "alice-shop-web". Compose services append the
// service name so every built image of a stack is distinct.
func ImageName(owner, repo, target string, service ...string) string {
	parts := []string{owner, repo, target}
	parts = append(parts, service...)
	for i, part := range parts {
		parts[i] = strings.Trim(imageNameInvalid.ReplaceAllString(strings.ToLower(part), "-"), "-")
	}
	return strings.Join(parts, "-")
}

// ImageRepository is the full repository path, e.g. "git.example.com/deployments/alice-shop-web".
func ImageRepository(registryHost, registryOwner, image string) string {
	return registryHost + "/" + strings.ToLower(registryOwner) + "/" + image
}

// DigestReference pins an image repository to a digest.
func DigestReference(repository, digest string) string {
	return repository + "@" + digest
}
