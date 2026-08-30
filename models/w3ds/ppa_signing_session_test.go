// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds_test

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	w3ds_model "forgejo.org/models/w3ds"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPPASigningSessionIsSingleUse(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	session := &w3ds_model.PPASigningSession{
		ID:               "gitw3:ppa:v1:test-single-use",
		RepositoryID:     1,
		UserID:           1,
		Version:          "1.2.3",
		ReleaseTag:       "v1.2.3",
		ManifestCommitID: "0123456789abcdef",
		Statement:        `{}`,
		Status:           w3ds_model.PPASigningPending,
		ExpiresUnix:      timeutil.TimeStampNow() + 600,
	}
	require.NoError(t, w3ds_model.CreatePPASigningSession(db.DefaultContext, session))

	loaded, err := w3ds_model.GetPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, w3ds_model.PPASigningPending, loaded.Status)

	claimed, err := w3ds_model.ClaimPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = w3ds_model.ClaimPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	assert.False(t, claimed)

	require.NoError(t, w3ds_model.FinishPPASigningSession(db.DefaultContext, session.ID, w3ds_model.PPASigningCompleted, ""))
	loaded, err = w3ds_model.GetPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	assert.Equal(t, w3ds_model.PPASigningCompleted, loaded.Status)
}

func TestPPASigningSessionExpires(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	session := &w3ds_model.PPASigningSession{
		ID:               "gitw3:ppa:v1:test-expired",
		RepositoryID:     1,
		UserID:           1,
		Version:          "1.2.3",
		ReleaseTag:       "v1.2.3",
		ManifestCommitID: "0123456789abcdef",
		Statement:        `{}`,
		Status:           w3ds_model.PPASigningPending,
		ExpiresUnix:      timeutil.TimeStampNow() - 1,
	}
	require.NoError(t, w3ds_model.CreatePPASigningSession(db.DefaultContext, session))

	loaded, err := w3ds_model.GetPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, w3ds_model.PPASigningExpired, loaded.Status)
	claimed, err := w3ds_model.ClaimPPASigningSession(db.DefaultContext, session.ID)
	require.NoError(t, err)
	assert.False(t, claimed)
}
