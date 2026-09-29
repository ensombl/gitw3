// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"forgejo.org/modules/json"
)

const (
	maxAwarenessPages = 100
	// Profiles often embed their photo as a data URI, so pages are small
	// and the body limit generous.
	awarenessPageSize = 50
	maxAwarenessBody  = 8 << 20
	// MaxAvatarSourceLength bounds an avatar URL or data URI (about 3 MiB of image).
	MaxAvatarSourceLength = 4 << 20
)

// PersonProfile is the subset of a W3DS User profile used by GitW3.
type PersonProfile struct {
	DisplayName string
	// AvatarURL is the newest photo GitW3 can load: an absolute http(s) URL
	// or a data:image URI. Relative paths are skipped; they point at
	// whichever platform wrote the profile, which the profile does not say.
	AvatarURL string
}

// UsableAvatar reports whether GitW3 can load an avatar source.
func UsableAvatar(source string) bool {
	if source == "" || len(source) > MaxAvatarSourceLength {
		return false
	}
	if _, ok := DecodeDataImage(source); ok {
		return true
	}
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// DecodeDataImage decodes a base64 data:image URI.
func DecodeDataImage(source string) ([]byte, bool) {
	rest, found := strings.CutPrefix(source, "data:")
	if !found {
		return nil, false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return nil, false
	}
	mediaType, encoding, _ := strings.Cut(meta, ";")
	switch strings.ToLower(mediaType) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
	default:
		return nil, false
	}
	if !strings.EqualFold(encoding, "base64") {
		return nil, false
	}
	payload = strings.TrimSpace(payload)
	for _, encoder := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := encoder.DecodeString(payload); err == nil && len(data) > 0 {
			return data, true
		}
	}
	return nil, false
}

type awarenessPacket struct {
	Data map[string]any `json:"data"`
}

type awarenessPacketsResponse struct {
	Packets    []awarenessPacket `json:"packets"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor"`
}

// FetchPersonProfile reads the newest non-platform User profile for an eName.
// AaaS returns packets oldest first, so later person packets replace earlier
// ones. Platform profiles share the ontology and are deliberately ignored.
func FetchPersonProfile(ctx context.Context, client *http.Client, baseURL, apiKey, ename string) (*PersonProfile, error) {
	if client == nil {
		return nil, errors.New("an HTTP client is required")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if baseURL == "" || apiKey == "" {
		return nil, errors.New("AaaS URL and API key are required")
	}

	ename = strings.TrimSpace(ename)
	if ename == "" {
		return nil, errors.New("an eName is required")
	}
	if !strings.HasPrefix(ename, "@") {
		ename = "@" + ename
	}

	var profile *PersonProfile
	var cursor string
	for range maxAwarenessPages {
		endpoint, err := url.Parse(baseURL + "/api/packets")
		if err != nil {
			return nil, fmt.Errorf("parse AaaS URL: %w", err)
		}
		query := endpoint.Query()
		query.Set("limit", strconv.Itoa(awarenessPageSize))
		query.Set("evault", ename)
		query.Set("ontology", UserProfileOntology)
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		endpoint.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create AaaS request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("read AaaS profile: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAwarenessBody+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read AaaS response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close AaaS response: %w", closeErr)
		}
		if len(body) > maxAwarenessBody {
			return nil, errors.New("AaaS response is too large")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("AaaS returned HTTP %d", resp.StatusCode)
		}

		var result awarenessPacketsResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode AaaS response: %w", err)
		}
		for _, packet := range result.Packets {
			if packet.Data == nil || stringField(packet.Data, "platformName") != "" {
				continue
			}
			if profile == nil {
				profile = &PersonProfile{}
			}
			if name := firstString(packet.Data, "displayName", "name", "username"); name != "" {
				profile.DisplayName = name
			}
			// A later update with an unusable photo keeps the last usable one.
			if avatar := firstString(packet.Data, "avatarUrl", "avatar"); UsableAvatar(avatar) {
				profile.AvatarURL = avatar
			}
		}

		if !result.HasMore {
			return profile, nil
		}
		if result.NextCursor == "" || result.NextCursor == cursor {
			return nil, errors.New("AaaS pagination did not advance")
		}
		cursor = result.NextCursor
	}

	return nil, errors.New("AaaS pagination exceeded its safety limit")
}

func firstString(data map[string]any, fields ...string) string {
	for _, field := range fields {
		if value := stringField(data, field); value != "" {
			return value
		}
	}
	return ""
}

func stringField(data map[string]any, field string) string {
	value, ok := data[field].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
