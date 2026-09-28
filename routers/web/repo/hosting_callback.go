// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"errors"
	"io"
	"net/http"
	"time"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	hosting_service "forgejo.org/services/hosting"
)

const maxBuildCallbackBody = 256 << 10

// HostingBuildCallback receives the signed result of a builder run.
func HostingBuildCallback(ctx *context.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, maxBuildCallbackBody))
	if err != nil {
		ctx.JSON(http.StatusRequestEntityTooLarge, map[string]string{"message": "callback body too large"})
		return
	}
	err = hosting_service.HandleBuildCallback(ctx, body, ctx.Req.Header.Get(hosting_module.CallbackSignatureHeader))
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, hosting_service.ErrCallbackUnauthorized):
		ctx.JSON(http.StatusUnauthorized, map[string]string{"message": err.Error()})
	case errors.Is(err, hosting_service.ErrCallbackReplayed):
		ctx.JSON(http.StatusConflict, map[string]string{"message": err.Error()})
	case errors.Is(err, hosting_service.ErrCallbackRejected):
		ctx.JSON(http.StatusUnprocessableEntity, map[string]string{"message": err.Error()})
	case errors.Is(err, hosting_service.ErrDisabled):
		ctx.JSON(http.StatusNotFound, map[string]string{"message": err.Error()})
	default:
		log.Error("Hosting build callback: %v", err)
		ctx.JSON(http.StatusInternalServerError, map[string]string{"message": "callback failed"})
	}
}

// HostingBuildSource serves the source archive of one build job to the
// builder. Access is granted by a short-lived HMAC-signed URL instead of a
// token, so the untrusted build node never holds a reusable credential.
func HostingBuildSource(ctx *context.Context) {
	jobID := ctx.ParamsInt64("job")
	if !setting.Hosting.Enabled ||
		!hosting_module.VerifySourceURL(setting.Hosting.CallbackSecret, jobID, ctx.FormString("exp"), ctx.FormString("sig"), time.Now()) {
		ctx.Status(http.StatusForbidden)
		return
	}
	job, err := hosting_model.GetBuildJob(ctx, jobID)
	if err != nil || (job.Status != hosting_model.BuildQueued && job.Status != hosting_model.BuildRunning) {
		ctx.Status(http.StatusNotFound)
		return
	}
	deployment, err := hosting_model.GetDeployment(ctx, job.DeploymentID)
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	repo, err := repo_model.GetRepositoryByID(ctx, deployment.RepoID)
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		ctx.ServerError("OpenRepository", err)
		return
	}
	defer gitRepo.Close()
	ctx.Resp.Header().Set("Content-Type", "application/gzip")
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	if err := gitRepo.CreateArchive(ctx, git.TARGZ, ctx.Resp, false, deployment.CommitSHA); err != nil {
		log.Error("Hosting source archive for job %d: %v", jobID, err)
	}
}
