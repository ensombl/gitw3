// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

const (
	DeploymentAwaitingSignature = "awaiting_signature"
	DeploymentPublishing        = "publishing"
	DeploymentCompleted         = "completed"
	DeploymentFailed            = "failed"
	DeploymentExpired           = "expired"
)

// Deployment is a repository release deployed by one GitW3 user. DeployerEName
// is immutable so a later wallet relink cannot rewrite historical authorship.
type Deployment struct {
	ID                        string             `xorm:"pk VARCHAR(64)"`
	SigningPayload            string             `xorm:"UNIQUE VARCHAR(128) NOT NULL"`
	RepositoryID              int64              `xorm:"INDEX NOT NULL"`
	UserID                    int64              `xorm:"INDEX NOT NULL"`
	DeployerEName             string             `xorm:"VARCHAR(255) NOT NULL"`
	Name                      string             `xorm:"VARCHAR(255) NOT NULL"`
	Environment               string             `xorm:"VARCHAR(255) NOT NULL"`
	ReleaseID                 int64              `xorm:"NOT NULL"`
	Version                   string             `xorm:"VARCHAR(255) NOT NULL"`
	ReleaseTag                string             `xorm:"VARCHAR(255) NOT NULL"`
	CommitSHA                 string             `xorm:"VARCHAR(64) NOT NULL"`
	PlatformEName             string             `xorm:"VARCHAR(255) NOT NULL"`
	VersionEName              string             `xorm:"VARCHAR(255) NOT NULL"`
	DeploymentEName           string             `xorm:"VARCHAR(255) NOT NULL"`
	PublicKey                 string             `xorm:"TEXT NOT NULL"`
	BundlePayload             string             `xorm:"TEXT NOT NULL"`
	WalletSignature           string             `xorm:"TEXT"`
	WalletPublicKey           string             `xorm:"TEXT"`
	KeyBindingCertificate     string             `xorm:"TEXT"`
	DeploymentKeyDocumentID   string             `xorm:"VARCHAR(64)"`
	SoftwareVersionDocumentID string             `xorm:"VARCHAR(64)"`
	Status                    string             `xorm:"INDEX VARCHAR(32) NOT NULL"`
	Failure                   string             `xorm:"TEXT"`
	CreatedUnix               timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix               timeutil.TimeStamp `xorm:"updated"`
	ExpiresUnix               timeutil.TimeStamp `xorm:"INDEX NOT NULL"`
}

func init() {
	db.RegisterModel(new(Deployment))
}

func CreateDeployment(ctx context.Context, deployment *Deployment) error {
	return db.Insert(ctx, deployment)
}

func GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	deployment, exists, err := db.Get[Deployment](ctx, builder.Eq{"id": id})
	if err != nil || !exists {
		return nil, err
	}
	return deployment, nil
}

func GetDeploymentBySigningPayload(ctx context.Context, payload string) (*Deployment, error) {
	deployment, exists, err := db.Get[Deployment](ctx, builder.Eq{"signing_payload": payload})
	if err != nil || !exists {
		return nil, err
	}
	if deployment.ExpiresUnix <= timeutil.TimeStampNow() && deployment.Status == DeploymentAwaitingSignature {
		deployment.Status = DeploymentExpired
		_, err = db.GetEngine(ctx).ID(deployment.ID).Cols("status", "updated_unix").Update(deployment)
		if err != nil {
			return nil, err
		}
	}
	return deployment, nil
}

func ListDeploymentsForUser(ctx context.Context, repositoryID, userID int64) ([]*Deployment, error) {
	deployments := make([]*Deployment, 0)
	err := db.GetEngine(ctx).
		Where("repository_id = ? AND user_id = ? AND wallet_signature IS NOT NULL AND wallet_signature <> ''", repositoryID, userID).
		Desc("created_unix").
		Find(&deployments)
	for _, deployment := range deployments {
		if deployment.Status == DeploymentFailed {
			deployment.Status = DeploymentPublishing
			deployment.Failure = ""
		}
	}
	return deployments, err
}

func ClaimDeploymentSignature(ctx context.Context, payload string) (bool, error) {
	result, err := db.GetEngine(ctx).
		Where("signing_payload = ? AND status = ? AND expires_unix > ?", payload, DeploymentAwaitingSignature, timeutil.TimeStampNow()).
		Cols("status", "updated_unix").
		Update(&Deployment{Status: DeploymentPublishing})
	return err == nil && result > 0, err
}

func RecordDeploymentSignature(ctx context.Context, id, signature, publicKey, certificate string) error {
	_, err := db.GetEngine(ctx).ID(id).
		Cols("wallet_signature", "wallet_public_key", "key_binding_certificate", "updated_unix").
		Update(&Deployment{
			WalletSignature: signature, WalletPublicKey: publicKey,
			KeyBindingCertificate: certificate,
		})
	return err
}

func UpdateDeploymentPublication(ctx context.Context, id, status, failure, deploymentDocumentID, versionDocumentID string) error {
	_, err := db.GetEngine(ctx).ID(id).
		Cols("status", "failure", "deployment_key_document_id", "software_version_document_id", "updated_unix").
		Update(&Deployment{
			Status: status, Failure: failure,
			DeploymentKeyDocumentID:   deploymentDocumentID,
			SoftwareVersionDocumentID: versionDocumentID,
		})
	return err
}
