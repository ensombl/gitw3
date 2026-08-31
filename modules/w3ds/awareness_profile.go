// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"forgejo.org/modules/json"
)

const (
	maxAwarenessPages = 100
	maxAwarenessBody  = 4 << 20
)

// PersonProfile is the subset of a W3DS User profile used by GitW3.
type PersonProfile struct {
	DisplayName string
	AvatarURL   string
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
		query.Set("limit", "200")
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
			profile = &PersonProfile{
				DisplayName: firstString(packet.Data, "displayName", "name", "username"),
				AvatarURL:   firstString(packet.Data, "avatarUrl", "avatar"),
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
