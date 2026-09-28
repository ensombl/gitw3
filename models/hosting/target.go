// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hosting stores GitW3 managed hosting state: deploy targets, their
// deployments, build jobs, environment, domains and the audit trail.
package hosting

import (
	"context"
	"encoding/json"
	"fmt"

	"forgejo.org/models/db"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// Target is one deployable unit of a repository, created from deploy.yml.
type Target struct {
	ID               int64  `xorm:"pk autoincr"`
	RepoID           int64  `xorm:"UNIQUE(repo_name) INDEX NOT NULL"`
	Name             string `xorm:"UNIQUE(repo_name) VARCHAR(64) NOT NULL"`
	Kind             string `xorm:"VARCHAR(16) NOT NULL"`
	ConfigJSON       string `xorm:"TEXT"`
	DokployAppID     string `xorm:"VARCHAR(128)"`
	DokployComposeID string `xorm:"VARCHAR(128)"`
	Replicas         int    `xorm:"NOT NULL DEFAULT 1"`
	AutoDeploy       bool   `xorm:"INDEX NOT NULL DEFAULT false"`
	// W3DSDeploymentID links the wallet-signed W3DS deployment record created
	// on the first managed deploy; later versions are signed with the target's
	// deployment key instead of the wallet.
	W3DSDeploymentID string `xorm:"'w3ds_deployment_id' VARCHAR(64)"`
	DeploymentEName  string `xorm:"'deployment_ename' VARCHAR(255)"`
	// W3DSVersion is the software version currently published for the W3DS deployment.
	W3DSVersion      string             `xorm:"'w3ds_version' VARCHAR(255)"`
	PublicKey        string             `xorm:"TEXT"`
	PrivateKeyEnc    []byte             `xorm:"BLOB"`
	LiveDeploymentID int64              `xorm:"NOT NULL DEFAULT 0"`
	CreatedUnix      timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix      timeutil.TimeStamp `xorm:"updated"`
}

// TableName keeps hosting tables clear of the existing W3DS deployment table.
func (Target) TableName() string { return "hosting_target" }

func init() {
	db.RegisterModel(new(Target))
}

// Spec returns the target's parsed deploy.yml entry.
func (t *Target) Spec() (*hosting_module.Target, error) {
	spec := &hosting_module.Target{}
	if err := json.Unmarshal([]byte(t.ConfigJSON), spec); err != nil {
		return nil, fmt.Errorf("decode target %d config: %w", t.ID, err)
	}
	return spec, nil
}

// SetSpec stores a validated deploy.yml entry on the target.
func (t *Target) SetSpec(spec *hosting_module.Target) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	t.ConfigJSON = string(data)
	t.Kind = spec.Kind
	t.Replicas = spec.Replicas
	t.AutoDeploy = spec.AutoDeploy
	return nil
}

// SetPrivateKey encrypts the target's deployment private key. The row must already exist.
func (t *Target) SetPrivateKey(pem string) {
	t.PrivateKeyEnc = keying.HostingDeploymentKey.Encrypt([]byte(pem), keying.ColumnAndID("private_key", t.ID))
}

// PrivateKey decrypts the target's deployment private key.
func (t *Target) PrivateKey() (string, error) {
	if len(t.PrivateKeyEnc) == 0 {
		return "", nil
	}
	plain, err := keying.HostingDeploymentKey.Decrypt(t.PrivateKeyEnc, keying.ColumnAndID("private_key", t.ID))
	if err != nil {
		return "", fmt.Errorf("decrypt deployment key of target %d: %w", t.ID, err)
	}
	return string(plain), nil
}

// ErrTargetNotExist means the target is missing.
var ErrTargetNotExist = util.NewNotExistErrorf("hosting target does not exist")

func CreateTarget(ctx context.Context, target *Target) error {
	return db.Insert(ctx, target)
}

func GetTarget(ctx context.Context, id int64) (*Target, error) {
	target, exists, err := db.Get[Target](ctx, builder.Eq{"id": id})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrTargetNotExist
	}
	return target, nil
}

func GetTargetByRepoAndName(ctx context.Context, repoID int64, name string) (*Target, error) {
	target, exists, err := db.Get[Target](ctx, builder.Eq{"repo_id": repoID, "name": name})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrTargetNotExist
	}
	return target, nil
}

func ListTargets(ctx context.Context, repoID int64) ([]*Target, error) {
	targets := make([]*Target, 0, 4)
	return targets, db.GetEngine(ctx).Where("repo_id = ?", repoID).Asc("name").Find(&targets)
}

func ListAutoDeployTargets(ctx context.Context, repoID int64) ([]*Target, error) {
	targets := make([]*Target, 0, 4)
	return targets, db.GetEngine(ctx).Where("repo_id = ? AND auto_deploy = ?", repoID, true).Find(&targets)
}

// UpdateTargetCols persists the named columns of a target.
func UpdateTargetCols(ctx context.Context, target *Target, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(target.ID).Cols(cols...).Update(target)
	return err
}

// DeleteTarget removes a target together with its env vars. Deployments and
// audit events are kept for history.
func DeleteTarget(ctx context.Context, id int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := db.GetEngine(ctx).Where("target_id = ?", id).Delete(new(EnvVar)); err != nil {
			return err
		}
		_, err := db.GetEngine(ctx).ID(id).Delete(new(Target))
		return err
	})
}

// DeleteTargetsByRepo is used when a repository is deleted.
func DeleteTargetsByRepo(ctx context.Context, repoID int64) error {
	targets, err := ListTargets(ctx, repoID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := DeleteTarget(ctx, target.ID); err != nil {
			return err
		}
	}
	return nil
}

// GetTargetByW3DSDeployment finds the target a W3DS deployment record was prepared for.
func GetTargetByW3DSDeployment(ctx context.Context, w3dsDeploymentID string) (*Target, error) {
	target, exists, err := db.Get[Target](ctx, builder.Eq{"w3ds_deployment_id": w3dsDeploymentID})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrTargetNotExist
	}
	return target, nil
}

// ListAllTargets returns every target on the instance.
func ListAllTargets(ctx context.Context) ([]*Target, error) {
	targets := make([]*Target, 0, 16)
	return targets, db.GetEngine(ctx).Asc("id").Find(&targets)
}
