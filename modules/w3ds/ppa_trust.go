// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultTrustedPPAIssuers are the PPA services whose signed decisions count
// as certification when nothing else is configured.
var DefaultTrustedPPAIssuers = []string{"https://ppa.w3ds.metastate.foundation"}

// ErrUntrustedPPA means a decision was not issued by a trusted PPA.
var ErrUntrustedPPA = errors.New("PPA decision is not from a trusted issuer")

// PPAAccreditationRecord is a decision as stored in a platform's eVault. Only
// the JWS is trusted; every other field is a convenience copy that anyone
// with write access to the eVault could edit.
type PPAAccreditationRecord struct {
	JWS             string `json:"jws"`
	IssuerJWKSURI   string `json:"issuerJwksUri"`
	PlatformEName   string `json:"platformEName"`
	PlatformVersion string `json:"platformVersion"`
	CreatedAt       string `json:"createdAt"`
}

type ppaClaims struct {
	jwt.RegisteredClaims
	Decision          string  `json:"decision"`
	Level             *string `json:"level"`
	Statement         string  `json:"statement"`
	ReviewedBy        string  `json:"reviewedBy"`
	PlatformVersion   string  `json:"platformVersion"`
	ApplicantResponse *string `json:"applicantResponse"`
}

// NormalizePPAIssuers turns a configured list of hosts or origins into
// origins. Bare hosts mean https; http is only kept for local development.
func NormalizePPAIssuers(values []string) []string {
	issuers := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		if !strings.Contains(value, "://") {
			value = "https://" + value
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			continue
		}
		issuers = append(issuers, parsed.Scheme+"://"+strings.ToLower(parsed.Host))
	}
	return issuers
}

// PPAVerifier checks PPA decisions against an allowlist of issuers and their
// published signing keys.
type PPAVerifier struct {
	trusted map[string]bool
	client  *http.Client
	ttl     time.Duration

	mu   sync.Mutex
	keys map[string]cachedJWKS
}

type cachedJWKS struct {
	keys    map[string]*ecdsa.PublicKey
	fetched time.Time
}

// NewPPAVerifier trusts the given issuers (hosts or origins).
func NewPPAVerifier(issuers []string, client *http.Client) *PPAVerifier {
	verifier := &PPAVerifier{trusted: map[string]bool{}, client: client, ttl: 10 * time.Minute, keys: map[string]cachedJWKS{}}
	for _, issuer := range NormalizePPAIssuers(issuers) {
		verifier.trusted[issuer] = true
	}
	return verifier
}

// Trusts reports whether an issuer origin is on the allowlist.
func (v *PPAVerifier) Trusts(issuer string) bool {
	normalized := NormalizePPAIssuers([]string{issuer})
	return len(normalized) == 1 && v.trusted[normalized[0]]
}

// Verify returns the decision carried by a record's JWS if a trusted PPA
// signed it. The returned fields come from the signed claims, including the
// signed issue time used to order decisions.
func (v *PPAVerifier) Verify(ctx context.Context, record PPAAccreditationRecord) (*AccreditationDecision, error) {
	if strings.TrimSpace(record.JWS) == "" {
		return nil, fmt.Errorf("%w: the decision is not signed", ErrUntrustedPPA)
	}
	unverified := &ppaClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(record.JWS, unverified); err != nil {
		return nil, fmt.Errorf("parse PPA decision: %w", err)
	}
	issuer := NormalizePPAIssuers([]string{unverified.Issuer})
	if len(issuer) != 1 || !v.trusted[issuer[0]] {
		return nil, fmt.Errorf("%w: %q", ErrUntrustedPPA, unverified.Issuer)
	}
	// Keys always come from the trusted issuer itself, never from the
	// record's issuerJwksUri, which the eVault owner could point elsewhere.
	keys, err := v.issuerKeys(ctx, issuer[0])
	if err != nil {
		return nil, err
	}
	claims := &ppaClaims{}
	_, err = jwt.ParseWithClaims(record.JWS, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		if key, ok := keys[kid]; ok {
			return key, nil
		}
		return nil, fmt.Errorf("unknown PPA signing key %q", kid)
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuedAt())
	if err != nil {
		return nil, fmt.Errorf("verify PPA decision: %w", err)
	}
	if claims.Decision != "granted" && claims.Decision != "denied" {
		return nil, fmt.Errorf("PPA decision has unknown outcome %q", claims.Decision)
	}
	decision := &AccreditationDecision{
		PlatformEName: claims.Subject, PlatformVersion: claims.PlatformVersion, Decision: claims.Decision,
		Statement: claims.Statement, ReviewedByEName: claims.ReviewedBy,
	}
	if claims.Level != nil {
		decision.Level = *claims.Level
	}
	if claims.ApplicantResponse != nil {
		decision.ApplicantResponse = *claims.ApplicantResponse
	}
	if claims.IssuedAt != nil {
		decision.CreatedAt = claims.IssuedAt.UTC().Format(time.RFC3339)
	}
	return decision, nil
}

func (v *PPAVerifier) issuerKeys(ctx context.Context, issuer string) (map[string]*ecdsa.PublicKey, error) {
	v.mu.Lock()
	cached, ok := v.keys[issuer]
	v.mu.Unlock()
	if ok && time.Since(cached.fetched) < v.ttl {
		return cached.keys, nil
	}
	var jwks registryJWKS
	if err := getSignatureJSON(ctx, v.client, issuer+"/.well-known/jwks.json", nil, &jwks); err != nil {
		if ok {
			return cached.keys, nil // keep verifying with the last good keys
		}
		return nil, fmt.Errorf("fetch PPA keys from %s: %w", issuer, err)
	}
	keys, err := parseRegistryKeys(jwks)
	if err != nil {
		return nil, fmt.Errorf("PPA keys from %s: %w", issuer, err)
	}
	v.mu.Lock()
	v.keys[issuer] = cachedJWKS{keys: keys, fetched: time.Now()}
	v.mu.Unlock()
	return keys, nil
}
