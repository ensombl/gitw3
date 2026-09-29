// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"

	user_model "forgejo.org/models/user"
	"forgejo.org/services/w3dsidentity"
)

func initW3DSTasks() {
	if !w3dsidentity.Enabled() {
		return
	}
	// Profiles change on other platforms; sign-in alone would leave photos
	// stale for people who stay signed in.
	RegisterTaskFatal("w3ds_avatar_sync", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 6h",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return w3dsidentity.SyncAvatars(ctx)
	})
}
