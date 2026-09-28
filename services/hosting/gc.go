// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	hosting_model "forgejo.org/models/hosting"
	packages_model "forgejo.org/models/packages"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	packages_service "forgejo.org/services/packages"
)

var jobTagPattern = regexp.MustCompile(`^job-(\d+)$`)

// registryGCGrace protects images of builds that just finished or are
// still waiting for a deploy step.
const registryGCGrace = 24 * time.Hour

// keptDigests returns the digests registry GC must keep: the last
// KEEP_DIGESTS released images of every target plus anything live,
// in flight or referenced by a rollback.
func keptDigests(ctx context.Context) (map[string]bool, error) {
	keep := map[string]bool{}
	targets, err := hosting_model.ListAllTargets(ctx)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		deployments, err := hosting_model.ListDeploymentDigests(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		released := 0
		for _, deployment := range deployments {
			digests := []string{deployment.ImageDigest}
			if images, err := deployment.Images(); err == nil {
				for _, ref := range images {
					if digest, ok := digestOf(ref); ok {
						digests = append(digests, digest)
					}
				}
			}
			live := deployment.ID == target.LiveDeploymentID
			inFlight := !deployment.Status.IsFinal() && deployment.Status != hosting_model.StatusLive
			if !live && !inFlight {
				// Only releases that actually ran can be rolled back to.
				wasLive := deployment.Status == hosting_model.StatusLive || deployment.Status == hosting_model.StatusSuperseded
				if !wasLive || released >= setting.Hosting.KeepDigests {
					continue
				}
				released++
			}
			for _, digest := range digests {
				keep[digest] = true
			}
		}
	}
	return keep, nil
}

// digestOf returns the digest of a digest-pinned image reference.
func digestOf(ref string) (string, bool) {
	_, digest, ok := strings.Cut(ref, "@")
	return digest, ok
}

// CollectRegistryGarbage deletes build images that no target can roll back
// to any more. Blobs are then reclaimed by Forgejo's package cleanup.
func CollectRegistryGarbage(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	owner, err := user_model.GetUserByName(ctx, setting.Hosting.RegistryOwner)
	if err != nil {
		return err
	}
	keep, err := keptDigests(ctx)
	if err != nil {
		return err
	}
	versions, err := packages_model.GetVersionsByPackageType(ctx, owner.ID, packages_model.TypeContainer)
	if err != nil {
		return err
	}
	deleted := 0
	for _, version := range versions {
		match := jobTagPattern.FindStringSubmatch(version.Version)
		if match == nil || time.Since(version.CreatedUnix.AsTime()) < registryGCGrace {
			continue
		}
		jobID, _ := strconv.ParseInt(match[1], 10, 64)
		if keepJobImage(ctx, jobID, keep) {
			continue
		}
		if err := packages_service.RemovePackageVersion(ctx, owner, version); err != nil {
			log.Warn("Registry GC: remove %s: %v", version.Version, err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		log.Info("Registry GC removed %d unused build images", deleted)
	}
	return nil
}

func keepJobImage(ctx context.Context, jobID int64, keep map[string]bool) bool {
	job, err := hosting_model.GetBuildJob(ctx, jobID)
	if err != nil {
		return false
	}
	if job.Status == hosting_model.BuildQueued || job.Status == hosting_model.BuildRunning {
		return true
	}
	deployment, err := hosting_model.GetDeployment(ctx, job.DeploymentID)
	if err != nil {
		return false
	}
	if keep[deployment.ImageDigest] {
		return true
	}
	images, err := deployment.Images()
	if err != nil {
		return false
	}
	for _, ref := range images {
		if digest, ok := digestOf(ref); ok && keep[digest] {
			return true
		}
	}
	return false
}
