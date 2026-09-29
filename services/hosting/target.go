// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/w3ds"
)

const (
	maxConfigSize  = 64 << 10
	maxComposeSize = 256 << 10
	// deploymentKeyJSONEnvVar carries the app's w3ds-deployment-key.json content.
	deploymentKeyJSONEnvVar = "W3DS_DEPLOYMENT_KEY_JSON"
)

// ErrNoDeployConfig means a commit has neither deploy.yml nor a Dockerfile.
var ErrNoDeployConfig = errors.New("add a Dockerfile or " + hosting_module.ConfigPath + " to deploy this repository")

// ReleaseConfig is the deploy configuration found at a release commit.
type ReleaseConfig struct {
	Config *hosting_module.Config
	// Compose holds the validated compose file of each compose target.
	Compose map[string]*hosting_module.ComposeFile
	Builds  map[string][]hosting_module.ComposeBuild
	// Preflight holds the instant Dockerfile checks per target.
	Preflight map[string]*hosting_module.Preflight
}

// LoadReleaseConfig reads and validates deploy.yml (or the zero-config
// Dockerfile default) at a commit. Compose files are validated too, so
// unsupported stack features fail before anything is built.
func LoadReleaseConfig(ctx context.Context, repo *repo_model.Repository, commitSHA string) (*ReleaseConfig, error) {
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer gitRepo.Close()
	commit, err := gitRepo.GetCommit(commitSHA)
	if err != nil {
		return nil, err
	}
	result := &ReleaseConfig{
		Compose: map[string]*hosting_module.ComposeFile{}, Builds: map[string][]hosting_module.ComposeBuild{},
		Preflight: map[string]*hosting_module.Preflight{},
	}
	content, err := commit.GetFileContent(hosting_module.ConfigPath, maxConfigSize)
	switch {
	case err == nil:
		if result.Config, err = hosting_module.ParseConfig([]byte(content)); err != nil {
			return nil, err
		}
	case git.IsErrNotExist(err):
		dockerfile, dockerErr := commit.GetFileContent("Dockerfile", maxConfigSize)
		if git.IsErrNotExist(dockerErr) {
			return nil, ErrNoDeployConfig
		}
		if dockerErr != nil {
			return nil, dockerErr
		}
		result.Config = hosting_module.DefaultConfig([]byte(dockerfile))
	default:
		return nil, err
	}
	for _, target := range result.Config.Targets {
		if target.Kind != hosting_module.KindCompose {
			dockerfile, err := commit.GetFileContent(target.Dockerfile, maxConfigSize)
			if err != nil {
				return nil, fmt.Errorf("target %q: %s not found in this release", target.Name, target.Dockerfile)
			}
			_, ignoreErr := commit.GetTreeEntryByPath(path.Join(target.Context, ".dockerignore"))
			result.Preflight[target.Name] = hosting_module.CheckDockerfile(dockerfile, ignoreErr == nil)
			continue
		}
		composeContent, err := commit.GetFileContent(target.Compose, maxComposeSize)
		if err != nil {
			return nil, fmt.Errorf("target %q: read %s: %w", target.Name, target.Compose, err)
		}
		file, builds, err := hosting_module.ParseCompose([]byte(composeContent), path.Dir(target.Compose))
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", target.Name, err)
		}
		if target.Service != "" && !slices.Contains(file.Services(), target.Service) {
			return nil, fmt.Errorf("target %q: service %q is not in %s", target.Name, target.Service, target.Compose)
		}
		result.Compose[target.Name] = file
		result.Builds[target.Name] = builds
	}
	return result, nil
}

// maxAppNameLength keeps app names inside Dokploy's 63-character limit,
// leaving room for the random suffix Dokploy appends.
const maxAppNameLength = 48

// dokployAppName is the Swarm service or stack name of a target. Owners are
// often 36-character eNames, so long names keep a readable prefix and end in
// a hash of the full name, which stays stable across deploys.
func dokployAppName(repo *repo_model.Repository, target *hosting_model.Target) string {
	return shortAppName(hosting_module.ImageName(repo.OwnerName, repo.Name, target.Name) + "-" + strconv.FormatInt(target.ID, 36))
}

