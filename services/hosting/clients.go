// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"
	"fmt"
	"strings"

	actions_model "forgejo.org/models/actions"
	container_model "forgejo.org/models/packages/container"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/setting"
	actions_service "forgejo.org/services/actions"
	container_service "forgejo.org/services/packages/container"
)

// BuildInputs are the workflow_dispatch inputs of the central builder workflow.
type BuildInputs struct {
	JobID       int64
	Spec        string // base64 JSON BuildSpec
	SourceURL   string
	CallbackURL string
}

// BuildState is a coarse view of a builder run.
type BuildState string

const (
	BuildStateWaiting   BuildState = "waiting"
	BuildStateRunning   BuildState = "running"
	BuildStateSucceeded BuildState = "succeeded"
	BuildStateFailed    BuildState = "failed"
	BuildStateCancelled BuildState = "cancelled"
)

// BuilderClient dispatches and controls runs of the central builder workflow.
type BuilderClient interface {
	Dispatch(ctx context.Context, doer *user_model.User, inputs BuildInputs) (runID int64, err error)
	Cancel(ctx context.Context, runID int64) error
	Status(ctx context.Context, runID int64) (BuildState, error)
	RunLink(ctx context.Context, runID int64) string
}

// RegistryClient answers questions about images in the deployments org.
type RegistryClient interface {
	Exists(ctx context.Context, image, digest string) (bool, error)
}

type actionsBuilder struct{}

// NewBuilderClient dispatches the configured builder workflow through
// Forgejo Actions. App repositories can trigger it but never edit it.
func NewBuilderClient() BuilderClient {
	return actionsBuilder{}
}

func builderRepo(ctx context.Context) (*repo_model.Repository, error) {
	owner, name, ok := strings.Cut(setting.Hosting.BuilderRepo, "/")
	if !ok {
		return nil, fmt.Errorf("[hosting] BUILDER_REPO %q must be owner/name", setting.Hosting.BuilderRepo)
	}
	return repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
}

func (actionsBuilder) Dispatch(ctx context.Context, doer *user_model.User, inputs BuildInputs) (int64, error) {
	repo, err := builderRepo(ctx)
	if err != nil {
		return 0, fmt.Errorf("builder repository: %w", err)
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return 0, err
	}
	defer gitRepo.Close()
	ref := setting.Hosting.BuilderRef
	if ref == "" {
		ref = repo.DefaultBranch
	}
	workflow, err := actions_service.GetWorkflowFromCommit(gitRepo, ref, setting.Hosting.BuilderWorkflow)
	if err != nil {
		return 0, fmt.Errorf("builder workflow %s@%s: %w", setting.Hosting.BuilderWorkflow, ref, err)
	}
	values := map[string]string{
		"job_id":       fmt.Sprint(inputs.JobID),
		"spec":         inputs.Spec,
		"source_url":   inputs.SourceURL,
		"callback_url": inputs.CallbackURL,
	}
	run, _, err := workflow.Dispatch(ctx, func(key string) string { return values[key] }, repo, doer)
	if err != nil {
		return 0, err
	}
	return run.ID, nil
}

func (actionsBuilder) Cancel(ctx context.Context, runID int64) error {
	run, err := actions_model.GetRunByID(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.IsDone() {
		return nil
	}
	return actions_service.CancelRun(ctx, run)
}

func (actionsBuilder) Status(ctx context.Context, runID int64) (BuildState, error) {
	run, err := actions_model.GetRunByID(ctx, runID)
	if err != nil {
		return "", err
	}
	return buildStateOf(run.Status), nil
}

func buildStateOf(status actions_model.Status) BuildState {
	switch {
	case status == actions_model.StatusSuccess:
		return BuildStateSucceeded
	case status == actions_model.StatusCancelled:
		return BuildStateCancelled
	case status.IsDone():
		return BuildStateFailed
	case status == actions_model.StatusRunning:
		return BuildStateRunning
	}
	return BuildStateWaiting
}

func (actionsBuilder) RunLink(ctx context.Context, runID int64) string {
	run, err := actions_model.GetRunByID(ctx, runID)
	if err != nil {
		return ""
	}
	if err := run.LoadRepo(ctx); err != nil {
		return ""
	}
	return run.Link()
}

type packagesRegistry struct{}

// NewRegistryClient checks images in Forgejo's own container registry.
func NewRegistryClient() RegistryClient {
	return packagesRegistry{}
}

func (packagesRegistry) Exists(ctx context.Context, image, digest string) (bool, error) {
	owner, err := user_model.GetUserByName(ctx, setting.Hosting.RegistryOwner)
	if err != nil {
		return false, fmt.Errorf("registry owner %q: %w", setting.Hosting.RegistryOwner, err)
	}
	_, err = container_service.WorkaroundGetContainerBlob(ctx, &container_model.BlobSearchOptions{
		OwnerID: owner.ID, Image: strings.ToLower(image), Digest: digest, IsManifest: true,
	})
	if errors.Is(err, container_model.ErrContainerBlobNotExist) {
		return false, nil
	}
	return err == nil, err
}
