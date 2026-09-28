// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// DeploymentKeyFormat matches the w3ds-deployment-key.json files the
// self-hosted deploy wizard lets users download.
const DeploymentKeyFormat = "w3ds-deployment-key-v1"

// DeploymentVersionPayloadPrefix prefixes payloads a deployment key signs to
// move an existing deployment to a new software version without a wallet.
const DeploymentVersionPayloadPrefix = "gitw3:deployment-version:v1:"

// DeploymentKeyFile is the JSON document holding a deployment key pair.
type DeploymentKeyFile struct {
	Format    string `json:"format"`
	Algorithm struct {
		Name       string `json:"name"`
		NamedCurve string `json:"namedCurve"`
		Hash       string `json:"hash"`
	} `json:"algorithm"`
	PublicKey       string `json:"publicKey"`
	PrivateKeyPKCS8 string `json:"privateKeyPkcs8"`
	CreatedAt       string `json:"createdAt"`
}

// GenerateDeploymentKey creates a P-256 deployment key. It returns the
// z-prefixed (base58btc SPKI) public key and the key file JSON.
func GenerateDeploymentKey() (string, string, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	spki, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		return "", "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", "", err
	}
	file := DeploymentKeyFile{
		Format:          DeploymentKeyFormat,
		PublicKey:       "z" + encodeBase58BTC(spki),
		PrivateKeyPKCS8: base64.StdEncoding.EncodeToString(pkcs8),
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	file.Algorithm.Name, file.Algorithm.NamedCurve, file.Algorithm.Hash = "ECDSA", "P-256", "SHA-256"
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", "", err
	}
	return file.PublicKey, string(data) + "\n", nil
}

// DeploymentVersionPayload is what a deployment key signs to publish a new
// version of an already wallet-authorised deployment.
func DeploymentVersionPayload(deploymentEName, version, releaseTag, commitSHA string) (string, error) {
	values := []string{deploymentEName, version, releaseTag, commitSHA}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return "", errors.New("deployment version fields are required")
		}
	}
	document, err := json.Marshal(map[string]string{
		"deploymentEname": deploymentEName, "version": version,
		"releaseTag": releaseTag, "commitSha": strings.ToLower(commitSHA),
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	return DeploymentVersionPayloadPrefix + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// SignWithDeploymentKey signs payload with the key file's private key and
// returns a base64 raw r||s signature, the format WebCrypto produces.
func SignWithDeploymentKey(keyFile, payload string) (string, error) {
	file := DeploymentKeyFile{}
	if err := json.Unmarshal([]byte(keyFile), &file); err != nil {
		return "", fmt.Errorf("decode deployment key: %w", err)
	}
	der, err := base64.StdEncoding.DecodeString(file.PrivateKeyPKCS8)
	if err != nil {
		return "", fmt.Errorf("decode deployment key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return "", fmt.Errorf("parse deployment key: %w", err)
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return "", errors.New("deployment key is not a P-256 key")
	}
	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifyDeploymentKeySignature checks a deployment key signature against the
// z-prefixed public key recorded in the deployment's W3DS binding.
func VerifyDeploymentKeySignature(publicKey, payload, signature string) bool {
	key, err := decodeP256PublicKey(publicKey)
	if err != nil {
		return false
	}
	candidates, err := decodeSignatureCandidates(signature)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(payload))
	for _, candidate := range candidates {
		if verifyECDSASignature(key, digest[:], candidate) {
			return true
		}
	}
	return false
}

func encodeBase58BTC(data []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	number := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	mod := new(big.Int)
	encoded := make([]byte, 0, len(data)*138/100+1)
	for number.Sign() > 0 {
		number.DivMod(number, base, mod)
		encoded = append(encoded, alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		encoded = append(encoded, '1')
	}
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}