func shortAppName(name string) string {
	if len(name) <= maxAppNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	prefix := strings.TrimRight(name[:maxAppNameLength-9], "-")
	return prefix + "-" + hex.EncodeToString(sum[:4])
}

func appSpec(repo *repo_model.Repository, target *hosting_model.Target, spec *hosting_module.Target) AppSpec {
	nanoCPUs, _ := spec.Resources.NanoCPUs()
	memory, _ := spec.Resources.MemoryBytes()
	app := AppSpec{
		Name: repo.FullName() + " · " + target.Name, AppName: dokployAppName(repo, target),
		Description: "Managed by GitW3 for " + repo.FullName(),
		Replicas:    spec.Replicas, NanoCPUs: nanoCPUs, MemoryBytes: memory, Port: spec.Port,
	}
	if spec.Healthcheck != nil {
		interval, _ := time.ParseDuration(spec.Healthcheck.Interval)
		timeout, _ := time.ParseDuration(spec.Healthcheck.Timeout)
		app.Healthcheck = &HealthcheckSpec{Path: spec.Healthcheck.Path, Port: spec.Port, Interval: interval, Timeout: timeout}
	}
	return app
}

// EnsureTarget returns the named target of a repository, creating it (with
// its Dokploy app, deployment key and pool domain) on first use. The spec is
// always refreshed from the release being deployed. A non-empty subdomain
// becomes (or replaces) the target's address under the base domain.
func EnsureTarget(ctx context.Context, repo *repo_model.Repository, spec *hosting_module.Target, actor *user_model.User, subdomain string) (*hosting_model.Target, error) {
	target, err := hosting_model.GetTargetByRepoAndName(ctx, repo.ID, spec.Name)
	switch {
	case err == nil:
		if err := syncTargetSpec(ctx, repo, target, spec); err != nil {
			return nil, err
		}
		if subdomain != "" && routable(spec) {
			if _, err := ClaimSubdomain(ctx, target, subdomain, actor.ID); err != nil {
				return nil, err
			}
		}
		return target, nil
	case !errors.Is(err, hosting_model.ErrTargetNotExist):
		return nil, err
	}
	target = &hosting_model.Target{RepoID: repo.ID, Name: spec.Name}
	if err := target.SetSpec(spec); err != nil {
		return nil, err
	}
	if err := hosting_model.CreateTarget(ctx, target); err != nil {
		return nil, err
	}
	if err := provisionTarget(ctx, repo, target, spec, subdomain, actor.ID); err != nil {
		// Leave no half-created target behind; the next deploy starts over.
		if cleanupErr := removeTarget(ctx, target); cleanupErr != nil {
			log.Error("Clean up hosting target %d after failed provisioning: %v", target.ID, cleanupErr)
		}
		return nil, err
	}
	hosting_model.Audit(ctx, actor.ID, repo.ID, target.ID, 0, hosting_model.AuditTargetEnabled, map[string]any{"name": target.Name, "kind": target.Kind})
	return target, nil
}

