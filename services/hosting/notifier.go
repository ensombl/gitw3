// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	notify_service "forgejo.org/services/notify"
)

type notifier struct {
	notify_service.NullNotifier
}

func newNotifier() notify_service.Notifier {
	return &notifier{}
}

// NewRelease auto-deploys a freshly published stable release to every
// target that opted in. Only tagged releases ever deploy.
func (*notifier) NewRelease(ctx context.Context, rel *repo_model.Release) {
	autoDeploy(ctx, rel)
}

// UpdateRelease covers a draft being published.
func (*notifier) UpdateRelease(ctx context.Context, doer *user_model.User, rel *repo_model.Release) {
	autoDeploy(ctx, rel)
}

func autoDeploy(ctx context.Context, rel *repo_model.Release) {
	if !Enabled() || rel.IsDraft || rel.IsPrerelease || rel.IsTag {
		return
	}
	if _, err := ReleaseVersion(rel); err != nil {
		return
	}
	targets, err := hosting_model.ListAutoDeployTargets(ctx, rel.RepoID)
	if err != nil || len(targets) == 0 {
		if err != nil {
			log.Error("List auto-deploy targets of repo %d: %v", rel.RepoID, err)
		}
		return
	}
	// Building and deploying outlives the request that published the release.
	go func() {
		ctx := graceful.GetManager().ShutdownContext()
		repo, err := repo_model.GetRepositoryByID(ctx, rel.RepoID)
		if err != nil {
			log.Error("Auto-deploy release %d: %v", rel.ID, err)
			return
		}
		publisher, err := user_model.GetUserByID(ctx, rel.PublisherID)
		if err != nil {
			log.Error("Auto-deploy release %d: publisher: %v", rel.ID, err)
			return
		}
		for _, target := range targets {
			if existing, err := hosting_model.ListDeployments(ctx, target.ID, 1); err == nil && len(existing) > 0 &&
				existing[0].ReleaseID == rel.ID && !existing[0].Status.IsFinal() {
				continue
			}
			_, err := Deploy(ctx, DeployOptions{
				Repo: repo, Release: rel, Actor: publisher, TargetName: target.Name, Trigger: hosting_model.TriggerRelease,
			})
			var userErr *UserError
			if errors.As(err, &userErr) {
				log.Info("Auto-deploy of %s to %s/%s skipped: %s", rel.TagName, repo.FullName(), target.Name, userErr.Message)
			} else if err != nil {
				log.Error("Auto-deploy of %s to %s/%s: %v", rel.TagName, repo.FullName(), target.Name, err)
			}
		}
	}()
}

// CreateRef publishes a pushed version tag of a W3DS platform as a release.
func (*notifier) CreateRef(_ context.Context, doer *user_model.User, repo *repo_model.Repository, refFullName git.RefName, _ string) {
	if refFullName.IsTag() {
		publishPushedTag(doer, repo, refFullName.TagName())
	}
}

// DeleteRepository tears down the repository's apps, domains and targets.
func (*notifier) DeleteRepository(ctx context.Context, _ *user_model.User, repo *repo_model.Repository) {
	if !Enabled() {
		return
	}
	if err := DeleteRepoTargets(ctx, repo.ID); err != nil {
		log.Error("Remove managed hosting of deleted repository %s: %v", repo.FullName(), err)
	}
}
