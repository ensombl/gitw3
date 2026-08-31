// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"strings"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const maxPlatformSlugLength = 100

// SlugFromDisplayName returns the lowercase dash-separated identifier used for
// both a platform's repository and its initial stable platform name.
func SlugFromDisplayName(displayName string) string {
	displayName = strings.NewReplacer("Æ", "AE", "æ", "ae", "Ø", "O", "ø", "o", "ß", "ss").Replace(displayName)
	normalized, _, err := transform.String(norm.NFD, displayName)
	if err != nil {
		normalized = displayName
	}

	var slug strings.Builder
	separatorPending := false
	for _, r := range strings.ToLower(normalized) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if separatorPending && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(r)
			separatorPending = false
			continue
		}
		separatorPending = slug.Len() > 0
	}

	value := strings.Trim(slug.String(), "-")
	if value == "" {
		value = "platform"
	}
	if len(value) > maxPlatformSlugLength {
		value = strings.TrimRight(value[:maxPlatformSlugLength], "-")
	}
	return value
}
