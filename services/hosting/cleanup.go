// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"path"
	"strings"
	"time"

	hosting_model "forgejo.org/models/hosting"
	packages_model "forgejo.org/models/packages"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	packages_service "forgejo.org/services/packages"
)

// orphanGrace leaves Dokploy resources that may still be provisioning alone.
const orphanGrace = time.Hour

// SweepOrphans removes hosting that outlived its repository. Deleting a
// repository normally tears its apps down, but deleting its owner removes
// repositories without that notification, so this catches whatever is left:
// targets without a repository, and Dokploy apps GitW3 created that no target
// owns any more.
func SweepOrphans(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	orphans, err := hosting_model.ListOrphanTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range orphans {
		log.Info("Removing hosting target %d (%s): its repository %d no longer exists", target.ID, target.Name, target.RepoID)
		if err := removeTarget(ctx, target); err != nil {
			log.Error("Remove orphaned hosting target %d: %v", target.ID, err)
		}
	}
	return sweepDokploy(ctx)
}

func sweepDokploy(ctx context.Context) error {
	managed, err := current().Dokploy.ListManaged(ctx)
	if err != nil {
		return err
	}
	targets, err := hosting_model.ListAllTargets(ctx)
	if err != nil {
		return err
	}
	owned := make(map[string]bool, len(targets))
	for _, target := range targets {
		owned[target.DokployAppID] = true
		owned[target.DokployComposeID] = true
	}
	for _, resource := range managed {
		if owned[resource.ID] || !strings.HasPrefix(resource.Description, managedDescriptionPrefix) ||
			time.Since(resource.CreatedAt) < orphanGrace {
			continue
		}
		log.Info("Removing Dokploy resource %s (%s): no hosting target owns it", resource.ID, resource.Description)
		if resource.Compose {
			err = current().Dokploy.DeleteCompose(ctx, resource.ID)
		} else {
			err = current().Dokploy.DeleteApp(ctx, resource.ID)
		}
		if err != nil && !isDokployNotFound(err) {
			log.Error("Remove orphaned Dokploy resource %s: %v", resource.ID, err)
		}
	}
	return nil
}

// targetImageNames returns the registry packages a target pushed to: its
// image, or one per compose service.
func targetImageNames(ctx context.Context, repo *repo_model.Repository, target *hosting_model.Target) ([]string, error) {
	names := map[string]bool{hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name): true}
	deployments, err := hosting_model.ListDeploymentDigests(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	for _, deployment := range deployments {
		images, err := deployment.Images()
		if err != nil {
			continue
		}
		for _, ref := range images {
			if name, _, found := strings.Cut(ref, "@"); found {
				names[path.Base(name)] = true
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	return result, nil
}

// deleteImages removes every version of the given registry packages. Blobs
// are reclaimed by Forgejo's package cleanup.
func deleteImages(ctx context.Context, names []string) {
	owner, err := user_model.GetUserByName(ctx, setting.Hosting.RegistryOwner)
	if err != nil {
		log.Warn("Delete hosting images: registry owner %s: %v", setting.Hosting.RegistryOwner, err)
		return
	}
	for _, name := range names {
		versions, err := packages_model.GetVersionsByPackageName(ctx, owner.ID, packages_model.TypeContainer, name)
		if err != nil {
			log.Warn("Delete hosting images of %s: %v", name, err)
			continue
		}
		for _, version := range versions {
			if err := packages_service.RemovePackageVersion(ctx, owner, version); err != nil {
				log.Warn("Delete hosting image %s:%s: %v", name, version.Version, err)
			}
		}
	}
}
