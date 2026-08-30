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
	PPASigningPending   = "pending"
	PPASigningVerifying = "verifying"
	PPASigningCompleted = "completed"
	PPASigningRejected  = "rejected"
	PPASigningExpired   = "expired"
)

// PPASigningSession is a short-lived, one-time wallet signing request. The
// signed statement itself is copied into the repository manifest on success;
// this row only coordinates the browser and wallet callback safely.
type PPASigningSession struct {
	ID               string             `xorm:"pk VARCHAR(128)"`
	RepositoryID     int64              `xorm:"INDEX NOT NULL"`
	UserID           int64              `xorm:"INDEX NOT NULL"`
	Version          string             `xorm:"VARCHAR(255) NOT NULL"`
	ReleaseTag       string             `xorm:"VARCHAR(255) NOT NULL"`
	ManifestCommitID string             `xorm:"VARCHAR(64) NOT NULL"`
	Statement        string             `xorm:"TEXT NOT NULL"`
	Status           string             `xorm:"INDEX VARCHAR(16) NOT NULL"`
	Failure          string             `xorm:"VARCHAR(255)"`
	CreatedUnix      timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix      timeutil.TimeStamp `xorm:"updated"`
	ExpiresUnix      timeutil.TimeStamp `xorm:"INDEX NOT NULL"`
}

func init() {
	db.RegisterModel(new(PPASigningSession))
}

// CreatePPASigningSession persists a wallet request and lazily removes old
// sessions for the same user and repository.
func CreatePPASigningSession(ctx context.Context, session *PPASigningSession) error {
	now := timeutil.TimeStampNow()
	_, _ = db.GetEngine(ctx).
		Where("repository_id = ? AND user_id = ? AND expires_unix <= ?", session.RepositoryID, session.UserID, now).
		Delete(new(PPASigningSession))
	return db.Insert(ctx, session)
}

// GetPPASigningSession returns a session and marks it expired when its deadline
// has passed.
func GetPPASigningSession(ctx context.Context, id string) (*PPASigningSession, error) {
	session, exists, err := db.Get[PPASigningSession](ctx, builder.Eq{"id": id})
	if err != nil || !exists {
		return nil, err
	}
	if session.ExpiresUnix <= timeutil.TimeStampNow() && session.Status != PPASigningCompleted && session.Status != PPASigningRejected {
		session.Status = PPASigningExpired
		_, err = db.GetEngine(ctx).ID(id).Cols("status", "updated_unix").Update(session)
		if err != nil {
			return nil, err
		}
	}
	return session, nil
}

// ClaimPPASigningSession atomically makes a pending session single-use.
func ClaimPPASigningSession(ctx context.Context, id string) (bool, error) {
	result, err := db.GetEngine(ctx).
		Where("id = ? AND status = ? AND expires_unix > ?", id, PPASigningPending, timeutil.TimeStampNow()).
		Cols("status", "updated_unix").
		Update(&PPASigningSession{Status: PPASigningVerifying})
	return err == nil && result > 0, err
}

// FinishPPASigningSession records the terminal state returned to the polling
// repository page.
func FinishPPASigningSession(ctx context.Context, id, status, failure string) error {
	_, err := db.GetEngine(ctx).ID(id).Cols("status", "failure", "updated_unix").Update(&PPASigningSession{
		Status:  status,
		Failure: failure,
	})
	return err
}
