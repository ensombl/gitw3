// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	PlatformManifestPath    = ".w3ds/platform.json"
	PlatformManifestVersion = 1
	UserProfileOntology     = "550e8400-e29b-41d4-a716-446655440000"
)

var (
	platformNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	semverPattern       = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	knownCategories     = map[string]struct{}{
		"Identity": {}, "Social": {}, "Governance": {}, "Wellness": {},
		"Finance": {}, "Storage": {}, "Productivity": {}, "Other": {},
	}
)

// PlatformManifest is the repository-owned source of truth for a W3DS platform.
type PlatformManifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	PlatformName  string  `json:"platformName"`
	DisplayName   string  `json:"displayName"`
	Description   string  `json:"description"`
	Version       string  `json:"version"`
	EName         *string `json:"ename"`
	URL           string  `json:"url"`
	LogoURL       string  `json:"logoUrl"`
	Category      string  `json:"category"`
	PublicKey     string  `json:"publicKey"`
}

// NewPlatformManifest creates a manifest whose eName will be filled by the publisher.
func NewPlatformManifest(platformName, displayName, description, version, appURL, logoURL, category, publicKey string) *PlatformManifest {
	return &PlatformManifest{
		SchemaVersion: PlatformManifestVersion,
		PlatformName:  strings.TrimSpace(platformName),
		DisplayName:   strings.TrimSpace(displayName),
		Description:   strings.TrimSpace(description),
		Version:       strings.TrimSpace(version),
		EName:         nil,
		URL:           strings.TrimSpace(appURL),
		LogoURL:       strings.TrimSpace(logoURL),
		Category:      strings.TrimSpace(category),
		PublicKey:     strings.TrimSpace(publicKey),
	}
}

// Validate checks the version-one repository contract.
func (m *PlatformManifest) Validate(allowLocalHTTP bool) error {
	if m == nil {
		return errors.New("platform manifest is required")
	}
	if m.SchemaVersion != PlatformManifestVersion {
		return fmt.Errorf("schemaVersion must be %d", PlatformManifestVersion)
	}
	if !platformNamePattern.MatchString(m.PlatformName) || len(m.PlatformName) > 100 {
		return errors.New("platformName must be a lowercase, dash-separated slug")
	}
	if m.DisplayName == "" || len(m.DisplayName) > 100 {
		return errors.New("displayName must contain between 1 and 100 characters")
	}
	if m.Description == "" || len(m.Description) > 2048 {
		return errors.New("description must contain between 1 and 2048 characters")
	}
	if !semverPattern.MatchString(m.Version) {
		return errors.New("version must be a semantic version such as 0.1.0")
	}
	if _, ok := knownCategories[m.Category]; !ok {
		return errors.New("category is not supported")
	}
	if !strings.HasPrefix(m.PublicKey, "z") || len(m.PublicKey) < 2 || len(m.PublicKey) > 8192 {
		return errors.New("publicKey must be a z-prefixed multibase key")
	}
	if err := validateWebURL(m.URL, false, allowLocalHTTP); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if m.LogoURL != "" {
		if err := validateWebURL(m.LogoURL, true, allowLocalHTTP); err != nil {
			return fmt.Errorf("logoUrl: %w", err)
		}
	}
	if m.EName != nil && strings.TrimSpace(*m.EName) == "" {
		return errors.New("ename must be null or a non-empty W3DS identifier")
	}
	return nil
}

func validateWebURL(value string, optional, allowLocalHTTP bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return errors.New("a public application URL is required")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if allowLocalHTTP && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
		return nil
	}
	return errors.New("must use HTTPS")
}

// Marshal returns stable, human-readable manifest JSON with a trailing newline.
func (m *PlatformManifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
