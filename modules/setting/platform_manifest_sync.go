// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"time"

	"forgejo.org/modules/w3ds"
)

// PlatformManifestSync controls the optional repository-page publication status integration.
var PlatformManifestSync = struct {
	Enabled       bool
	URL           string
	InternalToken string
	OntologyURL   string
	Timeout       time.Duration
}{
	OntologyURL: w3ds.ProductionOntologyURL,
	Timeout:     2 * time.Second,
}

func loadPlatformManifestSyncFrom(rootCfg ConfigProvider) {
	section := rootCfg.Section("platform_manifest_sync")
	PlatformManifestSync.Enabled = section.Key("ENABLED").MustBool(false)
	PlatformManifestSync.URL = section.Key("URL").MustString("")
	PlatformManifestSync.InternalToken = section.Key("INTERNAL_TOKEN").MustString("")
	PlatformManifestSync.OntologyURL = section.Key("ONTOLOGY_URL").MustString(w3ds.ProductionOntologyURL)
	PlatformManifestSync.Timeout = section.Key("TIMEOUT").MustDuration(2 * time.Second)
}
