// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"forgejo.org/modules/structs"
	"forgejo.org/modules/w3ds"

	"github.com/google/uuid"
)

var ErrManifestNotFound = errors.New("platform manifest not found")
var ErrReleaseNotFound = errors.New("published release not found")

type platformRelease struct {
	TagName string
	Version string
}

type forgejoClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newForgejoClient(config Config, client *http.Client) *forgejoClient {
	return &forgejoClient{baseURL: config.ForgejoURL, token: config.ForgejoToken, http: client}
}

func (c *forgejoClient) manifest(ctx context.Context, fullName, ref string) (*w3ds.PlatformManifest, string, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, "", errors.New("invalid repository full name")
	}
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/.w3ds/platform.json?ref=%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(ref))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Authorization", "token "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, "", ErrManifestNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", responseError("fetch manifest", response)
	}
	var content structs.ContentsResponse
	if err := json.NewDecoder(response.Body).Decode(&content); err != nil {
		return nil, "", fmt.Errorf("decode manifest response: %w", err)
	}
	if content.Content == nil {
		return nil, "", errors.New("manifest response has no content")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(*content.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("decode manifest content: %w", err)
	}
	var manifest w3ds.PlatformManifest
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, "", fmt.Errorf("parse manifest: %w", err)
	}
	return &manifest, content.SHA, nil
}

func (c *forgejoClient) latestRelease(ctx context.Context, fullName string) (*platformRelease, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, errors.New("invalid repository full name")
	}
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/releases/latest", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "token "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrReleaseNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError("fetch latest platform release", response)
	}
	var release structs.Release
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode latest platform release: %w", err)
	}
	version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
	if !valid {
		return nil, fmt.Errorf("latest release tag %q is not a semantic version such as v1.2.3", release.TagName)
	}
	return &platformRelease{TagName: release.TagName, Version: version}, nil
}

func (c *forgejoClient) updateManifest(ctx context.Context, fullName, branch, sha, message string, manifest *w3ds.PlatformManifest) error {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return errors.New("invalid repository full name")
	}
	data, err := manifest.Marshal()
	if err != nil {
		return err
	}
	payload := structs.UpdateFileOptions{
		DeleteFileOptions: structs.DeleteFileOptions{
			FileOptions: structs.FileOptions{
				Message:    message,
				BranchName: branch,
			},
			SHA: sha,
		},
		ContentBase64: base64.StdEncoding.EncodeToString(data),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/.w3ds/platform.json", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "token "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("update manifest", response)
	}
	return nil
}

func (c *forgejoClient) authorENames(ctx context.Context, fullName, ref string) ([]string, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, errors.New("invalid repository full name")
	}

	const pageSize = 50
	usernames := make(map[string]struct{})
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/commits?sha=%s&page=%d&limit=%d&stat=false&files=false&verification=false",
			c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(ref), page, pageSize)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "token "+c.token)
		response, err := c.http.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			err := responseError("fetch platform committers", response)
			response.Body.Close()
			return nil, err
		}
		var commits []*structs.Commit
		decodeErr := json.NewDecoder(response.Body).Decode(&commits)
		response.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode platform committers: %w", decodeErr)
		}
		for _, commit := range commits {
			if commit.Author != nil && commit.Author.UserName != "" {
				usernames[commit.Author.UserName] = struct{}{}
			}
			if commit.Committer != nil && commit.Committer.UserName != "" {
				usernames[commit.Committer.UserName] = struct{}{}
			}
		}
		if len(commits) < pageSize {
			break
		}
	}

	orderedUsers := make([]string, 0, len(usernames))
	for username := range usernames {
		orderedUsers = append(orderedUsers, username)
	}
	sort.Strings(orderedUsers)
	authorENames := make([]string, 0, len(orderedUsers))
	seenENames := make(map[string]struct{}, len(orderedUsers))
	for _, username := range orderedUsers {
		endpoint := fmt.Sprintf("%s/api/v1/users/%s", c.baseURL, url.PathEscape(username))
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "token "+c.token)
		response, err := c.http.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusNotFound {
			response.Body.Close()
			continue
		}
		if response.StatusCode != http.StatusOK {
			err := responseError("resolve platform committer identity", response)
			response.Body.Close()
			return nil, err
		}
		var user structs.User
		decodeErr := json.NewDecoder(response.Body).Decode(&user)
		response.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode platform committer identity: %w", decodeErr)
		}
		eName := strings.TrimSpace(user.LoginName)
		if !strings.HasPrefix(eName, "@") || len(eName) < 2 {
			continue
		}
		if _, seen := seenENames[eName]; seen {
			continue
		}
		seenENames[eName] = struct{}{}
		authorENames = append(authorENames, eName)
	}
	return authorENames, nil
}

