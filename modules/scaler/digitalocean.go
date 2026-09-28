// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forgejo.org/modules/json"
)

// DropletSpec is what the scaler asks DigitalOcean for.
type DropletSpec struct {
	Name     string
	Region   string
	Size     string
	Image    string
	VPCUUID  string
	Tags     []string
	SSHKeys  []string
	UserData string
}

// Droplet is the subset of a DigitalOcean droplet the scaler uses.
type Droplet struct {
	ID        string
	Name      string
	Status    string // new, active, off, archive
	PrivateIP string
	CreatedAt time.Time
}

// ErrDropletNotFound is returned for deleted droplets.
var ErrDropletNotFound = errors.New("droplet not found")

// DOClient is the subset of the DigitalOcean API the scaler needs; its
// token only needs droplet create/delete and tag/VPC read scopes.
type DOClient interface {
	CreateDroplet(ctx context.Context, spec DropletSpec) (string, error)
	GetDroplet(ctx context.Context, id string) (*Droplet, error)
	DeleteDroplet(ctx context.Context, id string) error
	ListByTag(ctx context.Context, tag string) ([]Droplet, error)
}

// DigitalOcean is the REST client.
type DigitalOcean struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewDigitalOcean(token string, timeout time.Duration) *DigitalOcean {
	return &DigitalOcean{baseURL: "https://api.digitalocean.com/v2", token: token, http: &http.Client{Timeout: timeout}}
}

type apiDroplet struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	Networks  struct {
		V4 []struct {
			IPAddress string `json:"ip_address"`
			Type      string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

func (d apiDroplet) toDroplet() Droplet {
	droplet := Droplet{ID: strconv.FormatInt(d.ID, 10), Name: d.Name, Status: d.Status, CreatedAt: d.CreatedAt}
	for _, network := range d.Networks.V4 {
		if network.Type == "private" {
			droplet.PrivateIP = network.IPAddress
		}
	}
	return droplet
}

func (d *DigitalOcean) do(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := d.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return response.StatusCode, fmt.Errorf("digitalocean %s %s: HTTP %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil {
		return response.StatusCode, nil
	}
	return response.StatusCode, json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(output)
}

func (d *DigitalOcean) CreateDroplet(ctx context.Context, spec DropletSpec) (string, error) {
	keys := make([]any, 0, len(spec.SSHKeys))
	for _, key := range spec.SSHKeys {
		keys = append(keys, key)
	}
	var created struct {
		Droplet apiDroplet `json:"droplet"`
	}
	_, err := d.do(ctx, http.MethodPost, "/droplets", map[string]any{
		"name": spec.Name, "region": spec.Region, "size": spec.Size, "image": spec.Image,
		"vpc_uuid": spec.VPCUUID, "tags": spec.Tags, "ssh_keys": keys, "user_data": spec.UserData,
		"monitoring": true, "ipv6": false,
	}, &created)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(created.Droplet.ID, 10), nil
}

func (d *DigitalOcean) GetDroplet(ctx context.Context, id string) (*Droplet, error) {
	var result struct {
		Droplet apiDroplet `json:"droplet"`
	}
	status, err := d.do(ctx, http.MethodGet, "/droplets/"+url.PathEscape(id), nil, &result)
	if status == http.StatusNotFound {
		return nil, ErrDropletNotFound
	}
	if err != nil {
		return nil, err
	}
	droplet := result.Droplet.toDroplet()
	return &droplet, nil
}

func (d *DigitalOcean) DeleteDroplet(ctx context.Context, id string) error {
	status, err := d.do(ctx, http.MethodDelete, "/droplets/"+url.PathEscape(id), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (d *DigitalOcean) ListByTag(ctx context.Context, tag string) ([]Droplet, error) {
	droplets := make([]Droplet, 0, 8)
	for page := 1; ; page++ {
		var result struct {
			Droplets []apiDroplet `json:"droplets"`
			Links    struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		query := url.Values{"tag_name": {tag}, "per_page": {"200"}, "page": {strconv.Itoa(page)}}
		if _, err := d.do(ctx, http.MethodGet, "/droplets?"+query.Encode(), nil, &result); err != nil {
			return nil, err
		}
		for _, droplet := range result.Droplets {
			droplets = append(droplets, droplet.toDroplet())
		}
		if result.Links.Pages.Next == "" {
			return droplets, nil
		}
	}
}
