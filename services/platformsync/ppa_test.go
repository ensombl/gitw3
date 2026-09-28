// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"forgejo.org/modules/json"
	"forgejo.org/modules/w3ds"

	"github.com/golang-jwt/jwt/v5"
)

// testPPA is a stand-in for the trusted PPA service: it serves a JWKS and
// signs decisions the way the real PPA does.
var testPPA = struct {
	once   sync.Once
	server *httptest.Server
	key    *ecdsa.PrivateKey
}{}

func testPPAURL() string {
	testPPA.once.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		testPPA.key = key
		testPPA.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "EC", "crv": "P-256", "kid": "ppa-accreditation-key-1", "alg": "ES256",
				"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
				"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
			}}})
		}))
	})
	return testPPA.server.URL
}

// ppaRecord stores a decision the way the PPA writes it to an eVault.
func ppaRecord(decision w3ds.AccreditationDecision) map[string]any {
	issued := time.Now()
	if parsed, err := time.Parse(time.RFC3339, decision.CreatedAt); err == nil {
		issued = parsed
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": testPPAURL(), "sub": decision.PlatformEName, "iat": issued.Unix(),
		"decision": decision.Decision, "level": decision.Level, "statement": decision.Statement,
		"reviewedBy": decision.ReviewedByEName, "platformVersion": decision.PlatformVersion,
	})
	token.Header["kid"] = "ppa-accreditation-key-1"
	jws, err := token.SignedString(testPPA.key)
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"jws": jws, "issuerJwksUri": testPPAURL() + "/.well-known/jwks.json",
		"platformEName": decision.PlatformEName, "platformVersion": decision.PlatformVersion,
		"decision": decision.Decision, "createdAt": decision.CreatedAt,
	}
}
