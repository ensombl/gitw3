// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"forgejo.org/models/db"
	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/w3ds"
	release_service "forgejo.org/services/release"
)

// deployPush publishes a push to the default branch as the next patch
// release when one of the repository's apps deploys every push. Hosted apps
// only ever run released versions, so the release is what deploys: it goes
// through the usual NewRelease path (auto-deploy, W3DS version publishing).
func deployPush(pusher *user_model.User, repo *repo_model.Repository, opts *repo_module.PushUpdateOptions) {
	if !Enabled() || !opts.RefFullName.IsBranch() || opts.IsDelRef() ||
		opts.RefFullName.BranchName() != repo.DefaultBranch {
		return
	}
	go func() {
		ctx := graceful.GetManager().ShutdownContext()
		if tag, err := releasePush(ctx, pusher, repo, opts); err != nil {
			log.Error("Deploy push %s to %s: %v", opts.NewCommitID, repo.FullName(), err)
		} else if tag != "" {
			log.Info("Published push %s to %s as %s", opts.NewCommitID, repo.FullName(), tag)
		}
	}()
}

// releasePush creates the release for a push, returning its tag, or "" when
// the push should not deploy.
func releasePush(ctx context.Context, pusher *user_model.User, repo *repo_model.Repository, opts *repo_module.PushUpdateOptions) (string, error) {
	enabled, err := hosting_model.HasDeployOnPushTarget(ctx, repo.ID)
	if err != nil || !enabled {
		return "", err
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return "", err
	}
	defer gitRepo.Close()

	// Metadata commits (GitW3 syncing .w3ds/platform.json after a release)
	// would otherwise release again, forever.
	if !opts.IsNewRef() {
		files, err := gitRepo.GetFilesChangedBetween(opts.OldCommitID, opts.NewCommitID)
		if err != nil {
			return "", err
		}
		if onlyPlatformMetadata(files) {
			return "", nil
		}
	}
	// A commit that already has a version (a pushed tag) deploys that way.
	released, err := db.GetEngine(ctx).Where("repo_id = ? AND sha1 = ?", repo.ID, opts.NewCommitID).Exist(new(repo_model.Release))
	if err != nil || released {
		return "", err
	}

	tag, err := nextPatchTag(ctx, repo, gitRepo)
	if err != nil {
		return "", err
	}
	release := &repo_model.Release{
		RepoID: repo.ID, Repo: repo, PublisherID: pusher.ID, Publisher: pusher,
		TagName: tag, Target: opts.NewCommitID, Title: tag,
		Note: "Published automatically from a push to " + repo.DefaultBranch + ".",
	}
	if err := release_service.CreateRelease(gitRepo, release, "", nil); err != nil {
		return "", err
	}
	return tag, nil
}

func onlyPlatformMetadata(files []string) bool {
	if len(files) == 0 {
		return true
	}
	for _, file := range files {
		if !strings.HasPrefix(file, ".w3ds/") {
			return false
		}
	}
	return true
}

// nextPatchTag returns the tag after the latest stable release (v0.1.0 for
// the first), skipping tags that already exist.
func nextPatchTag(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository) (string, error) {
	major, minor, patch := 0, 1, 0
	latest, err := repo_model.GetLatestReleaseByRepoID(ctx, repo.ID)
	switch {
	case err == nil:
		version, valid := w3ds.NormalizeReleaseVersion(latest.TagName)
		if !valid {
			return "", fmt.Errorf("latest release %s is not a semantic version", latest.TagName)
		}
		if major, minor, patch, err = parseStableVersion(version); err != nil {
			return "", fmt.Errorf("latest release %s: %w", latest.TagName, err)
		}
		patch++
	case !repo_model.IsErrReleaseNotExist(err):
		return "", err
	}
	for range 100 {
		tag := fmt.Sprintf("v%d.%d.%d", major, minor, patch)
		if !gitRepo.IsTagExist(tag) {
			return tag, nil
		}
		patch++
	}
	return "", fmt.Errorf("no free version after v%d.%d.%d", major, minor, patch)
}

func parseStableVersion(version string) (major, minor, patch int, err error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", version)
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		if numbers[i], err = strconv.Atoi(part); err != nil || numbers[i] < 0 {
			return 0, 0, 0, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", version)
		}
	}
	return numbers[0], numbers[1], numbers[2], nil
}
