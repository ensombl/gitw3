// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import "time"

// PlatformManifestSync controls the optional repository-page publication status integration.
var PlatformManifestSync = struct {
	Enabled       bool
	URL           string
	InternalToken string
	Timeout       time.Duration
}{
	Timeout: 2 * time.Second,
}

func loadPlatformManifestSyncFrom(rootCfg ConfigProvider) {
	section := rootCfg.Section("platform_manifest_sync")
	PlatformManifestSync.Enabled = section.Key("ENABLED").MustBool(false)
	PlatformManifestSync.URL = section.Key("URL").MustString("")
	PlatformManifestSync.InternalToken = section.Key("INTERNAL_TOKEN").MustString("")
	PlatformManifestSync.Timeout = section.Key("TIMEOUT").MustDuration(2 * time.Second)
}
