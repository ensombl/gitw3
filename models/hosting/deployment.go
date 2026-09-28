// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"forgejo.org/models/db"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// Status is a deployment lifecycle state.
type Status string

const (
	StatusQueued                Status = "queued"
	StatusBuilding              Status = "building"
	StatusBuilt                 Status = "built"
	StatusAwaitingSignature     Status = "awaiting_signature"
	StatusAwaitingCertification Status = "awaiting_certification"
	StatusDeploying             Status = "deploying"
	StatusLive                  Status = "live"
	StatusBuildFailed           Status = "build_failed"
	StatusDeployFailed          Status = "deploy_failed"
	StatusRolledBack            Status = "rolled_back"
	StatusSuperseded            Status = "superseded"
	StatusCancelled             Status = "cancelled"
)

var transitions = map[Status][]Status{
	StatusQueued:                {StatusBuilding, StatusAwaitingSignature, StatusAwaitingCertification, StatusDeploying, StatusBuildFailed, StatusDeployFailed, StatusCancelled},
	StatusBuilding:              {StatusBuilt, StatusBuildFailed, StatusCancelled},
	StatusBuilt:                 {StatusAwaitingSignature, StatusAwaitingCertification, StatusDeploying, StatusDeployFailed, StatusCancelled},
	StatusAwaitingSignature:     {StatusAwaitingCertification, StatusDeploying, StatusDeployFailed, StatusCancelled},
	StatusAwaitingCertification: {StatusDeploying, StatusDeployFailed, StatusCancelled},
	StatusDeploying:             {StatusLive, StatusDeployFailed, StatusRolledBack},
	StatusDeployFailed:          {StatusRolledBack},
	StatusLive:                  {StatusSuperseded},
}

// CanTransition reports whether the lifecycle allows moving from one status to another.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

// IsFinal reports whether no further transitions are possible.
func (s Status) IsFinal() bool {
	return len(transitions[s]) == 0 || s == StatusDeployFailed
}

// IsPending reports whether the deployment has not reached the cluster yet
// and may therefore be superseded by a newer one.
func (s Status) IsPending() bool {
	switch s {
	case StatusQueued, StatusBuilding, StatusBuilt, StatusAwaitingSignature, StatusAwaitingCertification:
		return true
	}
	return false
}

// PendingStatuses are the states a newer deployment cancels.
var PendingStatuses = []Status{StatusQueued, StatusBuilding, StatusBuilt, StatusAwaitingSignature, StatusAwaitingCertification}

// Trigger says what started a deployment.
type Trigger string

const (
	TriggerManual   Trigger = "manual"
	TriggerRelease  Trigger = "release"
	TriggerRollback Trigger = "rollback"
)

// Deployment is one attempt to put a tagged release of a target live.
type Deployment struct {
	ID          int64   `xorm:"pk autoincr"`
	TargetID    int64   `xorm:"INDEX NOT NULL"`
	RepoID      int64   `xorm:"INDEX NOT NULL"`
	ActorID     int64   `xorm:"NOT NULL"`
	ReleaseID   int64   `xorm:"NOT NULL"`
	TagName     string  `xorm:"VARCHAR(255) NOT NULL"`
	CommitSHA   string  `xorm:"VARCHAR(64) NOT NULL"`
	ImageDigest string  `xorm:"VARCHAR(128)"`
	ImagesJSON  string  `xorm:"TEXT"`
	Status      Status  `xorm:"INDEX VARCHAR(32) NOT NULL"`
	Trigger     Trigger `xorm:"VARCHAR(16) NOT NULL"`
	RollbackOf  int64   `xorm:"NOT NULL DEFAULT 0"`
	// EnvSnapshotEnc is the encrypted environment as deployed, so a rollback
	// restores configuration as well as code.
	EnvSnapshotEnc []byte             `xorm:"BLOB"`
	Error          string             `xorm:"TEXT"`
	Warning        string             `xorm:"TEXT"`
	StartedUnix    timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix    timeutil.TimeStamp `xorm:"updated INDEX"`
	FinishedUnix   timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
}

func (Deployment) TableName() string { return "hosting_deployment" }

