// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"encoding/json"

	"forgejo.org/models/db"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"
)

// Audit actions.
const (
	AuditTargetEnabled     = "target.enabled"
	AuditTargetUpdated     = "target.updated"
	AuditTargetDeleted     = "target.deleted"
	AuditDeployRequested   = "deployment.requested"
	AuditDeployCancelled   = "deployment.cancelled"
	AuditDeployBuilt       = "deployment.built"
	AuditDeployLive        = "deployment.live"
	AuditDeployFailed      = "deployment.failed"
	AuditRollbackRequested = "deployment.rollback"
	AuditEnvSet            = "env.set"
	AuditEnvDeleted        = "env.deleted"
	AuditDomainAttached    = "domain.attached"
	AuditDomainVerified    = "domain.verified"
	AuditDomainRemoved     = "domain.removed"
	AuditSigned            = "w3ds.signed"
)

// AuditEvent is an append-only record of who did what to a hosting target.
type AuditEvent struct {
	ID           int64              `xorm:"pk autoincr"`
	ActorID      int64              `xorm:"INDEX NOT NULL"`
	RepoID       int64              `xorm:"INDEX NOT NULL"`
	TargetID     int64              `xorm:"INDEX NOT NULL"`
	DeploymentID int64              `xorm:"INDEX NOT NULL DEFAULT 0"`
	Action       string             `xorm:"VARCHAR(64) NOT NULL"`
	Payload      string             `xorm:"TEXT"`
	CreatedUnix  timeutil.TimeStamp `xorm:"created INDEX"`
}

func (AuditEvent) TableName() string { return "hosting_audit_event" }

func init() {
	db.RegisterModel(new(AuditEvent))
}

// Audit records an event. Failures are logged rather than returned so the
// audit trail never blocks an operation that already happened.
func Audit(ctx context.Context, actorID, repoID, targetID, deploymentID int64, action string, payload map[string]any) {
	event := &AuditEvent{ActorID: actorID, RepoID: repoID, TargetID: targetID, DeploymentID: deploymentID, Action: action}
	if len(payload) > 0 {
		data, err := json.Marshal(payload)
		if err == nil {
			event.Payload = string(data)
		}
	}
	if err := db.Insert(ctx, event); err != nil {
		log.Error("hosting audit %s on target %d: %v", action, targetID, err)
	}
}

func ListAuditEvents(ctx context.Context, repoID int64, limit int) ([]*AuditEvent, error) {
	events := make([]*AuditEvent, 0, limit)
	return events, db.GetEngine(ctx).Where("repo_id = ?", repoID).Desc("id").Limit(limit).Find(&events)
}
