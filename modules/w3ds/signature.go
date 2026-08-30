// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const ProductionRegistryURL = "https://registry.w3ds.metastate.foundation"

const maxSignatureResponseSize = 1 << 20

// SignatureVerification is the durable evidence returned after a wallet
// signature is checked against a Registry-issued eVault key binding.
type SignatureVerification struct {
	Valid                 bool
	PublicKey             string
	KeyBindingCertificate string
}

type registryResolution struct {
	URI string `json:"uri"`
}

type whoisResponse struct {
	KeyBindingCertificates []string `json:"keyBindingCertificates"`
}

type registryJWKS struct {
	Keys []registryJWK `json:"keys"`
}

type registryJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	KID string `json:"kid"`
	ALG string `json:"alg"`
}

type keyBindingClaims struct {
	EName     string `json:"ename"`
	PublicKey string `json:"publicKey"`
	jwt.RegisteredClaims
}

// VerifyWalletSignature resolves an eName, validates the Registry-signed key
// bindings in its eVault, and verifies an ECDSA P-256 signature over payload.
func VerifyWalletSignature(ctx context.Context, client *http.Client, registryBaseURL, eName, signature, payload string) (*SignatureVerification, error) {
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	registryBaseURL = strings.TrimRight(strings.TrimSpace(registryBaseURL), "/")
	registryURL, err := url.Parse(registryBaseURL)
	if err != nil || (registryURL.Scheme != "https" && registryURL.Scheme != "http") || registryURL.Host == "" {
		return nil, errors.New("Registry URL must be an absolute HTTP URL")
	}
	eName = strings.TrimSpace(eName)
	signature = strings.TrimSpace(signature)
	if eName == "" || signature == "" || payload == "" {
		return nil, errors.New("eName, signature, and payload are required")
	}
	if len(eName) > 512 || len(signature) > 8192 || len(payload) > 8192 {
		return nil, errors.New("signature request is too large")
	}

	resolveURL := registryBaseURL + "/resolve?w3id=" + url.QueryEscape(eName)
	var resolution registryResolution
	if err := getSignatureJSON(ctx, client, resolveURL, nil, &resolution); err != nil {
		return nil, fmt.Errorf("resolve signer eVault: %w", err)
	}
	evaultURL, err := url.Parse(strings.TrimSpace(resolution.URI))
	if err != nil || (evaultURL.Scheme != "https" && evaultURL.Scheme != "http") || evaultURL.Host == "" {
		return nil, errors.New("Registry returned an invalid eVault URL")
	}
	whoisURL := evaultURL.ResolveReference(&url.URL{Path: "/whois"}).String()
	var whois whoisResponse
	if err := getSignatureJSON(ctx, client, whoisURL, map[string]string{"X-ENAME": eName}, &whois); err != nil {
		return nil, fmt.Errorf("load signer key bindings: %w", err)
	}
	if len(whois.KeyBindingCertificates) == 0 {
		return nil, errors.New("signer eVault has no key binding certificates")
	}

	var jwks registryJWKS
	if err := getSignatureJSON(ctx, client, registryBaseURL+"/.well-known/jwks.json", nil, &jwks); err != nil {
		return nil, fmt.Errorf("load Registry signing keys: %w", err)
	}
	registryKeys, err := parseRegistryKeys(jwks)
	if err != nil {
		return nil, err
	}
	signatures, err := decodeSignatureCandidates(signature)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(payload))

	for _, certificate := range whois.KeyBindingCertificates {
		claims := &keyBindingClaims{}
		token, err := jwt.ParseWithClaims(certificate, claims, func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			key := registryKeys[kid]
			if key == nil {
				return nil, errors.New("key binding certificate uses an unknown Registry key")
			}
			return key, nil
		}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithExpirationRequired())
		if err != nil || !token.Valid || claims.EName != eName || claims.PublicKey == "" {
			continue
		}
		publicKey, err := decodeP256PublicKey(claims.PublicKey)
		if err != nil {
			continue
		}
		for _, candidate := range signatures {
			if verifyECDSASignature(publicKey, digest[:], candidate) {
				return &SignatureVerification{
					Valid:                 true,
					PublicKey:             claims.PublicKey,
					KeyBindingCertificate: certificate,
				}, nil
			}
		}
	}

	return &SignatureVerification{Valid: false}, nil
}

func getSignatureJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxSignatureResponseSize))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func parseRegistryKeys(jwks registryJWKS) (map[string]*ecdsa.PublicKey, error) {
	keys := make(map[string]*ecdsa.PublicKey, len(jwks.Keys))
	for _, jwk := range jwks.Keys {
		if jwk.KTY != "EC" || jwk.CRV != "P-256" || jwk.KID == "" || (jwk.ALG != "" && jwk.ALG != "ES256") {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(jwk.X)
		y, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
		if errX != nil || errY != nil {
			continue
		}
		key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if key.Curve.IsOnCurve(key.X, key.Y) {
			keys[jwk.KID] = key
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("Registry JWKS contains no usable ES256 keys")
	}
	return keys, nil
}

func decodeP256PublicKey(encoded string) (*ecdsa.PublicKey, error) {
	decoded, err := decodeMultibaseOrHex(encoded)
	if err != nil {
		return nil, err
	}
	if len(decoded) == 65 && decoded[0] == 4 {
		x, y := elliptic.Unmarshal(elliptic.P256(), decoded)
		if x == nil || y == nil {
			return nil, errors.New("invalid raw P-256 public key")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(decoded)
	if err != nil {
		return nil, fmt.Errorf("parse P-256 public key: %w", err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return nil, errors.New("key binding does not contain a P-256 public key")
	}
	return publicKey, nil
}

func decodeMultibaseOrHex(value string) ([]byte, error) {
	switch {
	case strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X"):
		return hex.DecodeString(value[2:])
	case strings.HasPrefix(value, "f"):
		return hex.DecodeString(value[1:])
	case strings.HasPrefix(value, "m"):
		return decodeBase64(value[1:])
	case strings.HasPrefix(value, "z"):
		if decoded, err := hex.DecodeString(value[1:]); err == nil {
			return decoded, nil
		}
		return decodeBase58BTC(value[1:])
	default:
		return nil, errors.New("unsupported public key encoding")
	}
}

func decodeSignatureCandidates(signature string) ([][]byte, error) {
	candidates := make([][]byte, 0, 2)
	if decoded, err := decodeBase64(signature); err == nil {
		candidates = append(candidates, decoded)
	}
	if strings.HasPrefix(signature, "z") {
		if decoded, err := decodeBase58BTC(signature[1:]); err == nil {
			candidates = append(candidates, decoded)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("signature is not valid base64 or base58btc")
	}
	return candidates, nil
}

func decodeBase64(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func decodeBase58BTC(value string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	if value == "" || len(value) > 16384 {
		return nil, errors.New("invalid base58btc value")
	}
	number := new(big.Int)
	base := big.NewInt(58)
	for _, character := range value {
		index := strings.IndexRune(alphabet, character)
		if index < 0 {
			return nil, errors.New("invalid base58btc character")
		}
		number.Mul(number, base)
		number.Add(number, big.NewInt(int64(index)))
	}
	decoded := number.Bytes()
	leadingZeros := 0
	for leadingZeros < len(value) && value[leadingZeros] == '1' {
		leadingZeros++
	}
	return append(make([]byte, leadingZeros), decoded...), nil
}

func verifyECDSASignature(publicKey *ecdsa.PublicKey, digest, signature []byte) bool {
	if len(signature) == 64 {
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		return ecdsa.Verify(publicKey, digest, r, s)
	}
	return ecdsa.VerifyASN1(publicKey, digest, signature)
}
