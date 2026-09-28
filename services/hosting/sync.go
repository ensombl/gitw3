// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"strings"
	"time"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
)

// SyncDeployments advances every in-flight deployment. It runs from cron and
// is idempotent, so a crash at any point is repaired on the next tick.
func SyncDeployments(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	deployments, err := hosting_model.ListDeploymentsByStatus(ctx,
		hosting_model.StatusQueued, hosting_model.StatusBuilding, hosting_model.StatusBuilt,
		hosting_model.StatusAwaitingSignature, hosting_model.StatusAwaitingCertification, hosting_model.StatusDeploying)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch deployment.Status {
		case hosting_model.StatusQueued, hosting_model.StatusBuilding:
			syncBuild(ctx, deployment)
		case hosting_model.StatusBuilt, hosting_model.StatusAwaitingCertification:
			goLive(ctx, deployment)
		case hosting_model.StatusAwaitingSignature:
			syncSignature(ctx, deployment)
		case hosting_model.StatusDeploying:
			syncRollout(ctx, deployment)
		}
	}
	return nil
}

func syncBuild(ctx context.Context, deployment *hosting_model.Deployment) {
	job, err := hosting_model.GetBuildJobByDeployment(ctx, deployment.ID)
	if err != nil {
		if deployment.Trigger == hosting_model.TriggerRollback {
			return
		}
		log.Warn("Deployment %d has no build job: %v", deployment.ID, err)
		return
	}
	if job.RunID == 0 {
		return
	}
	state, err := current().Builder.Status(ctx, job.RunID)
	if err != nil {
		log.Warn("Build run %d status: %v", job.RunID, err)
		return
	}
	switch state {
	case BuildStateWaiting:
		if age := time.Since(deployment.StartedUnix.AsTime()); age > setting.Hosting.BuildQueueAlert {
			log.Warn("Deployment %d has waited %s for a build runner; is the build node up?", deployment.ID, age.Round(time.Minute))
		}
	case BuildStateRunning:
		if deployment.Status == hosting_model.StatusQueued {
			_, _ = hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilding)
			job.Status = hosting_model.BuildRunning
			_ = hosting_model.UpdateBuildJobCols(ctx, job, "status")
		}
	case BuildStateSucceeded:
		// The run finished but no valid callback arrived in time.
		if time.Since(deployment.UpdatedUnix.AsTime()) > 5*time.Minute {
			markBuildLost(ctx, deployment, job, "The build finished without reporting an image.")
		}
	case BuildStateFailed:
		markBuildLost(ctx, deployment, job, "The build failed; see the build log.")
	case BuildStateCancelled:
		job.Status = hosting_model.BuildCancelled
		_ = hosting_model.UpdateBuildJobCols(ctx, job, "status")
		deployment.Error = "The build was cancelled."
		_, _ = hosting_model.Transition(ctx, deployment, hosting_model.StatusCancelled, "error")
	}
}

func markBuildLost(ctx context.Context, deployment *hosting_model.Deployment, job *hosting_model.BuildJob, message string) {
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		return
	}
	job.Status = hosting_model.BuildFailed
	job.FinishedUnix = timeutil.TimeStampNow()
	_ = hosting_model.UpdateBuildJobCols(ctx, job, "status", "finished_unix")
	failBuild(ctx, target, deployment, message)
}

func syncSignature(ctx context.Context, deployment *hosting_model.Deployment) {
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		return
	}
	if targetAuthorised(ctx, target) {
		goLive(ctx, deployment)
		return
	}
	// A wallet request that expired leaves the build ready; the next deploy
	// click issues a fresh request. Give up after a day.
	if time.Since(deployment.UpdatedUnix.AsTime()) > 24*time.Hour {
		cancelDeployment(ctx, deployment, "The deployer did not sign the W3DS deployment in time.")
	}
}

