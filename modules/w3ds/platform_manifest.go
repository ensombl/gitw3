// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PlatformManifestPath          = ".w3ds/platform.json"
	PlatformManifestVersion       = 1
	DefaultPlatformVersion        = "0.1.0"
	UserProfileOntology           = "550e8400-e29b-41d4-a716-446655440000"
	PlatformAccreditationOntology = "e1749947-5a10-4973-b9fa-230d8714c36a"
	PPASubmissionStatementType    = "w3ds.ppa.release-submission"
	PPASubmissionPayloadPrefix    = "gitw3:ppa:v1:"
)

// PPASubmissionStatement is the canonical release application an authorized
// repository owner signs with their eID wallet.
type PPASubmissionStatement struct {
	Type               string   `json:"type"`
	SchemaVersion      int      `json:"schemaVersion"`
	RepositoryID       int64    `json:"repositoryId"`
	Repository         string   `json:"repository"`
	PlatformEName      string   `json:"platformEName"`
	PlatformName       string   `json:"platformName"`
	ReleaseTag         string   `json:"releaseTag"`
	Version            string   `json:"version"`
	ManifestCommitID   string   `json:"manifestCommitId"`
	Domains            []string `json:"domains"`
	SignerEName        string   `json:"signerEName"`
	IssuedAt           string   `json:"issuedAt"`
	Nonce              string   `json:"nonce"`
	PreviousDecision   string   `json:"previousDecision,omitempty"`
	PreviousDecisionAt string   `json:"previousDecisionAt,omitempty"`
	ResponseToDecision string   `json:"responseToDecision,omitempty"`
}

// PPASubmissionProof travels with the PlatformProfile in the platform eVault.
// The certificate records the Registry-backed binding that was valid when
// GitW3 accepted the signature.
type PPASubmissionProof struct {
	Statement             PPASubmissionStatement `json:"statement"`
	Payload               string                 `json:"payload"`
	Signature             string                 `json:"signature"`
	PublicKey             string                 `json:"publicKey"`
	KeyBindingCertificate string                 `json:"keyBindingCertificate"`
	VerifiedAt            string                 `json:"verifiedAt"`
}

