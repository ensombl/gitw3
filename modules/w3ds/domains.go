// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const ProductionOntologyURL = "https://ontology.w3ds.metastate.foundation"

// Domain is an application domain from the published W3DS domain ontology.
type Domain struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// DomainCatalog is the published domain ontology response.
type DomainCatalog struct {
	SchemaID string   `json:"schemaId"`
	Domains  []Domain `json:"domains"`
}

// FetchDomains loads the authoritative application domains from the ontology service.
func FetchDomains(ctx context.Context, client *http.Client, baseURL string) (*DomainCatalog, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("domain ontology URL must be absolute")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/domains", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("load domain ontology: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("load domain ontology: unexpected HTTP status %d", response.StatusCode)
	}
	var catalog DomainCatalog
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode domain ontology: %w", err)
	}
	if strings.TrimSpace(catalog.SchemaID) == "" || len(catalog.Domains) == 0 {
		return nil, errors.New("domain ontology is empty")
	}
	seen := make(map[string]struct{}, len(catalog.Domains))
	for i := range catalog.Domains {
		domain := &catalog.Domains[i]
		domain.ID = strings.TrimSpace(domain.ID)
		domain.Label = strings.TrimSpace(domain.Label)
		domain.Description = strings.TrimSpace(domain.Description)
		if !domainIDPattern.MatchString(domain.ID) || domain.Label == "" {
			return nil, fmt.Errorf("domain ontology contains invalid domain %q", domain.ID)
		}
		if _, exists := seen[domain.ID]; exists {
			return nil, fmt.Errorf("domain ontology contains duplicate domain %q", domain.ID)
		}
		seen[domain.ID] = struct{}{}
	}
	return &catalog, nil
}

// ValidateSelectedDomains verifies selections against a fetched ontology catalog.
func ValidateSelectedDomains(selected []string, catalog *DomainCatalog) error {
	if catalog == nil {
		return errors.New("domain ontology is required")
	}
	if len(selected) == 0 {
		return errors.New("select at least one application domain")
	}
	known := make(map[string]struct{}, len(catalog.Domains))
	for _, domain := range catalog.Domains {
		known[domain.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		id = strings.TrimSpace(id)
		if _, ok := known[id]; !ok {
			return fmt.Errorf("application domain %q is not in the published ontology", id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("application domain %q was selected more than once", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
