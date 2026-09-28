// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// authorENames returns the W3DS-linked repository maintainers. Forgejo's
// assignees endpoint is backed by repository permissions and includes the
// owner, direct collaborators, and organization team members with write
// access. This deliberately avoids deriving ownership by walking Git history.
func (c *forgejoClient) authorENames(ctx context.Context, fullName string) ([]string, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, errors.New("invalid repository full name")
	}

	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/assignees", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))
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
	if response.StatusCode != http.StatusOK {
		return nil, responseError("list platform maintainers", response)
	}
	var maintainers []*structs.User
	if err := json.NewDecoder(response.Body).Decode(&maintainers); err != nil {
		return nil, fmt.Errorf("decode platform maintainers: %w", err)
	}
	authorENames := make([]string, 0, len(maintainers))
	for _, maintainer := range maintainers {
		if maintainer == nil {
			continue
		}
		ename := strings.TrimSpace(maintainer.LoginName)
		if strings.HasPrefix(ename, "@") && len(ename) > 1 {
			authorENames = append(authorENames, ename)
		}
	}
	return mergeENames(authorENames), nil
}

type w3dsClient struct {
	config Config
	http   *http.Client
	ppa    *w3ds.PPAVerifier
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newW3DSClient(config Config, client *http.Client) *w3dsClient {
	return &w3dsClient{config: config, http: client, ppa: w3ds.NewPPAVerifier(config.TrustedPPAIssuers, client)}
}

type preparedIdentity struct {
	RegistryEntropy string
	Namespace       string
	EName           string
}

type legacyPlatformProfile struct {
	ID           string
	Digest       string
	Payload      json.RawMessage
	AuthorENames []string
}

func (c *w3dsClient) inspectLegacyToken(ctx context.Context, token string) error {
	if strings.TrimSpace(c.config.RegistrySharedSecret) == "" {
		return errors.New("Registry migration credential is not configured")
	}
	var result struct {
		Fingerprint string `json:"fingerprint"`
	}
	return c.postJSON(ctx, c.config.RegistryURL+"/platforms/migrations/inspect-token", map[string]string{"token": token}, &result,
		map[string]string{"Authorization": "Bearer " + c.config.RegistrySharedSecret})
}

func (c *w3dsClient) legacyPlatformProfile(ctx context.Context, ename, token string) (*legacyPlatformProfile, error) {
	endpoint, err := c.resolve(ctx, ename)
	if err != nil {
		return nil, err
	}
	graphql := map[string]any{
		"query": `query ExistingPlatformProfiles($ontologyId: ID!, $first: Int!) {
	metaEnvelopes(filter: {ontologyId: $ontologyId}, first: $first) { edges { node { id parsed } } }
}`,
		"variables": map[string]any{"ontologyId": w3ds.UserProfileOntology, "first": 100},
	}
	var result struct {
		Data struct {
			MetaEnvelopes struct {
				Edges []struct {
					Node struct {
						ID     string          `json:"id"`
						Parsed json.RawMessage `json:"parsed"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"metaEnvelopes"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, graphql, &result, map[string]string{"Authorization": "Bearer " + token, "X-ENAME": ename}); err != nil {
		return nil, fmt.Errorf("read existing PlatformProfile: %w", err)
	}
	if err := graphQLErrors("eVault profile read", result.Errors); err != nil {
		return nil, err
	}
	matches := make([]legacyPlatformProfile, 0, 1)
	for _, edge := range result.Data.MetaEnvelopes.Edges {
		var payload map[string]any
		if edge.Node.ID == "" || json.Unmarshal(edge.Node.Parsed, &payload) != nil || strings.TrimSpace(stringValue(payload["ename"])) != ename {
			continue
		}
		canonical, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(canonical)
		matches = append(matches, legacyPlatformProfile{
			ID: edge.Node.ID, Digest: hex.EncodeToString(digest[:]), Payload: canonical,
			AuthorENames: explicitProfileAuthors(payload),
		})
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected exactly one PlatformProfile for %s, found %d", ename, len(matches))
	}
	return &matches[0], nil
}

func explicitProfileAuthors(payload map[string]any) []string {
	for _, field := range []string{"authorEnames", "authors", "ownerEName", "submittedBy", "contactEName"} {
		seen := map[string]struct{}{}
		authors := make([]string, 0)
		values := []any{payload[field]}
		if list, ok := payload[field].([]any); ok {
			values = list
		}
		for _, value := range values {
			ename := strings.TrimSpace(stringValue(value))
			if !strings.HasPrefix(ename, "@") {
				continue
			}
			if _, ok := seen[ename]; ok {
				continue
			}
			seen[ename] = struct{}{}
			authors = append(authors, ename)
		}
		if len(authors) > 0 {
			return authors
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func (c *w3dsClient) activateMigration(ctx context.Context, ename, envelopeID, legacyToken string) error {
	if strings.TrimSpace(c.config.RegistrySharedSecret) == "" {
		return errors.New("Registry migration credential is not configured")
	}
	var result struct {
		Token string `json:"token"`
	}
	return c.postJSON(ctx, c.config.RegistryURL+"/platforms/migrations/activate", map[string]string{
		"ename": ename, "manager": c.config.PublisherURL, "profileEnvelopeId": envelopeID, "legacyToken": legacyToken,
	}, &result, map[string]string{"Authorization": "Bearer " + c.config.RegistrySharedSecret})
}

func (c *w3dsClient) managerToken(ctx context.Context, ename string) (string, error) {
	if strings.TrimSpace(c.config.RegistrySharedSecret) == "" {
		return "", errors.New("Registry migration credential is not configured")
	}
	var result struct {
		Token string `json:"token"`
	}
	err := c.postJSON(ctx, c.config.RegistryURL+"/platforms/management/token", map[string]string{
		"ename": ename, "manager": c.config.PublisherURL,
	}, &result, map[string]string{"Authorization": "Bearer " + c.config.RegistrySharedSecret})
	if err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", errors.New("Registry returned no platform manager token")
	}
	return result.Token, nil
}

func deploymentEName(registryEntropy, namespace string) (string, error) {
	parts := strings.Split(registryEntropy, ".")
	if len(parts) != 3 {
		return "", errors.New("registry returned an invalid entropy token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("registry returned an invalid entropy token payload")
	}
	var claims struct {
		Entropy string `json:"entropy"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || strings.TrimSpace(claims.Entropy) == "" {
		return "", errors.New("registry entropy token has no entropy")
	}
	namespaceUUID, err := uuid.Parse(namespace)
	if err != nil {
		return "", errors.New("deployment namespace is not a valid UUID")
	}
	return "@" + uuid.NewSHA1(namespaceUUID, []byte(claims.Entropy)).String(), nil
}

func (c *w3dsClient) prepareIdentity(ctx context.Context) (*preparedIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.RegistryURL+"/entropy", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError("request registry entropy", response)
	}
	var entropy struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&entropy); err != nil || entropy.Token == "" {
		return nil, errors.New("registry returned invalid entropy")
	}
	prepared := &preparedIdentity{RegistryEntropy: entropy.Token, Namespace: uuid.NewString()}
	prepared.EName, err = deploymentEName(prepared.RegistryEntropy, prepared.Namespace)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (c *w3dsClient) provisionPrepared(ctx context.Context, prepared *preparedIdentity, publicKey string) (string, error) {
	if prepared == nil {
		return "", errors.New("prepared identity is required")
	}
	payload := map[string]string{
		"registryEntropy": prepared.RegistryEntropy,
		"namespace":       prepared.Namespace,
		"verificationId":  c.config.VerificationID,
		"publicKey":       publicKey,
	}
	var provisioned struct {
		Success bool   `json:"success"`
		W3ID    string `json:"w3id"`
	}
	if err := c.postJSON(ctx, c.config.ProvisionerURL+"/provision", payload, &provisioned, nil); err != nil {
		return "", fmt.Errorf("provision deployment eVault: %w", err)
	}
	if !provisioned.Success || strings.TrimSpace(provisioned.W3ID) != prepared.EName {
		return "", errors.New("provisioner returned a different deployment eName")
	}
	return prepared.EName, nil
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
		"registryEntropy": entropy.Token, "namespace": uuid.NewString(),
		"verificationId": c.config.VerificationID, "publicKey": publicKey,
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

func (c *w3dsClient) registerSoftwareVersion(ctx context.Context, job *DeploymentJob) error {
	token, err := c.platformToken(ctx)
	if err != nil {
		return err
	}
	payload := map[string]string{
		"platformEname": job.PlatformEName,
		"version":       job.Version,
		"releaseTag":    job.ReleaseTag,
		"commitSha":     job.CommitSHA,
	}
	var record struct {
		EName string `json:"ename"`
	}
	if err := c.postJSON(ctx, c.config.RegistryURL+"/records/software-versions", payload, &record, map[string]string{"Authorization": "Bearer " + token}); err != nil {
		return fmt.Errorf("register software version: %w", err)
	}
	if record.EName != job.VersionEName {
		return errors.New("Registry returned a different software version eName")
	}
	return nil
}

func (c *w3dsClient) deploymentRegistered(ctx context.Context, ename string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.RegistryURL+"/resolve?w3id="+url.QueryEscape(ename), nil)
	if err != nil {
		return false, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, responseError("resolve deployment eVault", response)
	}
	return true, nil
}

func (c *w3dsClient) publishDeployment(ctx context.Context, job *DeploymentJob) error {
	registered, err := c.deploymentRegistered(ctx, job.DeploymentEName)
	if err != nil {
		return err
	}
	if !registered {
		if _, err := c.provisionPrepared(ctx, &preparedIdentity{
			RegistryEntropy: job.RegistryEntropy, Namespace: job.Namespace, EName: job.DeploymentEName,
		}, job.PublicKey); err != nil {
			return err
		}
	}
	endpoint, err := c.resolve(ctx, job.DeploymentEName)
	if err != nil {
		return err
	}
	token, err := c.platformToken(ctx)
	if err != nil {
		return err
	}
	deploymentDocument, versionDocument, _, err := w3ds.BuildDeploymentAttestation(
		job.DeploymentEName, job.DeploymentName, job.Environment, job.DeployerEName,
		job.PlatformEName, job.VersionEName, job.Version, job.ReleaseTag, job.CommitSHA, job.PublicKey,
	)
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": "Bearer " + token, "X-ENAME": job.DeploymentEName}
	job.DeploymentKeyDocumentID, err = c.ensureDeploymentBinding(ctx, endpoint, headers, deploymentDocument, job)
	if err != nil {
		return err
	}
	job.SoftwareVersionDocumentID, err = c.ensureDeploymentBinding(ctx, endpoint, headers, versionDocument, job)
	if err != nil {
		return err
	}
	job.ProfileEnvelopeID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("gitw3:deployment:"+job.ID)).String()
	profile := w3ds.DeploymentProfile{
		DeploymentEName: job.DeploymentEName, DeploymentName: job.DeploymentName,
		Environment: job.Environment, DeployerEName: job.DeployerEName,
		PlatformEName: job.PlatformEName, VersionEName: job.VersionEName,
		Version: job.Version, ReleaseTag: job.ReleaseTag, CommitSHA: job.CommitSHA,
		PublicKey: job.PublicKey, DeploymentKeyDocumentID: job.DeploymentKeyDocumentID,
		SoftwareVersionDocumentID: job.SoftwareVersionDocumentID, CreatedAt: job.CreatedAt.Format(time.RFC3339),
	}
	graphql := map[string]any{
		"query": `mutation PublishDeployment($id: String!, $input: MetaEnvelopeInput!) {
	updateMetaEnvelopeById(id: $id, input: $input) { metaEnvelope { id } }
}`,
		"variables": map[string]any{"id": job.ProfileEnvelopeID, "input": map[string]any{
			"ontology": w3ds.DeploymentProfileOntology, "payload": profile, "acl": []string{"*"},
		}},
	}
	var result struct {
		Data struct {
			Update struct {
				MetaEnvelope *struct {
					ID string `json:"id"`
				} `json:"metaEnvelope"`
			} `json:"updateMetaEnvelopeById"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
		return fmt.Errorf("publish deployment profile: %w", err)
	}
	if err := graphQLErrors("deployment profile mutation", result.Errors); err != nil {
		return err
	}
	if result.Data.Update.MetaEnvelope == nil {
		return errors.New("eVault returned no deployment profile")
	}
	return c.registerSoftwareVersion(ctx, job)
}

func (c *w3dsClient) ensureDeploymentBinding(ctx context.Context, endpoint string, headers map[string]string, document w3ds.DeploymentBindingDocument, job *DeploymentJob) (string, error) {
	owner := deploymentBindingSigner(document, job)
	graphql := map[string]any{
		"query": `query ExistingDeploymentBindings($type: BindingDocumentType!) {
	bindingDocuments(type: $type, first: 100) { edges { node { id parsed } } }
}`,
		"variables": map[string]any{"type": document.Type},
	}
	var existing struct {
		Data struct {
			Documents struct {
				Edges []struct {
					Node struct {
						ID     string `json:"id"`
						Parsed struct {
							Subject    string `json:"subject"`
							Signatures []struct {
								Signature     string `json:"signature"`
								SignedPayload string `json:"signedPayload"`
							} `json:"signatures"`
						} `json:"parsed"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"bindingDocuments"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, graphql, &existing, headers); err != nil {
		return "", fmt.Errorf("find deployment binding: %w", err)
	}
	if err := graphQLErrors("deployment binding read", existing.Errors); err != nil {
		return "", err
	}
	for _, edge := range existing.Data.Documents.Edges {
		if edge.Node.Parsed.Subject != document.Subject {
			continue
		}
		for _, signature := range edge.Node.Parsed.Signatures {
			if signature.Signature == owner.Signature && signature.SignedPayload == owner.Payload {
				return edge.Node.ID, nil
			}
		}
	}
	mutation := map[string]any{
		"query": `mutation CreateDeploymentBinding($input: CreateBindingDocumentInput!) {
	createBindingDocument(input: $input) { metaEnvelopeId errors { message code } }
}`,
		"variables": map[string]any{"input": map[string]any{
			"subject": document.Subject, "type": document.Type, "data": document.Data,
			"ownerSignature": map[string]any{
				"signer": owner.Signer, "signature": owner.Signature,
				"timestamp": job.UpdatedAt.Format(time.RFC3339), "scope": owner.Scope, "signedPayload": owner.Payload,
			},
		}},
	}
	var created struct {
		Data struct {
			Create struct {
				ID     string                 `json:"metaEnvelopeId"`
				Errors []mutationPayloadError `json:"errors"`
			} `json:"createBindingDocument"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, mutation, &created, headers); err != nil {
		return "", fmt.Errorf("create deployment binding: %w", err)
	}
	if err := graphQLErrors("deployment binding mutation", created.Errors); err != nil {
		return "", err
	}
	if err := mutationPayloadErrors("deployment binding mutation", created.Data.Create.Errors); err != nil {
		return "", err
	}
	if created.Data.Create.ID == "" {
		return "", errors.New("eVault returned no deployment binding document")
	}
	return created.Data.Create.ID, nil
}

func (c *w3dsClient) accreditations(ctx context.Context, ename, version string) ([]w3ds.AccreditationDecision, error) {
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
	decisions := make([]w3ds.AccreditationDecision, 0)
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
							Parsed w3ds.PPAAccreditationRecord `json:"parsed"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"metaEnvelopes"`
			} `json:"data"`
			Errors []graphQLError `json:"errors"`
		}
		if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
			return nil, fmt.Errorf("read PPA decisions: %w", err)
		}
		if err := graphQLErrors("PPA decision read", result.Errors); err != nil {
			return nil, err
		}
		for _, edge := range result.Data.MetaEnvelopes.Edges {
			// Only decisions signed by a trusted PPA count; anything else in
			// the eVault could have been written by the platform owner.
			verified, err := c.ppa.Verify(ctx, edge.Node.Parsed)
			if err != nil {
				slog.Warn("ignoring PPA decision", "platform", ename, "error", err)
				continue
			}
			decision := *verified
			if strings.TrimSpace(decision.PlatformEName) != ename || (version != "" && strings.TrimSpace(decision.PlatformVersion) != version) {
				continue
			}
			if decision.Decision != "granted" && decision.Decision != "denied" {
				continue
			}
			decisions = append(decisions, decision)
		}
		pageInfo := result.Data.MetaEnvelopes.PageInfo
		if !pageInfo.HasNextPage || pageInfo.EndCursor == "" {
			sort.SliceStable(decisions, func(i, j int) bool {
				return decisions[i].CreatedAt < decisions[j].CreatedAt
			})
			return decisions, nil
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
		return atStage("Registry resolve", err)
	}
	var token string
	if manifest.Migration != nil && (manifest.Migration.Status == "active" || manifest.Migration.Status == "activating") {
		token, err = c.managerToken(ctx, ename)
	} else {
		token, err = c.platformToken(ctx)
	}
	if err != nil {
		return atStage("certification token request", err)
	}
	payload, err := platformProfilePayload(manifest, createdAt, archived, authorENames)
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": "Bearer " + token, "X-ENAME": ename}
	matches, exists, err := c.platformProfileMatches(ctx, endpoint, headers, envelopeID, w3ds.UserProfileOntology, payload)
	if err != nil {
		return atStage("eVault verification", err)
	}
	if matches {
		return nil
	}

	mutationErr := c.writePlatformProfile(ctx, endpoint, headers, envelopeID, w3ds.UserProfileOntology, payload, exists)
	// An eVault can commit a write and still lose or mask the response. Always
	// read the deterministic envelope after the mutation; matching state is the
	// authoritative success signal for retries and ambiguous responses.
	matches, _, verifyErr := c.platformProfileMatches(ctx, endpoint, headers, envelopeID, w3ds.UserProfileOntology, payload)
	if verifyErr == nil && matches {
		return nil
	}
	if mutationErr != nil {
		return mutationErr
	}
	if verifyErr != nil {
		return atStage("eVault verification", verifyErr)
	}
	return &publicationError{Stage: "eVault verification", Err: errors.New("PlatformProfile did not match the requested state after mutation")}
}

func platformProfilePayload(manifest *w3ds.PlatformManifest, createdAt time.Time, archived bool, authorENames []string) (map[string]any, error) {
	if manifest == nil || manifest.EName == nil {
		return nil, errors.New("cannot build a platform profile without an eName")
	}
	ename := *manifest.EName
	now := time.Now().UTC()
	domains := manifest.Domains
	if domains == nil {
		domains = []string{}
	}
	payload := map[string]any{}
	if manifest.Migration != nil && len(manifest.Migration.SourceProfile) > 0 {
		if err := json.Unmarshal(manifest.Migration.SourceProfile, &payload); err != nil {
			return nil, fmt.Errorf("decode migrated source profile: %w", err)
		}
	}
	updates := map[string]any{
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
	for name, value := range updates {
		payload[name] = value
	}
	if manifest.SubmissionProof != nil {
		payload["submissionProof"] = manifest.SubmissionProof
		payload["submittedBy"] = manifest.SubmissionProof.Statement.SignerEName
	}
	if len(manifest.SubmissionHistory) > 0 {
		payload["submissionHistory"] = manifest.SubmissionHistory
	}
	if manifest.Category != "" {
		payload["category"] = manifest.Category
	}
	return payload, nil
}

func (c *w3dsClient) platformProfileMatches(ctx context.Context, endpoint string, headers map[string]string, envelopeID, ontology string, desired map[string]any) (matches, exists bool, err error) {
	graphql := map[string]any{
		"query": `query ExistingPlatformProfile($id: ID!) {
	profile: metaEnvelope(id: $id) { id ontology parsed }
}`,
		"variables": map[string]any{"id": envelopeID},
	}
	var result struct {
		Data struct {
			Profile *struct {
				ID       string         `json:"id"`
				Ontology string         `json:"ontology"`
				Parsed   map[string]any `json:"parsed"`
			} `json:"profile"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
		return false, false, err
	}
	if err := graphQLErrors("eVault verification", result.Errors); err != nil {
		return false, false, err
	}
	if result.Data.Profile == nil {
		return false, false, nil
	}
	if result.Data.Profile.ID != envelopeID || result.Data.Profile.Ontology != ontology {
		return false, true, nil
	}
	return relevantPayloadMatches(result.Data.Profile.Parsed, desired), true, nil
}

func relevantPayloadMatches(existing, desired map[string]any) bool {
	for key, expected := range desired {
		// updatedAt records the actual write time. It is deliberately excluded
		// from idempotency so a retry does not manufacture another semantic
		// change solely because its wall clock advanced.
		if key == "updatedAt" {
			continue
		}
		actual, ok := existing[key]
		if !ok {
			return false
		}
		actualJSON, actualErr := normalizedJSON(actual)
		expectedJSON, expectedErr := normalizedJSON(expected)
		if actualErr != nil || expectedErr != nil || !bytes.Equal(actualJSON, expectedJSON) {
			return false
		}
	}
	return true
}

func normalizedJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func (c *w3dsClient) writePlatformProfile(ctx context.Context, endpoint string, headers map[string]string, envelopeID, ontology string, payload map[string]any, exists bool) error {
	variables := map[string]any{
		"id": envelopeID,
		"input": map[string]any{
			"ontology": ontology,
			"payload":  payload,
			"acl":      []string{"*"},
		},
	}
	mutation := `mutation UpsertPlatformProfile($id: String!, $input: MetaEnvelopeInput!) {
	update: updateMetaEnvelopeById(id: $id, input: $input) { metaEnvelope { id } }
}`
	if exists {
		mutation = `mutation UpdatePlatformProfile($id: ID!, $input: MetaEnvelopeInput!) {
	update: updateMetaEnvelope(id: $id, input: $input) {
		metaEnvelope { id }
		errors { field message code }
	}
}`
	}
	graphql := map[string]any{"query": mutation, "variables": variables}
	var result struct {
		Data struct {
			Update struct {
				MetaEnvelope *struct {
					ID string `json:"id"`
				} `json:"metaEnvelope"`
				Errors []mutationPayloadError `json:"errors"`
			} `json:"update"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := c.postJSON(ctx, endpoint, graphql, &result, headers); err != nil {
		return atStage("eVault mutation", err)
	}
	if err := graphQLErrors("eVault mutation", result.Errors); err != nil {
		return err
	}
	if err := mutationPayloadErrors("eVault mutation", result.Data.Update.Errors); err != nil {
		return err
	}
	if result.Data.Update.MetaEnvelope == nil {
		return &publicationError{Stage: "eVault mutation", Err: errors.New("eVault returned no PlatformProfile")}
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
	if token := strings.TrimSpace(c.config.PlatformToken); token != "" {
		return token, nil
	}
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
	return fmt.Errorf("%s returned %d: %s", operation, response.StatusCode, safeResponseBody(data))
}

type bindingSigner struct {
	Signer    string
	Signature string
	Scope     string
	Payload   string
}

// deploymentBindingSigner picks who signs a deployment binding. The first
// version is covered by the deployer's wallet signature over the whole
// bundle; later versions of a managed deployment are signed by the
// deployment's own key, which the wallet authorised in that first bundle.
func deploymentBindingSigner(document w3ds.DeploymentBindingDocument, job *DeploymentJob) bindingSigner {
	if document.Type == "software_version" && job.VersionSignature != "" {
		return bindingSigner{Signer: job.DeploymentEName, Signature: job.VersionSignature, Scope: "deployment_version", Payload: job.VersionPayload}
	}
	return bindingSigner{Signer: job.DeployerEName, Signature: job.WalletSignature, Scope: "bundle", Payload: job.BundlePayload}
}
