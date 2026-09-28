// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// BuildJob status values.
const (
	BuildQueued    = "queued"
	BuildRunning   = "running"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
	BuildCancelled = "cancelled"
)

// BuildJob is the builder workflow run that produces a deployment's image.
// Rollbacks reuse an existing digest and have no build job.
type BuildJob struct {
	ID           int64  `xorm:"pk autoincr"`
	DeploymentID int64  `xorm:"UNIQUE NOT NULL"`
	RunID        int64  `xorm:"INDEX NOT NULL DEFAULT 0"`
	Runner       string `xorm:"VARCHAR(255)"`
	Status       string `xorm:"VARCHAR(16) NOT NULL"`
	// Nonce is sent to the builder inside the job spec and must accompany the
	// signed callback exactly once.
	Nonce          string             `xorm:"UNIQUE VARCHAR(64) NOT NULL"`
	NonceUsed      bool               `xorm:"NOT NULL DEFAULT false"`
	ScanResultJSON string             `xorm:"TEXT"`
	StartedUnix    timeutil.TimeStamp `xorm:"created"`
	FinishedUnix   timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
}

func (BuildJob) TableName() string { return "hosting_build_job" }

func init() {
	db.RegisterModel(new(BuildJob))
}

var ErrBuildJobNotExist = util.NewNotExistErrorf("hosting build job does not exist")

func CreateBuildJob(ctx context.Context, job *BuildJob) error {
	if job.Status == "" {
		job.Status = BuildQueued
	}
	return db.Insert(ctx, job)
}

func getBuildJob(ctx context.Context, cond builder.Cond) (*BuildJob, error) {
	job, exists, err := db.Get[BuildJob](ctx, cond)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrBuildJobNotExist
	}
	return job, nil
}

func GetBuildJob(ctx context.Context, id int64) (*BuildJob, error) {
	return getBuildJob(ctx, builder.Eq{"id": id})
}

func GetBuildJobByDeployment(ctx context.Context, deploymentID int64) (*BuildJob, error) {
	return getBuildJob(ctx, builder.Eq{"deployment_id": deploymentID})
}

func GetBuildJobByRunID(ctx context.Context, runID int64) (*BuildJob, error) {
	return getBuildJob(ctx, builder.Eq{"run_id": runID})
}

// ConsumeNonce marks a job's callback nonce as used. It returns false if the
// nonce is unknown or was already consumed, which makes callbacks single-use.
func ConsumeNonce(ctx context.Context, jobID int64, nonce string) (bool, error) {
	affected, err := db.GetEngine(ctx).
		Where("id = ? AND nonce = ? AND nonce_used = ?", jobID, nonce, false).
		Cols("nonce_used").Update(&BuildJob{NonceUsed: true})
	return affected > 0, err
}

// UpdateBuildJobCols persists the named columns of a build job.
func UpdateBuildJobCols(ctx context.Context, job *BuildJob, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(job.ID).Cols(cols...).Update(job)
	return err
}

// ListRunningBuildJobs returns jobs that have not finished, for queue alerts.
func ListRunningBuildJobs(ctx context.Context) ([]*BuildJob, error) {
	jobs := make([]*BuildJob, 0, 4)
	return jobs, db.GetEngine(ctx).In("status", []string{BuildQueued, BuildRunning}).Find(&jobs)
}