func init() {
	db.RegisterModel(new(Deployment))
}

// Images returns the service → digest reference map of a compose deployment.
func (d *Deployment) Images() (map[string]string, error) {
	images := map[string]string{}
	if d.ImagesJSON == "" {
		return images, nil
	}
	return images, json.Unmarshal([]byte(d.ImagesJSON), &images)
}

// SetEnvSnapshot encrypts the env that is about to be deployed. The row must already exist.
func (d *Deployment) SetEnvSnapshot(env map[string]string) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	d.EnvSnapshotEnc = keying.HostingEnvVar.Encrypt(data, keying.ColumnAndID("env_snapshot", d.ID))
	return nil
}

// EnvSnapshot decrypts the env recorded for this deployment.
func (d *Deployment) EnvSnapshot() (map[string]string, error) {
	env := map[string]string{}
	if len(d.EnvSnapshotEnc) == 0 {
		return env, nil
	}
	plain, err := keying.HostingEnvVar.Decrypt(d.EnvSnapshotEnc, keying.ColumnAndID("env_snapshot", d.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt env snapshot of deployment %d: %w", d.ID, err)
	}
	return env, json.Unmarshal(plain, &env)
}

var ErrDeploymentNotExist = util.NewNotExistErrorf("hosting deployment does not exist")

func CreateDeployment(ctx context.Context, deployment *Deployment) error {
	if deployment.Status == "" {
		deployment.Status = StatusQueued
	}
	return db.Insert(ctx, deployment)
}

func GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	deployment, exists, err := db.Get[Deployment](ctx, builder.Eq{"id": id})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrDeploymentNotExist
	}
	return deployment, nil
}

func ListDeployments(ctx context.Context, targetID int64, limit int) ([]*Deployment, error) {
	deployments := make([]*Deployment, 0, limit)
	return deployments, db.GetEngine(ctx).Where("target_id = ?", targetID).Desc("id").Limit(limit).Find(&deployments)
}

func ListDeploymentsByStatus(ctx context.Context, statuses ...Status) ([]*Deployment, error) {
	deployments := make([]*Deployment, 0, 8)
	return deployments, db.GetEngine(ctx).In("status", statuses).Asc("id").Find(&deployments)
}

func ListPendingDeployments(ctx context.Context, targetID, exceptID int64) ([]*Deployment, error) {
	deployments := make([]*Deployment, 0, 2)
	return deployments, db.GetEngine(ctx).
		Where("target_id = ? AND id <> ?", targetID, exceptID).
		In("status", PendingStatuses).
		Find(&deployments)
}

// DeploymentsWithDigest lists deployments of a target that reference a digest,
// used by registry GC to decide what must be kept.
func ListDeploymentDigests(ctx context.Context, targetID int64) ([]*Deployment, error) {
	deployments := make([]*Deployment, 0, 16)
	return deployments, db.GetEngine(ctx).
		Where("target_id = ? AND image_digest <> ''", targetID).
		Cols("id", "target_id", "image_digest", "images_json", "status").
		Desc("id").Find(&deployments)
}

// Transition atomically moves a deployment to a new status if it is still in
// one of the statuses that allow it. Extra columns set on update are written
// in the same statement. It returns false when another actor won the race.
func Transition(ctx context.Context, deployment *Deployment, to Status, cols ...string) (bool, error) {
	from := make([]Status, 0, 4)
	for status, next := range transitions {
		if slices.Contains(next, to) {
			from = append(from, status)
		}
	}
	update := *deployment
	update.Status = to
	if to.IsFinal() || to == StatusLive {
		update.FinishedUnix = timeutil.TimeStampNow()
		cols = append(cols, "finished_unix")
	}
	affected, err := db.GetEngine(ctx).ID(deployment.ID).In("status", from).
		Cols(append(cols, "status", "updated_unix")...).Update(&update)
	if err != nil || affected == 0 {
		return false, err
	}
	*deployment = update
	return true, nil
}

// UpdateDeploymentCols persists columns without changing status.
func UpdateDeploymentCols(ctx context.Context, deployment *Deployment, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(deployment.ID).Cols(cols...).Update(deployment)
	return err
}