func syncRollout(ctx context.Context, deployment *hosting_model.Deployment) {
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		return
	}
	rollout, err := rolloutState(ctx, target, deployment)
	if err != nil {
		log.Warn("Deployment %d rollout status: %v", deployment.ID, err)
		return
	}
	switch rollout {
	case RolloutHealthy:
		markLive(ctx, target, deployment)
		return
	case RolloutRolledBack:
		deployment.Error = "The new release failed its health checks; Swarm rolled back to the previous release."
		if ok, _ := hosting_model.Transition(ctx, deployment, hosting_model.StatusRolledBack, "error"); ok {
			hosting_model.Audit(ctx, deployment.ActorID, deployment.RepoID, target.ID, deployment.ID, hosting_model.AuditDeployFailed, map[string]any{"error": deployment.Error})
			setCommitStatus(ctx, target, deployment, "failure", "Rolled back: health checks failed", "")
		}
		return
	case RolloutFailed:
		failDeploy(ctx, target, deployment, "The rollout failed; the previous release keeps serving traffic.")
		return
	}
	if time.Since(deployment.UpdatedUnix.AsTime()) > setting.Hosting.HealthTimeout {
		failDeploy(ctx, target, deployment, "The new release did not become healthy in time.")
	}
}

func rolloutState(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment) (Rollout, error) {
	c := current()
	if c.Swarm == nil {
		return dokployRollout(ctx, target)
	}
	repo, err := repo_model.GetRepositoryByID(ctx, target.RepoID)
	if err != nil {
		return RolloutUnknown, err
	}
	var services []ServiceStatus
	digests := map[string]string{}
	if target.DokployComposeID != "" {
		services, err = c.Swarm.Services(ctx, "", dokployAppName(repo, target))
		images, imagesErr := deployment.Images()
		if imagesErr != nil {
			return RolloutUnknown, imagesErr
		}
		for service, ref := range images {
			_, digest, _ := strings.Cut(ref, "@")
			digests[service] = digest
		}
	} else {
		var state *AppState
		if state, err = c.Dokploy.AppState(ctx, target.DokployAppID); err != nil {
			return RolloutUnknown, err
		}
		services, err = c.Swarm.Services(ctx, state.AppName, "")
		digests[""] = deployment.ImageDigest
	}
	if err != nil {
		return RolloutUnknown, err
	}
	return EvaluateRollout(services, digests), nil
}

// dokployRollout is the fallback when no socket proxy is configured: it
// trusts Dokploy's own deploy status.
func dokployRollout(ctx context.Context, target *hosting_model.Target) (Rollout, error) {
	var state *AppState
	var err error
	if target.DokployComposeID != "" {
		state, err = current().Dokploy.ComposeState(ctx, target.DokployComposeID)
	} else {
		state, err = current().Dokploy.AppState(ctx, target.DokployAppID)
	}
	if err != nil {
		return RolloutUnknown, err
	}
	switch state.Status {
	case "done":
		return RolloutHealthy, nil
	case "error":
		return RolloutFailed, nil
	}
	return RolloutInProgress, nil
}

// TargetView is a template-friendly summary of a target.
type TargetView struct {
	Target  *hosting_model.Target
	Spec    *hosting_module.Target
	URL     string
	Live    *hosting_model.Deployment
	Domains []*hosting_model.Domain
}

// ViewTargets loads everything the Managed tab shows for a repository.
func ViewTargets(ctx context.Context, repoID int64) ([]*TargetView, error) {
	targets, err := hosting_model.ListTargets(ctx, repoID)
	if err != nil {
		return nil, err
	}
	views := make([]*TargetView, 0, len(targets))
	for _, target := range targets {
		spec, err := target.Spec()
		if err != nil {
			return nil, err
		}
		view := &TargetView{Target: target, Spec: spec, URL: PublicURL(ctx, target)}
		if target.LiveDeploymentID != 0 {
			view.Live, _ = hosting_model.GetDeployment(ctx, target.LiveDeploymentID)
		}
		if view.Domains, err = hosting_model.ListTargetDomains(ctx, target.ID); err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}
