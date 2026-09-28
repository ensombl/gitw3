// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"forgejo.org/modules/setting"
)

// DNSProvider creates the records behind pool domains.
type DNSProvider interface {
	// CreateRecord points fqdn at the ingress and returns a provider record ID
	// (empty when the provider has nothing to track).
	CreateRecord(ctx context.Context, fqdn string) (string, error)
	DeleteRecord(ctx context.Context, recordID string) error
}

// NewDNSProvider returns the configured provider.
func NewDNSProvider() DNSProvider {
	switch setting.Hosting.Domains.Provider {
	case setting.HostingDNSProviderCloudflare:
		return &cloudflareDNS{
			baseURL:  "https://api.cloudflare.com/client/v4",
			token:    setting.Hosting.Domains.CloudflareAPIToken,
			zoneID:   setting.Hosting.Domains.CloudflareZoneID,
			targetIP: setting.Hosting.Domains.TargetIP,
			proxied:  setting.Hosting.Domains.Proxied,
			http:     &http.Client{Timeout: setting.Hosting.HTTPTimeout},
		}
	default:
		return wildcardDNS{}
	}
}

// wildcardDNS is used when *.BASE_DOMAIN already points at the ingress, so
// every generated name resolves without any API call.
type wildcardDNS struct{}

func (wildcardDNS) CreateRecord(context.Context, string) (string, error) { return "", nil }
func (wildcardDNS) DeleteRecord(context.Context, string) error           { return nil }

type cloudflareDNS struct {
	baseURL  string
	token    string
	zoneID   string
	targetIP string
	proxied  bool
	http     *http.Client
}

type cloudflareResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (c *cloudflareDNS) do(ctx context.Context, method, path string, input any) (*cloudflareResponse, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err)
	}
	defer response.Body.Close()
	parsed := &cloudflareResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(parsed); err != nil {
		return nil, fmt.Errorf("cloudflare: HTTP %d: %w", response.StatusCode, err)
	}
	if !parsed.Success {
		messages := make([]string, 0, len(parsed.Errors))
		for _, e := range parsed.Errors {
			messages = append(messages, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return parsed, fmt.Errorf("cloudflare: HTTP %d: %s", response.StatusCode, strings.Join(messages, "; "))
	}
	return parsed, nil
}

func (c *cloudflareDNS) CreateRecord(ctx context.Context, fqdn string) (string, error) {
	response, err := c.do(ctx, http.MethodPost, "/zones/"+c.zoneID+"/dns_records", map[string]any{
		"type": "A", "name": fqdn, "content": c.targetIP, "ttl": 1, "proxied": c.proxied,
		"comment": "GitW3 managed hosting pool",
	})
	if err != nil {
		return "", err
	}
	var record struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Result, &record); err != nil || record.ID == "" {
		return "", fmt.Errorf("cloudflare: record for %s has no id", fqdn)
	}
	return record.ID, nil
}

func (c *cloudflareDNS) DeleteRecord(ctx context.Context, recordID string) error {
	if recordID == "" {
		return nil
	}
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+c.zoneID+"/dns_records/"+recordID, nil)
	return err
}
