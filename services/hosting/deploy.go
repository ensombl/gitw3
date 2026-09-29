// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	git_model "forgejo.org/models/git"
	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	w3ds_model "forgejo.org/models/w3ds"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/w3ds"
	commitstatus_service "forgejo.org/services/repository/commitstatus"

	"github.com/google/uuid"
)

// signingLifetime bounds how long a first-deploy wallet request stays valid.
const signingLifetime = 15 * time.Minute

// DeployOptions starts a deployment of a tagged release.
type DeployOptions struct {
	Repo    *repo_model.Repository
	Release *repo_model.Release
	Actor   *user_model.User
	// TargetName selects a deploy.yml target; empty means the first one.
	TargetName string
	Trigger    hosting_model.Trigger
	// PlatformEName and DeployerEName are needed when the target has no
	// wallet-authorised W3DS deployment yet.
	PlatformEName string
	DeployerEName string
	// PlatformName is shown in the wallet signing prompt.
	PlatformName string
	// Subdomain is the address the user picked under the base domain; empty
	// keeps the current one (or assigns an automatic name on first deploy).
	Subdomain string
}

// SigningRequest asks the deployer's wallet to authorise a target's W3DS
// deployment record. It is only needed once per target.
type SigningRequest struct {
	W3DSDeploymentID string
	SigningPayload   string
	DeploymentEName  string
	VersionEName     string
	Message          string
	ExpiresAt        time.Time
}

// DeployResult is what Deploy started.
type DeployResult struct {
	Target     *hosting_model.Target
	Deployment *hosting_model.Deployment
	Signing    *SigningRequest
}

// UserError is safe to show to the person who triggered the action.
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

func userErrorf(format string, args ...any) error {
	return &UserError{Message: fmt.Sprintf(format, args...)}
}

// ReleaseVersion validates that a release can be deployed and returns its version.
func ReleaseVersion(release *repo_model.Release) (string, error) {
	// A pushed version tag deploys like a release, so an AI assistant can ship
	// with `git tag v1.0.0 && git push --tags`.
	if release == nil || release.IsDraft || release.IsPrerelease || release.Sha1 == "" {
		return "", userErrorf("Choose a published stable release or version tag.")
	}
	version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
	if !valid {
		return "", userErrorf("The release tag must be a semantic version such as v1.2.3.")
	}
	return version, nil
}

// Deploy starts building and deploying a release to one target.
func Deploy(ctx context.Context, opts DeployOptions) (*DeployResult, error) {
	if !Enabled() {
		return nil, ErrDisabled
	}
	version, err := ReleaseVersion(opts.Release)
	if err != nil {
		return nil, err
	}
	// A version tag pushed before tags were published automatically still
	// needs to become a release before the PPA can certify it.
	if err := PublishVersionTag(ctx, opts.Actor, opts.Repo, opts.Release); err != nil {
		return nil, err
	}
	config, err := LoadReleaseConfig(ctx, opts.Repo, opts.Release.Sha1)
	if err != nil {
		return nil, &UserError{Message: err.Error()}
	}
	spec := config.Config.Targets[0]
	if opts.TargetName != "" {
		if spec = config.Config.Target(opts.TargetName); spec == nil {
			return nil, userErrorf("Release %s has no deploy target named %q.", opts.Release.TagName, opts.TargetName)
		}
	}
	preflight := config.Preflight[spec.Name]
	if preflight != nil && !preflight.OK() {
		return nil, &UserError{Message: "Fix this before deploying:\n• " + strings.Join(preflight.Problems, "\n• ")}
	}
	existing, err := hosting_model.GetTargetByRepoAndName(ctx, opts.Repo.ID, spec.Name)
	if err != nil && !errors.Is(err, hosting_model.ErrTargetNotExist) {
		return nil, err
	}
	if opts.Subdomain != "" {
		var existingID int64
		if existing != nil {
			existingID = existing.ID
		}
		if _, err := CheckSubdomain(ctx, opts.Subdomain, existingID); err != nil {
			return nil, &UserError{Message: err.Error()}
		}
	}
	needsSigning := setting.Hosting.RequireW3DS && (existing == nil || !targetAuthorised(ctx, existing))
	if needsSigning {
		if opts.Trigger == hosting_model.TriggerRelease {
			return nil, userErrorf("Target %q needs one wallet-signed deploy before releases can deploy automatically.", spec.Name)
		}
		if opts.PlatformEName == "" {
			return nil, userErrorf("Set this repository up as a W3DS platform before deploying it.")
		}
		if opts.DeployerEName == "" {
			return nil, userErrorf("Connect your W3DS eID wallet before deploying.")
		}
		certified, err := certifiedVersion(ctx, opts.Repo.ID, opts.PlatformEName, version)
		if err != nil {
			return nil, err
		}
		if !certified {
			return nil, userErrorf("Release %s needs PPA certification before it can be deployed.", version)
		}
	}

	target, err := EnsureTarget(ctx, opts.Repo, spec, opts.Actor, opts.Subdomain)
	if err != nil {
		return nil, err
	}
	result := &DeployResult{Target: target}
	if needsSigning {
		if result.Signing, err = prepareTargetSigning(ctx, opts, target, version); err != nil {
			return nil, err
		}
	}
	deployment := &hosting_model.Deployment{
		TargetID: target.ID, RepoID: opts.Repo.ID, ActorID: opts.Actor.ID, ReleaseID: opts.Release.ID,
		TagName: opts.Release.TagName, CommitSHA: strings.ToLower(opts.Release.Sha1), Trigger: opts.Trigger,
	}
	if preflight != nil && len(preflight.Warnings) > 0 {
		deployment.Warning = strings.Join(preflight.Warnings, "\n")
	}
	if err := hosting_model.CreateDeployment(ctx, deployment); err != nil {
		return nil, err
	}
	result.Deployment = deployment
	hosting_model.Audit(ctx, opts.Actor.ID, opts.Repo.ID, target.ID, deployment.ID, hosting_model.AuditDeployRequested,
		map[string]any{"tag": deployment.TagName, "trigger": string(deployment.Trigger)})
	supersedePending(ctx, target, deployment.ID)

	if err := dispatchBuild(ctx, opts, target, spec, config, deployment); err != nil {
		deployment.Error = err.Error()
		if _, transitionErr := hosting_model.Transition(ctx, deployment, hosting_model.StatusBuildFailed, "error"); transitionErr != nil {
			log.Error("Mark deployment %d failed: %v", deployment.ID, transitionErr)
		}
		setCommitStatus(ctx, target, deployment, api.CommitStatusError, "Build could not start", "")
		return result, err
	}
	setCommitStatus(ctx, target, deployment, api.CommitStatusPending, "Building "+deployment.TagName, "")
	return result, nil
}

