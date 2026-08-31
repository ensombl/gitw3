// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulePreservesBackoffAndDiagnosticsForDuplicates(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	job, err := store.Get(42)
	require.NoError(t, err)
	job.Status = StatusFailed
	job.Attempts = 4
	job.LastError = "eVault mutation: rejected"
	job.LastErrorStage = "eVault mutation"
	job.LastErrorCode = "FORBIDDEN"
	job.NextAttempt = time.Now().UTC().Add(time.Hour)
	require.NoError(t, store.Save(job))
	originalNextAttempt := job.NextAttempt

	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-1", false))
	duplicate, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, duplicate.Status)
	assert.Equal(t, 4, duplicate.Attempts)
	assert.Equal(t, "eVault mutation: rejected", duplicate.LastError)
	assert.Equal(t, "eVault mutation", duplicate.LastErrorStage)
	assert.Equal(t, "FORBIDDEN", duplicate.LastErrorCode)
	assert.Equal(t, originalNextAttempt, duplicate.NextAttempt)
	assert.Equal(t, job.LastScheduleKey, duplicate.LastScheduleKey)

	require.NoError(t, store.Schedule(42, "alice/platform", "main", "commit-2", false))
	newer, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, "commit-2", newer.TargetSHA)
	assert.Equal(t, StatusFailed, newer.Status, "the prior failure remains visible until the new attempt progresses")
	assert.Equal(t, 4, newer.Attempts)
	assert.Equal(t, "eVault mutation: rejected", newer.LastError)
	assert.False(t, newer.NextAttempt.After(time.Now().UTC()))
}

func TestScheduleEventCoalescesDuplicateReleaseDelivery(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.ScheduleEvent(42, "alice/platform", "main", "", false, "release-delivery"))
	job, err := store.Get(42)
	require.NoError(t, err)
	job.Status = StatusFailed
	job.Attempts = 2
	job.LastError = "release fetch: unavailable"
	job.NextAttempt = time.Now().UTC().Add(time.Hour)
	require.NoError(t, store.Save(job))
	originalNextAttempt := job.NextAttempt

	require.NoError(t, store.ScheduleEvent(42, "alice/platform", "main", "", false, "release-delivery"))
	duplicate, err := store.Get(42)
	require.NoError(t, err)
	assert.Equal(t, 2, duplicate.Attempts)
	assert.Equal(t, "release fetch: unavailable", duplicate.LastError)
	assert.Equal(t, originalNextAttempt, duplicate.NextAttempt)
}

func TestPublishedStateSurvivesBoltDBRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Save(&Job{
		RepositoryID: 42, FullName: "alice/platform", DefaultBranch: "main",
		TargetSHA: "commit-1", LastSHA: "commit-1", Status: StatusPublished,
		EName: "@platform", EnvelopeID: "profile-id", AuthorENames: []string{"@alice"},
	}))
	require.NoError(t, store.Close())

	reopened, err := OpenStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	job, err := reopened.Get(42)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, StatusPublished, job.Status)
	assert.Equal(t, "commit-1", job.LastSHA)
	assert.Equal(t, "profile-id", job.EnvelopeID)
	assert.Equal(t, []string{"@alice"}, job.AuthorENames)
}
