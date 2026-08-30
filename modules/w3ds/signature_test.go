// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyWalletSignature(t *testing.T) {
	registryKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	walletKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	spki, err := x509.MarshalPKIXPublicKey(&walletKey.PublicKey)
	require.NoError(t, err)
	publicKey := "m" + base64.RawStdEncoding.EncodeToString(spki)
	eName := "@wallet-owner"
	payload := "gitw3:ppa:v1:release-statement"

	certificate := jwt.NewWithClaims(jwt.SigningMethodES256, keyBindingClaims{
		EName:     eName,
		PublicKey: publicKey,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	})
	certificate.Header["kid"] = "registry-key"
	signedCertificate, err := certificate.SignedString(registryKey)
	require.NoError(t, err)

	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, walletKey, digest[:])
	require.NoError(t, err)
	rawSignature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	signature := base64.StdEncoding.EncodeToString(rawSignature)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/resolve":
			assert.Equal(t, eName, request.URL.Query().Get("w3id"))
			json.NewEncoder(response).Encode(map[string]string{"uri": server.URL})
		case "/whois":
			assert.Equal(t, eName, request.Header.Get("X-ENAME"))
			json.NewEncoder(response).Encode(whoisResponse{KeyBindingCertificates: []string{signedCertificate}})
		case "/.well-known/jwks.json":
			json.NewEncoder(response).Encode(registryJWKS{Keys: []registryJWK{{
				KTY: "EC", CRV: "P-256", KID: "registry-key", ALG: "ES256",
				X: base64.RawURLEncoding.EncodeToString(registryKey.X.FillBytes(make([]byte, 32))),
				Y: base64.RawURLEncoding.EncodeToString(registryKey.Y.FillBytes(make([]byte, 32))),
			}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := VerifyWalletSignature(context.Background(), server.Client(), server.URL, eName, signature, payload)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, publicKey, result.PublicKey)
	assert.Equal(t, signedCertificate, result.KeyBindingCertificate)

	wrongDigest := sha256.Sum256([]byte("wrong"))
	wrongR, wrongS, err := ecdsa.Sign(rand.Reader, walletKey, wrongDigest[:])
	require.NoError(t, err)
	wrongSignature := append(wrongR.FillBytes(make([]byte, 32)), wrongS.FillBytes(make([]byte, 32))...)
	result, err = VerifyWalletSignature(context.Background(), server.Client(), server.URL, eName, base64.StdEncoding.EncodeToString(wrongSignature), payload)
	require.NoError(t, err)
	assert.False(t, result.Valid)
}

func TestVerifyWalletSignatureRejectsMissingBindings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/resolve" {
			fmt.Fprintf(response, `{"uri":%q}`, "http://"+request.Host)
			return
		}
		json.NewEncoder(response).Encode(whoisResponse{})
	}))
	defer server.Close()

	_, err := VerifyWalletSignature(context.Background(), server.Client(), server.URL, "@owner", base64.StdEncoding.EncodeToString(make([]byte, 64)), "payload")
	assert.ErrorContains(t, err, "no key binding certificates")
}

func TestDecodeBase58BTC(t *testing.T) {
	decoded, err := decodeBase58BTC("11ZiCa")
	require.NoError(t, err)
	assert.Equal(t, append([]byte{0, 0}, new(big.Int).SetBytes([]byte("abc")).Bytes()...), decoded)
	_, err = decodeBase58BTC("0")
	assert.Error(t, err)
}
