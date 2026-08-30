// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strings"

	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
	"forgejo.org/services/context"
)

type w3dsDomainOption struct {
	ID          string
	Label       string
	Description string
	Selected    bool
}

func preparePlatformDomains(ctx *context.Context, selected []string) (*w3ds.DomainCatalog, error) {
	client := &http.Client{Timeout: setting.PlatformManifestSync.Timeout}
	catalog, err := w3ds.FetchDomains(ctx, client, setting.PlatformManifestSync.OntologyURL)
	if err != nil {
		ctx.Data["DomainOntologyUnavailable"] = true
		return nil, err
	}
	selectedSet := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[strings.TrimSpace(id)] = struct{}{}
	}
	options := make([]w3dsDomainOption, 0, len(catalog.Domains))
	for _, domain := range catalog.Domains {
		_, isSelected := selectedSet[domain.ID]
		options = append(options, w3dsDomainOption{
			ID:          domain.ID,
			Label:       domain.Label,
			Description: domain.Description,
			Selected:    isSelected,
		})
	}
	ctx.Data["PlatformDomains"] = options
	ctx.Data["DomainOntologySchemaID"] = catalog.SchemaID
	return catalog, nil
}