func dispatchBuild(ctx context.Context, opts DeployOptions, target *hosting_model.Target, spec *hosting_module.Target, config *ReleaseConfig, deployment *hosting_model.Deployment) error {
	job := &hosting_model.BuildJob{DeploymentID: deployment.ID, Nonce: hosting_module.NewNonce()}
	if err := hosting_model.CreateBuildJob(ctx, job); err != nil {
		return err
	}
	buildSpec := newBuildSpec(opts.Repo, target, spec, config, deployment, job)
	encoded, err := json.Marshal(buildSpec)
	if err != nil {
		return err
	}
	query := hosting_module.SignSourceURL(setting.Hosting.CallbackSecret, job.ID, time.Now(), setting.Hosting.SourceURLTTL)
	runID, err := current().Builder.Dispatch(ctx, opts.Actor, BuildInputs{
		JobID: job.ID, Spec: base64.StdEncoding.EncodeToString(encoded),
		SourceURL: sourceURL(job.ID, query), CallbackURL: callbackURL(),
	})
	if err != nil {
		job.Status = hosting_model.BuildFailed
		_ = hosting_model.UpdateBuildJobCols(ctx, job, "status")
		return fmt.Errorf("dispatch build: %w", err)
	}
	job.RunID = runID
	return hosting_model.UpdateBuildJobCols(ctx, job, "run_id")
}

// supersedePending cancels older deployments of a target that have not
// reached the cluster yet, so only the newest release is built and shipped.
func supersedePending(ctx context.Context, target *hosting_model.Target, newestID int64) {
	pending, err := hosting_model.ListPendingDeployments(ctx, target.ID, newestID)
	if err != nil {
		log.Error("List pending deployments of target %d: %v", target.ID, err)
		return
	}
	for _, deployment := range pending {
		if deployment.ID < newestID {
			cancelDeployment(ctx, deployment, "superseded by a newer deployment")
		}
	}
}

func cancelDeployment(ctx context.Context, deployment *hosting_model.Deployment, reason string) bool {
	if job, err := hosting_model.GetBuildJobByDeployment(ctx, deployment.ID); err == nil && job.RunID > 0 &&
		(job.Status == hosting_model.BuildQueued || job.Status == hosting_model.BuildRunning) {
		if err := current().Builder.Cancel(ctx, job.RunID); err != nil {
			log.Warn("Cancel build run %d: %v", job.RunID, err)
		}
		job.Status = hosting_model.BuildCancelled
		_ = hosting_model.UpdateBuildJobCols(ctx, job, "status")
	}
	deployment.Error = reason
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusCancelled, "error")
	if err != nil {
		log.Error("Cancel deployment %d: %v", deployment.ID, err)
	}
	return ok
}

