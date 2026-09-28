// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"
	"fmt"
	"strings"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
)

// BuildSpec is the job description handed to the central builder workflow.
// The builder only learns where to push; it never chooses image names.
type BuildSpec struct {
	JobID    int64        `json:"job_id"`
	Nonce    string       `json:"nonce"`
	Commit   string       `json:"commit"`
	Kind     string       `json:"kind"`
	Registry string       `json:"registry"`
	Images   []BuildImage `json:"images"`
	Scan     struct {
		Policy   string `json:"policy"`
		Severity string `json:"severity"`
	} `json:"scan"`
}

// BuildImage is one image the builder produces. Service is empty for
// Dockerfile targets and the compose service name otherwise.
type BuildImage struct {
	Service    string `json:"service"`
	Repository string `json:"repository"`
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}

func newBuildSpec(repo *repo_model.Repository, target *hosting_model.Target, spec *hosting_module.Target, config *ReleaseConfig, deployment *hosting_model.Deployment, job *hosting_model.BuildJob) BuildSpec {
	build := BuildSpec{
		JobID: job.ID, Nonce: job.Nonce, Commit: deployment.CommitSHA, Kind: spec.Kind,
		Registry: setting.HostingRegistryHost(),
	}
	build.Scan.Policy, build.Scan.Severity = setting.Hosting.Scan.Policy, setting.Hosting.Scan.Severity
	registryPath := strings.ToLower(setting.Hosting.RegistryOwner)
	if spec.Kind == hosting_module.KindCompose {
		for _, service := range config.Builds[spec.Name] {
			build.Images = append(build.Images, BuildImage{
				Service:    service.Service,
				Repository: registryPath + "/" + hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name, service.Service),
				Context:    service.Context, Dockerfile: joinContext(service.Context, service.Dockerfile),
			})
		}
		return build
	}
	build.Images = []BuildImage{{
		Repository: registryPath + "/" + hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name),
		Context:    spec.Context, Dockerfile: spec.Dockerfile,
	}}
	return build
}

// joinContext resolves a compose dockerfile, which is relative to its build context.
func joinContext(context, dockerfile string) string {
	if context == "." || context == "" {
		return dockerfile
	}
	return context + "/" + dockerfile
}

// BuildCallback is what the builder POSTs when a job finishes.
type BuildCallback struct {
	JobID  int64  `json:"job_id"`
	Nonce  string `json:"nonce"`
	Status string `json:"status"` // success or failure
	// Digests maps service ("" for Dockerfile targets) to the pushed manifest digest.
	Digests map[string]string `json:"digests"`
	Scan    *ScanResult       `json:"scan,omitempty"`
	Error   string            `json:"error,omitempty"`
	Runner  string            `json:"runner,omitempty"`
}

// ScanResult summarises the builder's vulnerability scan.
type ScanResult struct {
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
	Summary  string `json:"summary,omitempty"`
}

// Blocking reports whether the scan found anything at or above severity.
func (s *ScanResult) Blocking(severity string) bool {
	if s == nil {
		return false
	}
	switch strings.ToUpper(severity) {
	case "LOW":
		return s.Critical+s.High+s.Medium+s.Low > 0
	case "MEDIUM":
		return s.Critical+s.High+s.Medium > 0
	case "HIGH":
		return s.Critical+s.High > 0
	default:
		return s.Critical > 0
	}
}

// Callback errors map to HTTP statuses in the router.
var (
	ErrCallbackUnauthorized = errors.New("invalid callback signature")
	ErrCallbackReplayed     = errors.New("unknown or already used build nonce")
	ErrCallbackRejected     = errors.New("build result rejected")
)