func provisionTarget(ctx context.Context, repo *repo_model.Repository, target *hosting_model.Target, spec *hosting_module.Target, subdomain string, actorID int64) error {
	c := current()
	publicKey, keyFile, err := w3ds.GenerateDeploymentKey()
	if err != nil {
		return err
	}
	target.PublicKey = publicKey
	target.SetPrivateKey(keyFile)
	if spec.Kind == hosting_module.KindCompose {
		target.DokployComposeID, _, err = c.Dokploy.CreateCompose(ctx, ComposeSpec{
			Name: repo.FullName() + " · " + target.Name, AppName: dokployAppName(repo, target),
			Description: "Managed by GitW3 for " + repo.FullName(),
		})
	} else {
		target.DokployAppID, _, err = c.Dokploy.CreateApp(ctx, appSpec(repo, target, spec))
	}
	if err != nil {
		return fmt.Errorf("create Dokploy app: %w", err)
	}
	if err := hosting_model.UpdateTargetCols(ctx, target, "public_key", "private_key_enc", "dokploy_app_id", "dokploy_compose_id"); err != nil {
		return err
	}
	if subdomain != "" && routable(spec) {
		if _, err := ClaimSubdomain(ctx, target, subdomain, actorID); err != nil {
			return err
		}
	} else if err := attachPoolDomain(ctx, target, spec); err != nil {
		return err
	}
	if spec.Domain != "" {
		if _, err := AddCustomDomain(ctx, target, spec.Domain, 0); err != nil {
			log.Warn("Target %d: custom domain %s from deploy.yml: %v", target.ID, spec.Domain, err)
		}
	}
	return nil
}

func syncTargetSpec(ctx context.Context, repo *repo_model.Repository, target *hosting_model.Target, spec *hosting_module.Target) error {
	previous, err := target.Spec()
	if err != nil {
		return err
	}
	if previous.Kind != spec.Kind {
		return fmt.Errorf("target %q changed kind from %s to %s; delete the target to redeploy it with a new kind", spec.Name, previous.Kind, spec.Kind)
	}
	// auto_deploy in deploy.yml only seeds a new target; afterwards the
	// switch on the Deploy tab owns it.
	autoDeploy := target.AutoDeploy
	if err := target.SetSpec(spec); err != nil {
		return err
	}
	target.AutoDeploy = autoDeploy
	if err := hosting_model.UpdateTargetCols(ctx, target, "config_json", "kind", "replicas"); err != nil {
		return err
	}
	if target.DokployAppID != "" {
		if err := current().Dokploy.UpdateApp(ctx, target.DokployAppID, appSpec(repo, target, spec)); err != nil {
			return fmt.Errorf("update Dokploy app: %w", err)
		}
	}
	if spec.Port != previous.Port || spec.Service != previous.Service {
		return rerouteDomains(ctx, target, spec)
	}
	return nil
}

// DeleteTarget tears a target down: domains, Dokploy app and database rows.
func DeleteTarget(ctx context.Context, target *hosting_model.Target, actor *user_model.User) error {
	if err := removeTarget(ctx, target); err != nil {
		return err
	}
	hosting_model.Audit(ctx, actor.ID, target.RepoID, target.ID, 0, hosting_model.AuditTargetDeleted, map[string]any{"name": target.Name})
	return nil
}

func removeTarget(ctx context.Context, target *hosting_model.Target) error {
	c := current()
	pending, err := hosting_model.ListPendingDeployments(ctx, target.ID, 0)
	if err != nil {
		return err
	}
	for _, deployment := range pending {
		cancelDeployment(ctx, deployment, "target deleted")
	}
	domains, err := hosting_model.ListTargetDomains(ctx, target.ID)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		if err := releaseDomain(ctx, domain); err != nil {
			return err
		}
	}
	if target.DokployAppID != "" {
		if err := c.Dokploy.DeleteApp(ctx, target.DokployAppID); err != nil && !isDokployNotFound(err) {
			return fmt.Errorf("delete Dokploy app: %w", err)
		}
	}
	if target.DokployComposeID != "" {
		if err := c.Dokploy.DeleteCompose(ctx, target.DokployComposeID); err != nil && !isDokployNotFound(err) {
			return fmt.Errorf("delete Dokploy stack: %w", err)
		}
	}
	return hosting_model.DeleteTarget(ctx, target.ID)
}

// DeleteRepoTargets removes every target of a repository that is being deleted.
func DeleteRepoTargets(ctx context.Context, repoID int64) error {
	if !Enabled() {
		return nil
	}
	targets, err := hosting_model.ListTargets(ctx, repoID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := removeTarget(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func isDokployNotFound(err error) bool {
	var dokployErr *DokployError
	return errors.As(err, &dokployErr) && dokployErr.Status == 404
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