// Cancel stops a deployment that has not reached the cluster.
func Cancel(ctx context.Context, deployment *hosting_model.Deployment, actor *user_model.User) error {
	if !deployment.Status.IsPending() {
		return userErrorf("Only deployments that have not started rolling out can be cancelled.")
	}
	if !cancelDeployment(ctx, deployment, "cancelled by "+actor.Name) {
		return userErrorf("This deployment already moved on.")
	}
	hosting_model.Audit(ctx, actor.ID, deployment.RepoID, deployment.TargetID, deployment.ID, hosting_model.AuditDeployCancelled, nil)
	if target, err := hosting_model.GetTarget(ctx, deployment.TargetID); err == nil {
		setCommitStatus(ctx, target, deployment, api.CommitStatusError, "Deployment cancelled", "")
	}
	return nil
}

// Rollback redeploys the image (and env) of an earlier deployment without building.
func Rollback(ctx context.Context, target *hosting_model.Target, previous *hosting_model.Deployment, actor *user_model.User) (*hosting_model.Deployment, error) {
	if !Enabled() {
		return nil, ErrDisabled
	}
	if previous.TargetID != target.ID || (previous.Status != hosting_model.StatusLive && previous.Status != hosting_model.StatusSuperseded) {
		return nil, userErrorf("Only deployments that were live can be rolled back to.")
	}
	images, err := expectedImages(ctx, target, previous)
	if err != nil {
		return nil, err
	}
	for image, digest := range images {
		exists, err := current().Registry.Exists(ctx, image, digest)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, userErrorf("The image of %s was removed from the registry; deploy the release again instead.", previous.TagName)
		}
	}
	env, err := previous.EnvSnapshot()
	if err != nil {
		return nil, err
	}
	deployment := &hosting_model.Deployment{
		TargetID: target.ID, RepoID: target.RepoID, ActorID: actor.ID, ReleaseID: previous.ReleaseID,
		TagName: previous.TagName, CommitSHA: previous.CommitSHA, ImageDigest: previous.ImageDigest,
		ImagesJSON: previous.ImagesJSON, Trigger: hosting_model.TriggerRollback, RollbackOf: previous.ID,
	}
	if err := hosting_model.CreateDeployment(ctx, deployment); err != nil {
		return nil, err
	}
	hosting_model.Audit(ctx, actor.ID, target.RepoID, target.ID, deployment.ID, hosting_model.AuditRollbackRequested,
		map[string]any{"to": previous.ID, "tag": previous.TagName})
	supersedePending(ctx, target, deployment.ID)
	if err := hosting_model.ReplaceEnv(ctx, target.ID, env); err != nil {
		return deployment, err
	}
	goLive(ctx, deployment)
	return deployment, nil
}

// expectedImages maps registry image names to the digests a deployment runs.
func expectedImages(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment) (map[string]string, error) {
	repo, err := repo_model.GetRepositoryByID(ctx, target.RepoID)
	if err != nil {
		return nil, err
	}
	images := map[string]string{}
	if deployment.ImageDigest != "" && deployment.ImagesJSON == "" {
		images[hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name)] = deployment.ImageDigest
		return images, nil
	}
	refs, err := deployment.Images()
	if err != nil {
		return nil, err
	}
	for service, ref := range refs {
		_, digest, _ := strings.Cut(ref, "@")
		images[hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name, service)] = digest
	}
	if len(images) == 0 {
		return nil, userErrorf("This deployment has no recorded image.")
	}
	return images, nil
}

