// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/w3ds"
)

// goLive moves a built (or rollback) deployment towards the cluster. It is
// safe to call repeatedly: every step is guarded by a status transition.
func goLive(ctx context.Context, deployment *hosting_model.Deployment) {
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		log.Error("Deployment %d: load target: %v", deployment.ID, err)
		return
	}
	if setting.Hosting.RequireW3DS {
		ready, err := ensureW3DSVersion(ctx, target, deployment)
		if err != nil {
			failDeploy(ctx, target, deployment, err.Error())
			return
		}
		if !ready {
			return
		}
	}
	if err := deployToCluster(ctx, target, deployment); err != nil {
		failDeploy(ctx, target, deployment, err.Error())
	}
}

// ensureW3DSVersion makes sure the W3DS deployment record describes the
// release being deployed. It returns false (after parking the deployment)
// when the deployer still has to sign or the release awaits certification.
func ensureW3DSVersion(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment) (bool, error) {
	if !targetAuthorised(ctx, target) {
		_, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusAwaitingSignature)
		if err == nil {
			setCommitStatus(ctx, target, deployment, api.CommitStatusPending, "Waiting for the deployer's wallet signature", "")
		}
		return false, err
	}
	version, valid := w3ds.NormalizeReleaseVersion(deployment.TagName)
	if !valid {
		return false, fmt.Errorf("release tag %s is not a semantic version", deployment.TagName)
	}
	if version == target.W3DSVersion {
		return true, nil
	}
	keyFile, err := target.PrivateKey()
	if err != nil {
		return false, err
	}
	err = publishW3DSVersion(ctx, target.W3DSDeploymentID, target.DeploymentEName, keyFile, version, deployment.TagName, deployment.CommitSHA)
	if err != nil {
		// Certification can arrive later, and the publisher may be briefly
		// unavailable: park the built image and let the sync loop retry.
		if _, transitionErr := hosting_model.Transition(ctx, deployment, hosting_model.StatusAwaitingCertification); transitionErr != nil {
			return false, transitionErr
		}
		description := "Waiting for PPA certification of " + version
		if !IsCertificationRequired(err) {
			log.Warn("Deployment %d: publish W3DS version %s: %v", deployment.ID, version, err)
			description = "Waiting to publish " + version + " to W3DS"
		}
		setCommitStatus(ctx, target, deployment, api.CommitStatusPending, description, "")
		return false, nil
	}
	target.W3DSVersion = version
	return true, hosting_model.UpdateTargetCols(ctx, target, "w3ds_version")
}

func deployToCluster(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment) error {
	repo, err := repo_model.GetRepositoryByID(ctx, target.RepoID)
	if err != nil {
		return err
	}
	spec, err := target.Spec()
	if err != nil {
		return err
	}
	env, err := hosting_model.EnvMap(ctx, target.ID)
	if err != nil {
		return err
	}
	if err := deployment.SetEnvSnapshot(env); err != nil {
		return err
	}
	if err := hosting_model.UpdateDeploymentCols(ctx, deployment, "env_snapshot_enc"); err != nil {
		return err
	}
	runtimeEnv := withPlatformEnv(ctx, target, spec, deployment, env)
	// The W3DS deployment key travels in the environment: a file mount would
	// live on the manager's disk and cannot reach tasks on worker nodes.
	keyFile, err := target.PrivateKey()
	if err != nil {
		return err
	}
	if keyFile != "" {
		runtimeEnv[deploymentKeyJSONEnvVar] = keyFile
	}
	title := "GitW3 " + deployment.TagName + " (#" + strconv.FormatInt(deployment.ID, 10) + ")"
	c := current()
	if spec.Kind == hosting_module.KindCompose {
		config, err := LoadReleaseConfig(ctx, repo, deployment.CommitSHA)
		if err != nil {
			return err
		}
		file := config.Compose[target.Name]
		if file == nil {
			return fmt.Errorf("release %s no longer defines target %q", deployment.TagName, target.Name)
		}
		images, err := deployment.Images()
		if err != nil {
			return err
		}
		file.ApplyPlacement(setting.Hosting.PlacementConstraints)
		// Swarm stacks only use the deploy environment for substitution, so it
		// is written into each service.
		file.SetEnvironment(runtimeEnv)
		rendered, err := file.PinImages(images)
		if err != nil {
			return err
		}
		if err := c.Dokploy.DeployStack(ctx, target.DokployComposeID, string(rendered), runtimeEnv, title); err != nil {
			return fmt.Errorf("deploy stack: %w", err)
		}
	} else {
		ref := hosting_module.DigestReference(
			hosting_module.ImageRepository(setting.HostingRegistryHost(), setting.Hosting.RegistryOwner,
				hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name)),
			deployment.ImageDigest)
		if err := c.Dokploy.SetEnv(ctx, target.DokployAppID, runtimeEnv); err != nil {
			return fmt.Errorf("set environment: %w", err)
		}
		if err := c.Dokploy.DeployImage(ctx, target.DokployAppID, ref, title); err != nil {
			return fmt.Errorf("deploy image: %w", err)
		}
	}
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusDeploying)
	if err != nil || !ok {
		return err
	}
	setCommitStatus(ctx, target, deployment, api.CommitStatusPending, "Rolling out "+deployment.TagName, "")
	return nil
}

