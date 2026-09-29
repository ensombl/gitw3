// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"time"

	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	"forgejo.org/modules/w3ds"
	release_service "forgejo.org/services/release"
)

// PublishVersionTag turns a pushed version tag of a W3DS platform into a
// published release. W3DS versions, PPA certification and publishing all key
// off releases, while vibe coders (and their AI assistants) ship with
// `git tag v1.2.3 && git push --tags`; without this the tag would deploy but
// could never be certified. Other tags and repositories are left alone.
func PublishVersionTag(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, rel *repo_model.Release) error {
	if !rel.IsTag || rel.IsDraft || rel.IsPrerelease {
		return nil
	}
	if _, valid := w3ds.NormalizeReleaseVersion(rel.TagName); !valid {
		return nil
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return err
	}
	defer gitRepo.Close()
	if !isPlatformCommit(gitRepo, rel.Sha1) {
		return nil
	}
	rel.Repo = repo
	rel.IsTag = false
	if rel.Title == "" {
		rel.Title = rel.TagName
	}
	if doer != nil {
		rel.PublisherID = doer.ID
	}
	// createdFromTag announces it as a new release, which auto-deploys it and
	// lets the platform publisher pick up the version.
	return release_service.UpdateRelease(ctx, doer, gitRepo, rel, true, nil)
}

func isPlatformCommit(gitRepo *git.Repository, sha string) bool {
	commit, err := gitRepo.GetCommit(sha)
	if err != nil {
		return false
	}
	_, err = commit.GetTreeEntryByPath(w3ds.PlatformManifestPath)
	return err == nil
}

// publishPushedTag promotes a tag once the push has recorded it. Tag pushes
// are announced before their release rows are written, so it waits briefly.
func publishPushedTag(doer *user_model.User, repo *repo_model.Repository, tagName string) {
	go func() {
		ctx := graceful.GetManager().ShutdownContext()
		for range 20 {
			rel, err := repo_model.GetRelease(ctx, repo.ID, tagName)
			if err == nil {
				if err := PublishVersionTag(ctx, doer, repo, rel); err != nil {
					log.Error("Publish tag %s of %s as a release: %v", tagName, repo.FullName(), err)
				}
				return
			}
			if !repo_model.IsErrReleaseNotExist(err) {
				log.Error("Load tag %s of %s: %v", tagName, repo.FullName(), err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
}
