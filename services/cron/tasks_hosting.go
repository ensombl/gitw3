// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"

	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	hosting_service "forgejo.org/services/hosting"
)

func initHostingTasks() {
	if !setting.Hosting.Enabled {
		return
	}
	RegisterTaskFatal("hosting_sync_deployments", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 15s",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return hosting_service.SyncDeployments(ctx)
	})
	RegisterTaskFatal("hosting_domain_pool", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 5m",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return hosting_service.RefillDomainPool(ctx)
	})
	RegisterTaskFatal("hosting_alerts", &BaseConfig{
		Enabled:    true,
		RunAtStart: false,
		Schedule:   "@every 5m",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return hosting_service.CheckAlerts(ctx)
	})
	RegisterTaskFatal("hosting_registry_gc", &BaseConfig{
		Enabled:    true,
		RunAtStart: false,
		Schedule:   "@weekly",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return hosting_service.CollectRegistryGarbage(ctx)
	})
}