// prepareTargetSigning reserves the target's W3DS deployment identity with
// the deployment key GitW3 holds, so the wallet signs exactly once.
func prepareTargetSigning(ctx context.Context, opts DeployOptions, target *hosting_model.Target, version string) (*SigningRequest, error) {
	id := uuid.NewString()
	prepared, err := prepareW3DSDeployment(ctx, W3DSPrepareRequest{
		ID: id, RepositoryID: opts.Repo.ID, PlatformEName: opts.PlatformEName,
		DeploymentName: opts.Repo.Name + " " + target.Name, Environment: "production",
		DeployerEName: opts.DeployerEName, Version: version, ReleaseTag: opts.Release.TagName,
		CommitSHA: strings.ToLower(opts.Release.Sha1), PublicKey: target.PublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("reserve the W3DS deployment identity: %w", err)
	}
	payload, err := w3ds.DeploymentSigningPayload(prepared.BundlePayload)
	if err != nil || prepared.DeploymentEName == "" || prepared.VersionEName == "" {
		return nil, errors.New("the deployment publisher returned an incomplete identity")
	}
	expires := time.Now().UTC().Add(signingLifetime)
	record := &w3ds_model.Deployment{
		ID: id, SigningPayload: payload, RepositoryID: opts.Repo.ID, UserID: opts.Actor.ID,
		DeployerEName: opts.DeployerEName, Name: opts.Repo.Name + " " + target.Name, Environment: "production",
		ReleaseID: opts.Release.ID, Version: version, ReleaseTag: opts.Release.TagName,
		CommitSHA: strings.ToLower(opts.Release.Sha1), PlatformEName: opts.PlatformEName,
		VersionEName: prepared.VersionEName, DeploymentEName: prepared.DeploymentEName,
		PublicKey: target.PublicKey, BundlePayload: prepared.BundlePayload,
		Status: w3ds_model.DeploymentAwaitingSignature, ExpiresUnix: timeutil.TimeStamp(expires.Unix()),
	}
	if err := w3ds_model.CreateDeployment(ctx, record); err != nil {
		return nil, err
	}
	target.W3DSDeploymentID, target.DeploymentEName, target.W3DSVersion = id, prepared.DeploymentEName, version
	if err := hosting_model.UpdateTargetCols(ctx, target, "w3ds_deployment_id", "deployment_ename", "w3ds_version"); err != nil {
		return nil, err
	}
	name := opts.PlatformName
	if name == "" {
		name = opts.Repo.Name
	}
	return &SigningRequest{
		W3DSDeploymentID: id, SigningPayload: payload, DeploymentEName: prepared.DeploymentEName,
		VersionEName: prepared.VersionEName, ExpiresAt: expires,
		Message: "Deploy " + name + " " + version + " on GitW3 managed hosting",
	}, nil
}

// targetAuthorised reports whether the target's W3DS deployment record has
// been signed by its deployer.
func targetAuthorised(ctx context.Context, target *hosting_model.Target) bool {
	if target.W3DSDeploymentID == "" {
		return false
	}
	record, err := w3ds_model.GetDeployment(ctx, target.W3DSDeploymentID)
	return err == nil && record != nil && record.WalletSignature != ""
}

// SigningRequestFor returns the open wallet request of a target, if any, so
// the UI can show it again after a reload.
func SigningRequestFor(ctx context.Context, target *hosting_model.Target) *SigningRequest {
	if target.W3DSDeploymentID == "" {
		return nil
	}
	record, err := w3ds_model.GetDeployment(ctx, target.W3DSDeploymentID)
	if err != nil || record == nil || record.Status != w3ds_model.DeploymentAwaitingSignature ||
		record.ExpiresUnix <= timeutil.TimeStampNow() {
		return nil
	}
	return &SigningRequest{
		W3DSDeploymentID: record.ID, SigningPayload: record.SigningPayload,
		DeploymentEName: record.DeploymentEName, VersionEName: record.VersionEName,
		ExpiresAt: record.ExpiresUnix.AsTime(), Message: "Deploy " + record.Name + " " + record.Version + " on GitW3 managed hosting",
	}
}

// OnW3DSDeploymentSigned resumes a target's deployments once its deployer
// signed the W3DS deployment record in their wallet.
func OnW3DSDeploymentSigned(ctx context.Context, w3dsDeploymentID string) {
	target, err := hosting_model.GetTargetByW3DSDeployment(ctx, w3dsDeploymentID)
	if err != nil {
		if !errors.Is(err, hosting_model.ErrTargetNotExist) {
			log.Error("Find hosting target for W3DS deployment %s: %v", w3dsDeploymentID, err)
		}
		return
	}
	hosting_model.Audit(ctx, 0, target.RepoID, target.ID, 0, hosting_model.AuditSigned, map[string]any{"w3dsDeployment": w3dsDeploymentID})
	waiting, err := hosting_model.ListDeploymentsByStatus(ctx, hosting_model.StatusAwaitingSignature)
	if err != nil {
		log.Error("List deployments awaiting signature: %v", err)
		return
	}
	for _, deployment := range waiting {
		if deployment.TargetID == target.ID {
			goLive(ctx, deployment)
		}
	}
}

func setCommitStatus(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment, state api.CommitStatusState, description, targetURL string) {
	repo, err := repo_model.GetRepositoryByID(ctx, deployment.RepoID)
	if err != nil {
		log.Error("Commit status for deployment %d: %v", deployment.ID, err)
		return
	}
	creator, err := user_model.GetPossibleUserByID(ctx, deployment.ActorID)
	if err != nil {
		creator = user_model.NewGhostUser()
	}
	if targetURL == "" {
		targetURL = repo.HTMLURL() + "/deploy"
	}
	if err := commitstatus_service.CreateCommitStatus(ctx, repo, creator, deployment.CommitSHA, &git_model.CommitStatus{
		State: state, TargetURL: targetURL, Description: description, Context: "gitw3/deploy/" + target.Name,
	}); err != nil {
		log.Warn("Commit status for deployment %d: %v", deployment.ID, err)
	}
}
