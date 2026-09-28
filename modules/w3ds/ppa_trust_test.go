// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forgejo.org/modules/json"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPPA struct {
	server *httptest.Server
	key    *ecdsa.PrivateKey
}

func newTestPPA(t *testing.T) *testPPA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ppa := &testPPA{key: key}
	ppa.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "kid": "ppa-accreditation-key-1", "alg": "ES256", "use": "sig",
			"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
			"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
		}}})
	}))
	t.Cleanup(ppa.server.Close)
	return ppa
}

func (p *testPPA) sign(t *testing.T, issuer, platform, version, decision string, key *ecdsa.PrivateKey) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": issuer, "sub": platform, "jti": "acc-1", "iat": time.Now().Unix(),
		"decision": decision, "level": "L2", "statement": "ok", "reviewedBy": "@reviewer",
		"platformVersion": version,
	})
	token.Header["kid"] = "ppa-accreditation-key-1"
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

func TestPPAVerifier(t *testing.T) {
	ppa := newTestPPA(t)
	other := newTestPPA(t)
	ctx := context.Background()
	verifier := NewPPAVerifier([]string{ppa.server.URL}, ppa.server.Client())

	jws := ppa.sign(t, ppa.server.URL, "@platform", "1.2.3", "granted", ppa.key)
	decision, err := verifier.Verify(ctx, PPAAccreditationRecord{
		JWS: jws, PlatformEName: "@platform", PlatformVersion: "9.9.9", // unsigned fields are ignored
	})
	require.NoError(t, err)
	assert.Equal(t, "@platform", decision.PlatformEName)
	assert.Equal(t, "1.2.3", decision.PlatformVersion, "the signed version wins over the record's copy")
	assert.Equal(t, "granted", decision.Decision)
	assert.Equal(t, "L2", decision.Level)
	assert.NotEmpty(t, decision.CreatedAt)

	_, err = verifier.Verify(ctx, PPAAccreditationRecord{PlatformEName: "@platform"})
	require.ErrorIs(t, err, ErrUntrustedPPA, "unsigned records never count")

	untrusted := other.sign(t, other.server.URL, "@platform", "1.2.3", "granted", other.key)
	_, err = verifier.Verify(ctx, PPAAccreditationRecord{JWS: untrusted})
	require.ErrorIs(t, err, ErrUntrustedPPA, "a PPA that is not on the allowlist is ignored")

	forged := ppa.sign(t, ppa.server.URL, "@platform", "1.2.3", "granted", other.key)
	_, err = verifier.Verify(ctx, PPAAccreditationRecord{JWS: forged})
	require.Error(t, err, "claiming a trusted issuer without its key fails")

	parts := strings.Split(jws, ".")
	tamperedClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + ppa.server.URL + `","sub":"@other","decision":"granted","platformVersion":"1.2.3"}`))
	_, err = verifier.Verify(ctx, PPAAccreditationRecord{JWS: parts[0] + "." + tamperedClaims + "." + parts[2]})
	require.Error(t, err, "edited claims break the signature")
}

func TestNormalizePPAIssuers(t *testing.T) {
	assert.Equal(t,
		[]string{"https://ppa.w3ds.metastate.foundation", "http://localhost:4210"},
		NormalizePPAIssuers([]string{" ppa.w3ds.metastate.foundation/ ", "http://localhost:4210", "", "ftp://nope"}))
	verifier := NewPPAVerifier(DefaultTrustedPPAIssuers, http.DefaultClient)
	assert.True(t, verifier.Trusts("https://ppa.w3ds.metastate.foundation"))
	assert.True(t, verifier.Trusts("ppa.w3ds.metastate.foundation"))
	assert.False(t, verifier.Trusts("https://evil.example"))
}