type w3dsClient struct {
	config Config
	http   *http.Client
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newW3DSClient(config Config, client *http.Client) *w3dsClient {
	return &w3dsClient{config: config, http: client}
}

func (c *w3dsClient) provision(ctx context.Context, publicKey string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.RegistryURL+"/entropy", nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseError("request registry entropy", response)
	}
	var entropy struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&entropy); err != nil || entropy.Token == "" {
		return "", errors.New("registry returned invalid entropy")
	}

	payload := map[string]string{
		"registryEntropy": entropy.Token,
		"namespace":       uuid.NewString(),
		"verificationId":  c.config.VerificationID,
		"publicKey":       publicKey,
	}
	var provisioned struct {
		Success bool   `json:"success"`
		W3ID    string `json:"w3id"`
	}
	if err := c.postJSON(ctx, c.config.ProvisionerURL+"/provision", payload, &provisioned, nil); err != nil {
		return "", fmt.Errorf("provision platform eVault: %w", err)
	}
	if !provisioned.Success || strings.TrimSpace(provisioned.W3ID) == "" {
		return "", errors.New("provisioner did not return a platform eName")
	}
	return strings.TrimSpace(provisioned.W3ID), nil
}

func (c *w3dsClient) accreditation(ctx context.Context, ename, version string) (*w3ds.AccreditationDecision, error) {
	endpoint, err := c.resolve(ctx, ename)
	if err != nil {
		return nil, err
	}
	token, err := c.platformToken(ctx)
	if err != nil {
		return nil, err
	}
	const query = `query PlatformAccreditations($ontologyId: ID!, $first: Int!, $after: String) {
	metaEnvelopes(filter: {ontologyId: $ontologyId}, first: $first, after: $after) {
		edges { node { parsed } }
		pageInfo { hasNextPage endCursor }
	}
}`
	headers := map[string]string{"Authorization": "Bearer " + token, "X-ENAME": ename}
	var newest *w3ds.AccreditationDecision
	var after any
	for {
		graphql := map[string]any{
			"query": query,
			"variables": map[string]any{
				"ontologyId": w3ds.PlatformAccreditationOntology,
				"first":      100,
				"after":      after,
			},
		}
		var result struct {
			Data struct {
				MetaEnvelopes struct {
					Edges []struct {
						Node struct {
							Parsed w3ds.AccreditationDecision `json:"parsed"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"metaEnvelopes"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
			return nil, fmt.Errorf("read PPA decisions: %w", err)
		}
		if len(result.Errors) > 0 {
			return nil, errors.New(result.Errors[0].Message)
		}
		for _, edge := range result.Data.MetaEnvelopes.Edges {
			decision := edge.Node.Parsed
			if strings.TrimSpace(decision.PlatformEName) != ename || strings.TrimSpace(decision.PlatformVersion) != version {
				continue
			}
			if decision.Decision != "granted" && decision.Decision != "denied" {
				continue
			}
			if newest == nil || decision.CreatedAt > newest.CreatedAt {
				copy := decision
				newest = &copy
			}
		}
		pageInfo := result.Data.MetaEnvelopes.PageInfo
		if !pageInfo.HasNextPage || pageInfo.EndCursor == "" {
			return newest, nil
		}
		after = pageInfo.EndCursor
	}
}

func (c *w3dsClient) publish(ctx context.Context, envelopeID string, manifest *w3ds.PlatformManifest, createdAt time.Time, archived bool, authorENames []string) error {
	if manifest == nil || manifest.EName == nil {
		return errors.New("cannot publish a platform without an eName")
	}
	ename := *manifest.EName
	endpoint, err := c.resolve(ctx, ename)
	if err != nil {
		return err
	}
	token, err := c.platformToken(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	domains := manifest.Domains
	if domains == nil {
		domains = []string{}
	}
	payload := map[string]any{
		"platformName":      manifest.PlatformName,
		"displayName":       manifest.DisplayName,
		"description":       manifest.Description,
		"version":           manifest.Version,
		"ename":             ename,
		"isActive":          !archived && !manifest.IsDraft,
		"isArchived":        archived,
		"createdAt":         createdAt.Format(time.RFC3339),
		"updatedAt":         now.Format(time.RFC3339),
		"url":               manifest.URL,
		"logoUrl":           manifest.LogoURL,
		"domains":           domains,
		"requestedDomains":  domains,
		"inSubmission":      manifest.InSubmission,
		"submissionVersion": manifest.SubmissionVersion,
		"isDraft":           manifest.IsDraft,
		"authorEnames":      authorENames,
	}
	if manifest.SubmissionProof != nil {
		payload["submissionProof"] = manifest.SubmissionProof
		payload["submittedBy"] = manifest.SubmissionProof.Statement.SignerEName
	}
	if manifest.Category != "" {
		payload["category"] = manifest.Category
	}
	variables := map[string]any{
		"id": envelopeID,
		"input": map[string]any{
			"ontology": w3ds.UserProfileOntology,
			"payload":  payload,
			"acl":      []string{"*"},
		},
	}
	graphql := map[string]any{
		"query": `mutation UpdatePlatformProfile($id: String!, $input: MetaEnvelopeInput!) {
	updateMetaEnvelopeById(id: $id, input: $input) { metaEnvelope { id } }
}`,
		"variables": variables,
	}
	var result struct {
		Data struct {
			Update struct {
				MetaEnvelope *struct {
					ID string `json:"id"`
				} `json:"metaEnvelope"`
			} `json:"updateMetaEnvelopeById"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	headers := map[string]string{"Authorization": "Bearer " + token, "X-ENAME": ename}
	if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
		return fmt.Errorf("publish PlatformProfile: %w", err)
	}
	if len(result.Errors) > 0 {
		return errors.New(result.Errors[0].Message)
	}
	if result.Data.Update.MetaEnvelope == nil {
		return errors.New("eVault returned no PlatformProfile")
	}
	return nil
}

func (c *w3dsClient) resolve(ctx context.Context, ename string) (string, error) {
	endpoint := c.config.RegistryURL + "/resolve?w3id=" + url.QueryEscape(ename)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseError("resolve platform eVault", response)
	}
	var resolved struct {
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(response.Body).Decode(&resolved); err != nil || resolved.URI == "" {
		return "", errors.New("registry returned no eVault URI")
	}
	return strings.TrimRight(resolved.URI, "/") + "/graphql", nil
}

func (c *w3dsClient) platformToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiry) > time.Minute {
		return c.token, nil
	}
	var certified struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := c.postJSON(ctx, c.config.RegistryURL+"/platforms/certification", map[string]string{"platform": c.config.PublisherURL}, &certified, nil); err != nil {
		return "", fmt.Errorf("request platform token: %w", err)
	}
	if certified.Token == "" {
		return "", errors.New("registry returned no platform token")
	}
	c.token = certified.Token
	c.expiry = time.Now().Add(time.Hour)
	if certified.ExpiresAt > 0 {
		c.expiry = time.UnixMilli(certified.ExpiresAt)
	}
	return c.token, nil
}

func (c *w3dsClient) postJSON(ctx context.Context, endpoint string, input, output any, headers map[string]string) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError("POST "+endpoint, response)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return err
		}
	}
	return nil
}

func responseError(operation string, response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("%s returned %d: %s", operation, response.StatusCode, strings.TrimSpace(string(data)))
}
