// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var ErrPublicationStatusNotFound = errors.New("platform publication status not found")

// AccreditationDecision is the newest PPA decision for one platform version.
type AccreditationDecision struct {
	PlatformEName   string `json:"platformEName"`
	PlatformVersion string `json:"platformVersion"`
	Decision        string `json:"decision"`
	Level           string `json:"level,omitempty"`
	CreatedAt       string `json:"createdAt"`
}

// PublicationStatus is the safe subset of publisher state shown on repository pages.
type PublicationStatus struct {
	Status            string                 `json:"status"`
	EName             string                 `json:"ename"`
	ReleaseTag        string                 `json:"releaseTag"`
	ReleaseVersion    string                 `json:"releaseVersion"`
	LastError         string                 `json:"lastError"`
	Attempts          int                    `json:"attempts"`
	Decision          *AccreditationDecision `json:"decision,omitempty"`
	DecisionCheckedAt string                 `json:"decisionCheckedAt,omitempty"`
}

// FetchPublicationStatus loads state without exposing the internal service token to browsers.
func FetchPublicationStatus(ctx context.Context, client *http.Client, serviceURL, token string, repositoryID int64) (*PublicationStatus, error) {
	endpoint := strings.TrimRight(serviceURL, "/") + "/api/v1/status/" + strconv.FormatInt(repositoryID, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrPublicationStatusNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform publication status returned %d", response.StatusCode)
	}
	var status PublicationStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}