// SigningPayload returns the exact short string the eID wallet signs. It is a
// hash of the complete canonical statement, so the durable signature binds all
// release, repository, signer, and domain fields without producing an unwieldy
// QR session value.
func (s *PPASubmissionStatement) SigningPayload() (string, error) {
	if s == nil {
		return "", errors.New("PPA submission statement is required")
	}
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return PPASubmissionPayloadPrefix + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// NormalizeReleaseVersion converts a conventional release tag into the
// semantic version stored in the platform profile.
func NormalizeReleaseVersion(tag string) (string, bool) {
	version := strings.TrimSpace(tag)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	return version, semverPattern.MatchString(version)
}

var (
	platformNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	domainIDPattern     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	semverPattern       = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	knownCategories     = map[string]struct{}{
		"Identity": {}, "Social": {}, "Governance": {}, "Wellness": {},
		"Finance": {}, "Storage": {}, "Productivity": {}, "Other": {},
	}
)

// PlatformManifest is the repository-owned source of truth for a W3DS platform.
type PlatformManifest struct {
	SchemaVersion     int                  `json:"schemaVersion"`
	PlatformName      string               `json:"platformName"`
	DisplayName       string               `json:"displayName"`
	Description       string               `json:"description"`
	Version           string               `json:"version"`
	EName             *string              `json:"ename"`
	URL               string               `json:"url"`
	LogoURL           string               `json:"logoUrl"`
	Domains           []string             `json:"domains,omitempty"`
	Category          string               `json:"category,omitempty"`
	PublicKey         string               `json:"publicKey"`
	InSubmission      bool                 `json:"inSubmission"`
	SubmissionVersion string               `json:"submissionVersion,omitempty"`
	SubmissionProof   *PPASubmissionProof  `json:"submissionProof,omitempty"`
	SubmissionHistory []PPASubmissionProof `json:"submissionHistory,omitempty"`
	IsDraft           bool                 `json:"isDraft"`
}

// NewPlatformManifest creates a manifest whose eName will be filled by the publisher.
func NewPlatformManifest(platformName, displayName, description, version, appURL, logoURL string, domains []string, publicKey string) *PlatformManifest {
	return &PlatformManifest{
		SchemaVersion:     PlatformManifestVersion,
		PlatformName:      strings.TrimSpace(platformName),
		DisplayName:       strings.TrimSpace(displayName),
		Description:       strings.TrimSpace(description),
		Version:           strings.TrimSpace(version),
		EName:             nil,
		URL:               strings.TrimSpace(appURL),
		LogoURL:           strings.TrimSpace(logoURL),
		Domains:           normalizeDomains(domains),
		PublicKey:         strings.TrimSpace(publicKey),
		InSubmission:      false,
		SubmissionVersion: "",
		SubmissionProof:   nil,
		IsDraft:           true,
	}
}

// Validate checks the version-one repository contract.
func (m *PlatformManifest) Validate(allowLocalHTTP bool) error {
	if m == nil {
		return errors.New("platform manifest is required")
	}
	if m.SchemaVersion != PlatformManifestVersion {
		return fmt.Errorf("schemaVersion must be %d", PlatformManifestVersion)
	}
	if !platformNamePattern.MatchString(m.PlatformName) || len(m.PlatformName) > 100 {
		return errors.New("platformName must be a lowercase, dash-separated slug")
	}
	if m.DisplayName == "" || len(m.DisplayName) > 100 {
		return errors.New("displayName must contain between 1 and 100 characters")
	}
	if m.Description == "" || len(m.Description) > 2048 {
		return errors.New("description must contain between 1 and 2048 characters")
	}
	if !semverPattern.MatchString(m.Version) {
		return errors.New("version must be a semantic version such as 0.1.0")
	}
	if m.SubmissionVersion != "" && !semverPattern.MatchString(m.SubmissionVersion) {
		return errors.New("submissionVersion must be a semantic version")
	}
	if err := m.validateSubmissionProof(); err != nil {
		return err
	}
	if err := m.validateSubmissionHistory(); err != nil {
		return err
	}
	if err := validateManifestDomains(m.Domains, m.Category); err != nil {
		return err
	}
	if !strings.HasPrefix(m.PublicKey, "z") || len(m.PublicKey) < 2 || len(m.PublicKey) > 8192 {
		return errors.New("publicKey must be a z-prefixed multibase key")
	}
	if err := validateWebURL(m.URL, true, allowLocalHTTP); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if m.LogoURL != "" {
		if err := validateWebURL(m.LogoURL, true, allowLocalHTTP); err != nil {
			return fmt.Errorf("logoUrl: %w", err)
		}
	}
	if m.EName != nil && strings.TrimSpace(*m.EName) == "" {
		return errors.New("ename must be null or a non-empty W3DS identifier")
	}
	return nil
}

func (m *PlatformManifest) validateSubmissionProof() error {
	if !m.InSubmission {
		if m.SubmissionVersion != "" || m.SubmissionProof != nil {
			return errors.New("submissionVersion and submissionProof require inSubmission")
		}
		return nil
	}
	if m.SubmissionVersion == "" || m.SubmissionVersion != m.Version {
		return errors.New("an active submission must match the current platform version")
	}
	proof := m.SubmissionProof
	if proof == nil {
		return errors.New("an active submission requires an eID wallet signature proof")
	}
	statement := &proof.Statement
	if statement.Type != PPASubmissionStatementType || statement.SchemaVersion != 1 {
		return errors.New("submissionProof contains an unsupported statement")
	}
	if statement.RepositoryID <= 0 || strings.TrimSpace(statement.Repository) == "" || strings.TrimSpace(statement.ManifestCommitID) == "" {
		return errors.New("submissionProof is missing repository details")
	}
	if statement.PlatformName != m.PlatformName || statement.Version != m.Version || statement.ReleaseTag == "" {
		return errors.New("submissionProof does not match this platform release")
	}
	if m.EName == nil || statement.PlatformEName != strings.TrimSpace(*m.EName) {
		return errors.New("submissionProof does not match the platform eName")
	}
	if !strings.HasPrefix(statement.SignerEName, "@") || !slices.Equal(statement.Domains, m.Domains) {
		return errors.New("submissionProof does not match the signer or application domains")
	}
	issuedAt, err := time.Parse(time.RFC3339, statement.IssuedAt)
	if err != nil || issuedAt.IsZero() || strings.TrimSpace(statement.Nonce) == "" {
		return errors.New("submissionProof has invalid issuance details")
	}
	if (statement.PreviousDecision == "") != (statement.PreviousDecisionAt == "") || (statement.PreviousDecision != "" && statement.PreviousDecision != "denied") {
		return errors.New("submissionProof has invalid reapplication details")
	}
	if statement.ResponseToDecision != "" && statement.PreviousDecision != "denied" {
		return errors.New("submissionProof response requires a previous denial")
	}
	if utf8.RuneCountInString(statement.ResponseToDecision) > 2048 {
		return errors.New("submissionProof response must not exceed 2048 characters")
	}
	if statement.PreviousDecisionAt != "" {
		if _, err := time.Parse(time.RFC3339, statement.PreviousDecisionAt); err != nil {
			return errors.New("submissionProof has an invalid previous decision time")
		}
	}
	if _, err := time.Parse(time.RFC3339, proof.VerifiedAt); err != nil {
		return errors.New("submissionProof has an invalid verification time")
	}
	payload, err := statement.SigningPayload()
	if err != nil || proof.Payload != payload {
		return errors.New("submissionProof payload does not match its statement")
	}
	if proof.Signature == "" || len(proof.Signature) > 8192 || proof.PublicKey == "" || len(proof.PublicKey) > 8192 || proof.KeyBindingCertificate == "" || len(proof.KeyBindingCertificate) > 32768 {
		return errors.New("submissionProof is missing cryptographic evidence")
	}
	return nil
}

func (m *PlatformManifest) validateSubmissionHistory() error {
	if len(m.SubmissionHistory) > 100 {
		return errors.New("submissionHistory must not contain more than 100 signed submissions")
	}
	seen := make(map[string]struct{}, len(m.SubmissionHistory))
	for i := range m.SubmissionHistory {
		proof := &m.SubmissionHistory[i]
		if _, exists := seen[proof.Payload]; exists {
			return errors.New("submissionHistory must not contain duplicate signed submissions")
		}
		seen[proof.Payload] = struct{}{}
		historical := *m
		historical.Version = proof.Statement.Version
		historical.Domains = append([]string(nil), proof.Statement.Domains...)
		historical.InSubmission = true
		historical.SubmissionVersion = proof.Statement.Version
		historical.SubmissionProof = proof
		historical.SubmissionHistory = nil
		if err := historical.validateSubmissionProof(); err != nil {
			return fmt.Errorf("submissionHistory[%d]: %w", i, err)
		}
		if err := validateManifestDomains(historical.Domains, ""); err != nil {
			return fmt.Errorf("submissionHistory[%d]: %w", i, err)
		}
	}
	return nil
}

func normalizeDomains(domains []string) []string {
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		normalized = append(normalized, strings.TrimSpace(domain))
	}
	return normalized
}

func validateManifestDomains(domains []string, legacyCategory string) error {
	if len(domains) == 0 {
		if _, ok := knownCategories[legacyCategory]; ok {
			return nil
		}
		return errors.New("at least one application domain is required")
	}
	if len(domains) > 100 {
		return errors.New("no more than 100 application domains may be selected")
	}
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		if !domainIDPattern.MatchString(domain) || len(domain) > 100 {
			return errors.New("domains must be lowercase, dash-separated identifiers")
		}
		if _, exists := seen[domain]; exists {
			return errors.New("domains must not contain duplicates")
		}
		seen[domain] = struct{}{}
	}
	return nil
}

func validateWebURL(value string, optional, allowLocalHTTP bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return errors.New("a public application URL is required")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if allowLocalHTTP && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
		return nil
	}
	return errors.New("must use HTTPS")
}

// Marshal returns stable, human-readable manifest JSON with a trailing newline.
func (m *PlatformManifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
