// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	DeploymentProfileOntology = "d38e0c5b-9d63-4a21-8e8b-1d6b63af64d2"
	BindingDocumentOntology   = "b1d0a8c3-4e5f-6789-0abc-def012345678"
	DeploymentAttestationType = "deployment_attestation_bundle"
	DeploymentAttestationV1   = 1
)

type DeploymentKeyData struct {
	Kind           string `json:"kind"`
	DeploymentName string `json:"deploymentName"`
	Environment    string `json:"environment"`
	DeployerEName  string `json:"deployerEname"`
	PlatformEName  string `json:"platformEname"`
	PublicKey      string `json:"publicKey"`
	Algorithm      string `json:"algorithm"`
}

type SoftwareVersionData struct {
	Kind          string `json:"kind"`
	PlatformEName string `json:"platformEname"`
	VersionEName  string `json:"versionEname"`
	Version       string `json:"version"`
	ReleaseTag    string `json:"releaseTag"`
	CommitSHA     string `json:"commitSha"`
}

type DeploymentBindingDocument struct {
	Subject string `json:"subject"`
	Type    string `json:"type"`
	Data    any    `json:"data"`
}

type DeploymentBundleDocument struct {
	Subject string `json:"subject"`
	Type    string `json:"type"`
	Hash    string `json:"hash"`
}

type DeploymentAttestationBundle struct {
	Type      string                     `json:"type"`
	Version   int                        `json:"version"`
	Documents []DeploymentBundleDocument `json:"documents"`
}

type DeploymentProfile struct {
	DeploymentEName           string `json:"deploymentEname"`
	DeploymentName            string `json:"deploymentName"`
	Environment               string `json:"environment"`
	DeployerEName             string `json:"deployerEname"`
	PlatformEName             string `json:"platformEname"`
	VersionEName              string `json:"versionEname"`
	Version                   string `json:"version"`
	ReleaseTag                string `json:"releaseTag"`
	CommitSHA                 string `json:"commitSha"`
	PublicKey                 string `json:"publicKey"`
	DeploymentKeyDocumentID   string `json:"deploymentKeyDocumentId"`
	SoftwareVersionDocumentID string `json:"softwareVersionDocumentId"`
	CreatedAt                 string `json:"createdAt"`
}

func BuildDeploymentAttestation(deploymentEName, deploymentName, environment, deployerEName, platformEName, versionEName, version, releaseTag, commitSHA, publicKey string) (DeploymentBindingDocument, DeploymentBindingDocument, string, error) {
	values := []string{deploymentEName, deploymentName, environment, deployerEName, platformEName, versionEName, version, releaseTag, commitSHA, publicKey}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return DeploymentBindingDocument{}, DeploymentBindingDocument{}, "", errors.New("deployment attestation fields are required")
		}
	}
	deployment := DeploymentBindingDocument{
		Subject: deploymentEName,
		Type:    "deployment_key",
		Data: DeploymentKeyData{
			Kind: "deployment_key", DeploymentName: deploymentName, Environment: environment,
			DeployerEName: deployerEName, PlatformEName: platformEName,
			PublicKey: publicKey, Algorithm: "ECDSA_P256",
		},
	}
	softwareVersion := DeploymentBindingDocument{
		Subject: versionEName,
		Type:    "software_version",
		Data: SoftwareVersionData{
			Kind: "software_version", PlatformEName: platformEName, VersionEName: versionEName,
			Version: version, ReleaseTag: releaseTag, CommitSHA: strings.ToLower(commitSHA),
		},
	}
	bundle := DeploymentAttestationBundle{
		Type: DeploymentAttestationType, Version: DeploymentAttestationV1,
		Documents: []DeploymentBundleDocument{
			{Subject: deployment.Subject, Type: deployment.Type, Hash: bindingDocumentHash(deployment)},
			{Subject: softwareVersion.Subject, Type: softwareVersion.Type, Hash: bindingDocumentHash(softwareVersion)},
		},
	}
	payload, err := json.Marshal(bundle)
	return deployment, softwareVersion, string(payload), err
}

func bindingDocumentHash(document DeploymentBindingDocument) string {
	raw, _ := json.Marshal(document)
	var canonical map[string]any
	_ = json.Unmarshal(raw, &canonical)
	payload, _ := json.Marshal(canonical)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
