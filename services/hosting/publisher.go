// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

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

	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
)

// PublisherDeployment is the publisher sidecar's view of a W3DS deployment record.
type PublisherDeployment struct {
	ID                        string `json:"id"`
	Status                    string `json:"status"`
	DeploymentEName           string `json:"deploymentEName"`
	VersionEName              string `json:"versionEName"`
	BundlePayload             string `json:"bundlePayload"`
	DeploymentKeyDocumentID   string `json:"deploymentKeyDocumentId"`
	SoftwareVersionDocumentID string `json:"softwareVersionDocumentId"`
	LastError                 string `json:"lastError"`
}

// PublisherError is a non-2xx answer from the publisher.
type PublisherError struct {
	Status  int
	Message string
}

func (e *PublisherError) Error() string {
	return "publisher returned " + strconv.Itoa(e.Status) + ": " + e.Message
}

// IsCertificationRequired reports whether the publisher refused because the
// release is not PPA-certified (yet).
func IsCertificationRequired(err error) bool {
	var publisherErr *PublisherError
	return errors.As(err, &publisherErr) && strings.Contains(strings.ToLower(publisherErr.Message), "certification")
}

// IsVersionDenied reports whether the PPA denied the release being published.
func IsVersionDenied(err error) bool {
	var publisherErr *PublisherError
	return errors.As(err, &publisherErr) && strings.Contains(strings.ToLower(publisherErr.Message), "ppa denied")
}

// Publisher talks to the platform-manifest-sync sidecar that owns W3DS
// deployment records.
type Publisher interface {
	Call(ctx context.Context, method, path string, input, output any) error
}

type httpPublisher struct{}

// NewPublisher returns the configured publisher client.
func NewPublisher() Publisher {
	return httpPublisher{}
}

func (httpPublisher) Call(ctx context.Context, method, path string, input, output any) error {
	return CallPublisher(ctx, method, path, input, output)
}

// CallPublisher performs one authenticated JSON call to the publisher sidecar.
func CallPublisher(ctx context.Context, method, path string, input, output any) error {
	if !setting.PlatformManifestSync.Enabled || setting.PlatformManifestSync.URL == "" || setting.PlatformManifestSync.InternalToken == "" {
		return errors.New("deployment publisher is not configured")
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(setting.PlatformManifestSync.URL, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+setting.PlatformManifestSync.InternalToken)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: setting.PlatformManifestSync.SignatureTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return &PublisherError{Status: response.StatusCode, Message: strings.TrimSpace(string(message))}
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

// W3DSPrepareRequest reserves a W3DS deployment identity for a managed target.
type W3DSPrepareRequest struct {
	ID             string `json:"id"`
	RepositoryID   int64  `json:"repositoryId"`
	PlatformEName  string `json:"platformEName"`
	DeploymentName string `json:"deploymentName"`
	Environment    string `json:"environment"`
	DeployerEName  string `json:"deployerEName"`
	Version        string `json:"version"`
	ReleaseTag     string `json:"releaseTag"`
	CommitSHA      string `json:"commitSha"`
	PublicKey      string `json:"publicKey"`
}

func prepareW3DSDeployment(ctx context.Context, input W3DSPrepareRequest) (*PublisherDeployment, error) {
	prepared := new(PublisherDeployment)
	if err := current().Publisher.Call(ctx, http.MethodPost, "/api/v1/deployments/prepare", input, prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

// publishW3DSVersion moves an authorised deployment to a new release, signed
// with the target's deployment key.
func publishW3DSVersion(ctx context.Context, w3dsDeploymentID, deploymentEName, keyFile, version, tag, commitSHA string) error {
	payload, err := w3ds.DeploymentVersionPayload(deploymentEName, version, tag, commitSHA)
	if err != nil {
		return err
	}
	signature, err := w3ds.SignWithDeploymentKey(keyFile, payload)
	if err != nil {
		return err
	}
	return current().Publisher.Call(ctx, http.MethodPost, "/api/v1/deployments/"+url.PathEscape(w3dsDeploymentID)+"/versions", map[string]string{
		"version": version, "releaseTag": tag, "commitSha": commitSHA, "signature": signature,
	}, nil)
}

func certifiedVersion(ctx context.Context, repositoryID int64, platformEName, version string) (bool, error) {
	var result struct {
		Certifications map[string]struct {
			Certified bool `json:"certified"`
		} `json:"certifications"`
	}
	err := current().Publisher.Call(ctx, http.MethodPost, "/api/v1/platforms/deployment-certifications", map[string]any{
		"repositoryId": repositoryID, "platformEName": platformEName, "versions": []string{version},
	}, &result)
	if err != nil {
		return false, fmt.Errorf("check PPA certification: %w", err)
	}
	return result.Certifications[version].Certified, nil
}