// HandleBuildCallback verifies and applies a builder callback. The HMAC
// proves the builder sent it, the single-use nonce stops replays, and every
// digest must exist under this target's own image names in the registry, so a
// compromised build node can neither forge nor redirect a deploy.
func HandleBuildCallback(ctx context.Context, body []byte, signature string) error {
	if !Enabled() {
		return ErrDisabled
	}
	if !hosting_module.VerifyCallback(setting.Hosting.CallbackSecret, body, signature) {
		return ErrCallbackUnauthorized
	}
	callback := &BuildCallback{}
	if err := json.Unmarshal(body, callback); err != nil {
		return fmt.Errorf("%w: %v", ErrCallbackRejected, err)
	}
	job, err := hosting_model.GetBuildJob(ctx, callback.JobID)
	if err != nil {
		return ErrCallbackReplayed
	}
	consumed, err := hosting_model.ConsumeNonce(ctx, job.ID, callback.Nonce)
	if err != nil {
		return err
	}
	if !consumed {
		return ErrCallbackReplayed
	}
	deployment, err := hosting_model.GetDeployment(ctx, job.DeploymentID)
	if err != nil {
		return err
	}
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		return err
	}
	job.Runner = truncate(callback.Runner, 255)
	job.FinishedUnix = timeutil.TimeStampNow()
	if callback.Scan != nil {
		scan, _ := json.Marshal(callback.Scan)
		job.ScanResultJSON = string(scan)
	}
	if deployment.Status == hosting_model.StatusQueued {
		_, _ = hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilding)
	}
	if callback.Status != "success" {
		job.Status = hosting_model.BuildFailed
		_ = hosting_model.UpdateBuildJobCols(ctx, job, "status", "runner", "finished_unix", "scan_result_json")
		failBuild(ctx, target, deployment, "Build failed: "+truncate(callback.Error, 2000))
		return nil
	}
	if callback.Scan.Blocking(setting.Hosting.Scan.Severity) {
		summary := fmt.Sprintf("vulnerability scan found %d critical and %d high issues", callback.Scan.Critical, callback.Scan.High)
		if setting.Hosting.Scan.Policy == setting.HostingScanPolicyBlock {
			job.Status = hosting_model.BuildFailed
			_ = hosting_model.UpdateBuildJobCols(ctx, job, "status", "runner", "finished_unix", "scan_result_json")
			failBuild(ctx, target, deployment, "Blocked: "+summary)
			return nil
		}
		deployment.Warning = summary
	}
	images, err := verifiedImages(ctx, target, deployment, callback.Digests)
	if err != nil {
		job.Status = hosting_model.BuildFailed
		_ = hosting_model.UpdateBuildJobCols(ctx, job, "status", "runner", "finished_unix", "scan_result_json")
		failBuild(ctx, target, deployment, err.Error())
		return fmt.Errorf("%w: %v", ErrCallbackRejected, err)
	}
	job.Status = hosting_model.BuildSucceeded
	if err := hosting_model.UpdateBuildJobCols(ctx, job, "status", "runner", "finished_unix", "scan_result_json"); err != nil {
		return err
	}
	if single, ok := images[""]; ok {
		_, deployment.ImageDigest, _ = strings.Cut(single, "@")
	} else {
		encoded, _ := json.Marshal(images)
		deployment.ImagesJSON = string(encoded)
		_, deployment.ImageDigest, _ = strings.Cut(images[firstKey(images)], "@")
	}
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusBuilt, "image_digest", "images_json", "warning")
	if err != nil {
		return err
	}
	if !ok {
		// Cancelled or superseded while building; the image stays in the
		// registry for GC but is not deployed.
		return nil
	}
	hosting_model.Audit(ctx, deployment.ActorID, deployment.RepoID, target.ID, deployment.ID, hosting_model.AuditDeployBuilt, map[string]any{"digest": deployment.ImageDigest})
	goLive(ctx, deployment)
	return nil
}

// verifiedImages checks the reported digests against the images this
// target expects and the registry, and returns digest-pinned references.
func verifiedImages(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment, digests map[string]string) (map[string]string, error) {
	repo, err := repo_model.GetRepositoryByID(ctx, target.RepoID)
	if err != nil {
		return nil, err
	}
	spec, err := target.Spec()
	if err != nil {
		return nil, err
	}
	expected := []string{""}
	if spec.Kind == hosting_module.KindCompose {
		config, err := LoadReleaseConfig(ctx, repo, deployment.CommitSHA)
		if err != nil {
			return nil, err
		}
		expected = expected[:0]
		for _, build := range config.Builds[spec.Name] {
			expected = append(expected, build.Service)
		}
	}
	if len(digests) != len(expected) {
		return nil, fmt.Errorf("builder reported %d images, expected %d", len(digests), len(expected))
	}
	host, owner := setting.HostingRegistryHost(), setting.Hosting.RegistryOwner
	refs := make(map[string]string, len(expected))
	for _, service := range expected {
		digest, ok := digests[service]
		if !ok || !hosting_module.IsDigest(digest) {
			return nil, fmt.Errorf("builder reported no valid digest for %q", service)
		}
		var image string
		if service == "" {
			image = hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name)
		} else {
			image = hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name, service)
		}
		exists, err := current().Registry.Exists(ctx, image, digest)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("image %s@%s is not in the registry", image, digest)
		}
		refs[service] = hosting_module.DigestReference(hosting_module.ImageRepository(host, owner, image), digest)
	}
	return refs, nil
}

func failBuild(ctx context.Context, target *hosting_model.Target, deployment *hosting_model.Deployment, message string) {
	deployment.Error = message
	ok, err := hosting_model.Transition(ctx, deployment, hosting_model.StatusBuildFailed, "error", "warning")
	if err != nil {
		log.Error("Mark deployment %d build failed: %v", deployment.ID, err)
	}
	if !ok {
		return
	}
	hosting_model.Audit(ctx, deployment.ActorID, deployment.RepoID, target.ID, deployment.ID, hosting_model.AuditDeployFailed, map[string]any{"error": message})
	setCommitStatus(ctx, target, deployment, api.CommitStatusFailure, truncate(message, 140), "")
}

func firstKey(values map[string]string) string {
	first := ""
	for key := range values {
		if first == "" || key < first {
			first = key
		}
	}
	return first
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
