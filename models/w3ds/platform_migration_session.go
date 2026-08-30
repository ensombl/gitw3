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
	MigrationSigningPending   = "pending"
	MigrationSigningVerifying = "verifying"
	MigrationSigningReview    = "review"
	MigrationSigningCompleted = "completed"
	MigrationSigningRejected  = "rejected"
	MigrationSigningExpired   = "expired"
)

type PlatformMigrationSession struct {
	ID                string             `xorm:"pk VARCHAR(128)"`
	UserID            int64              `xorm:"INDEX NOT NULL"`
	OwnerID           int64              `xorm:"INDEX NOT NULL"`
	RepositoryID      int64              `xorm:"INDEX"`
	RepositoryName    string             `xorm:"VARCHAR(100) NOT NULL"`
	DefaultBranch     string             `xorm:"VARCHAR(100) NOT NULL"`
	IsPrivate         bool               `xorm:"NOT NULL"`
	EName             string             `xorm:"INDEX VARCHAR(255) NOT NULL"`
	ProfileEnvelopeID string             `xorm:"VARCHAR(255) NOT NULL"`
	ProfileDigest     string             `xorm:"VARCHAR(64) NOT NULL"`
	TokenFingerprint  string             `xorm:"VARCHAR(64) NOT NULL"`
	Profile           string             `xorm:"LONGTEXT NOT NULL"`
	AuthorENames      string             `xorm:"TEXT"`
	Statement         string             `xorm:"TEXT NOT NULL"`
	Proof             string             `xorm:"LONGTEXT"`
	Status            string             `xorm:"INDEX VARCHAR(16) NOT NULL"`
	Failure           string             `xorm:"VARCHAR(255)"`
	CreatedUnix       timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated"`
	ExpiresUnix       timeutil.TimeStamp `xorm:"INDEX NOT NULL"`
}

func init() {
	db.RegisterModel(new(PlatformMigrationSession))
}

func CreatePlatformMigrationSession(ctx context.Context, session *PlatformMigrationSession) error {
	now := timeutil.TimeStampNow()
	_, _ = db.GetEngine(ctx).Where("user_id = ? AND expires_unix <= ?", session.UserID, now).Delete(new(PlatformMigrationSession))
	return db.Insert(ctx, session)
}

func GetPlatformMigrationSession(ctx context.Context, id string) (*PlatformMigrationSession, error) {
	session, exists, err := db.Get[PlatformMigrationSession](ctx, builder.Eq{"id": id})
	if err != nil || !exists {
		return nil, err
	}
	if session.ExpiresUnix <= timeutil.TimeStampNow() && session.Status != MigrationSigningCompleted && session.Status != MigrationSigningRejected {
		session.Status = MigrationSigningExpired
		_, err = db.GetEngine(ctx).ID(id).Cols("status", "updated_unix").Update(session)
	}
	return session, err
}

func ClaimPlatformMigrationSession(ctx context.Context, id string) (bool, error) {
	result, err := db.GetEngine(ctx).
		Where("id = ? AND status = ? AND expires_unix > ?", id, MigrationSigningPending, timeutil.TimeStampNow()).
		Cols("status", "updated_unix").Update(&PlatformMigrationSession{Status: MigrationSigningVerifying})
	return err == nil && result > 0, err
}

func UpdatePlatformMigrationSession(ctx context.Context, session *PlatformMigrationSession, columns ...string) error {
	columns = append(columns, "updated_unix")
	_, err := db.GetEngine(ctx).ID(session.ID).Cols(columns...).Update(session)
	return err
}

func PendingPlatformMigrationReviews(ctx context.Context) ([]*PlatformMigrationSession, error) {
	sessions := make([]*PlatformMigrationSession, 0)
	err := db.GetEngine(ctx).Where("status = ?", MigrationSigningReview).Asc("created_unix").Find(&sessions)
	return sessions, err
}