// withPlatformEnv adds the variables GitW3 provides to every app. User
// variables win for PORT; GITW3_* names are reserved.
func withPlatformEnv(ctx context.Context, target *hosting_model.Target, spec *hosting_module.Target, deployment *hosting_model.Deployment, env map[string]string) map[string]string {
	runtime := make(map[string]string, len(env)+6)
	maps.Copy(runtime, env)
	if _, ok := runtime["PORT"]; !ok && spec.Port > 0 {
		runtime["PORT"] = strconv.Itoa(spec.Port)
	}
	runtime["GITW3_RELEASE"] = deployment.TagName
	runtime["GITW3_COMMIT"] = deployment.CommitSHA
	runtime["GITW3_TARGET"] = target.Name
	if url := PublicURL(ctx, target); url != "" {
		runtime["GITW3_URL"] = url
	}
	if target.DeploymentEName != "" {
		runtime["W3DS_DEPLOYMENT_ENAME"] = target.DeploymentEName
	}
	return runtime
}

func failDeploy(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment, message string) {
	deployment.Error = truncate(message, 2000)
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusDeployFailed, "error")
	if err != nil {
		log.Error("Mark deployment %d failed: %v", deployment.ID, err)
	}
	if !ok {
		return
	}
	hosting_model.Audit(ctx, deployment.ActorID, deployment.RepoID, target.ID, deployment.ID, hosting_model.AuditDeployFailed, map[string]any{"error": deployment.Error})
	setCommitStatus(ctx, target, deployment, api.CommitStatusFailure, truncate(message, 140), "")
}

func markLive(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment) {
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusLive)
	if err != nil || !ok {
		if err != nil {
			log.Error("Mark deployment %d live: %v", deployment.ID, err)
		}
		return
	}
	if target.LiveDeploymentID != 0 && target.LiveDeploymentID != deployment.ID {
		if previous, err := hosting_model.GetDeployment(ctx, target.LiveDeploymentID); err == nil {
			_, _ = hosting_model.Transition(ctx, previous, hosting_model.StatusSuperseded)
		}
	}
	target.LiveDeploymentID = deployment.ID
	if err := hosting_model.UpdateTargetCols(ctx, target, "live_deployment_id"); err != nil {
		log.Error("Record live deployment of target %d: %v", target.ID, err)
	}
	url := PublicURL(ctx, target)
	hosting_model.Audit(ctx, deployment.ActorID, deployment.RepoID, target.ID, deployment.ID, hosting_model.AuditDeployLive, map[string]any{"url": url})
	setCommitStatus(ctx, target, deployment, api.CommitStatusSuccess, deployment.TagName+" is live", url)
}
