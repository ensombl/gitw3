// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import "time"

const productionAwarenessURL = "https://aaas.w3ds.metastate.foundation"

// W3DSIdentity controls profile enrichment from Awareness-as-a-Service.
var W3DSIdentity = struct {
	AwarenessURL          string
	AwarenessAPIKey       string
	AvatarAllowedHostList string
	Timeout               time.Duration
}{
	AwarenessURL:          productionAwarenessURL,
	AvatarAllowedHostList: "external",
	Timeout:               5 * time.Second,
}

// W3DSAllowAlternativeAuthenticationForTests keeps Forgejo's upstream login
// flows available to the test suite. Real GitW3 processes always enforce W3DS
// as the sole interactive web authentication method.
var W3DSAllowAlternativeAuthenticationForTests bool

func W3DSOnlyAuthenticationEnabled() bool {
	return !IsInTesting || !W3DSAllowAlternativeAuthenticationForTests
}

func loadW3DSIdentityFrom(rootCfg ConfigProvider) {
	section := rootCfg.Section("w3ds_identity")
	W3DSIdentity.AwarenessURL = section.Key("AWARENESS_URL").MustString(productionAwarenessURL)
	W3DSIdentity.AwarenessAPIKey = section.Key("AWARENESS_API_KEY").MustString("")
	W3DSIdentity.AvatarAllowedHostList = section.Key("AVATAR_ALLOWED_HOST_LIST").MustString("external")
	W3DSIdentity.Timeout = section.Key("TIMEOUT").MustDuration(5 * time.Second)
}
