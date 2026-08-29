// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"forgejo.org/modules/base"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
	"forgejo.org/services/context"
)

const tplRepoW3DS base.TplName = "repo/w3ds"

// W3DS renders the repository's W3DS platform workspace.
func W3DS(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("platform.repo.title")
	ctx.Data["PageIsW3DS"] = true
	ctx.Data["W3DSOnboarded"] = ctx.FormBool("w3ds_onboarded")
	ctx.Data["W3DSUseAI"] = ctx.FormBool("ai")
	ctx.Data["PlatformManifestPath"] = w3ds.PlatformManifestPath

	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	if manifest != nil {
		status := loadPlatformPublicationStatus(ctx)
		eName := strings.TrimSpace(status.EName)
		if eName == "" && manifest.EName != nil {
			eName = strings.TrimSpace(*manifest.EName)
		}
		ctx.Data["IsW3DSPlatform"] = true
		ctx.Data["PlatformManifest"] = manifest
		ctx.Data["PlatformPublication"] = status
		ctx.Data["PlatformEName"] = eName
		ctx.Data["PlatformIdentityReady"] = eName != ""
		ctx.Data["PlatformPublished"] = status.Status == "published"
	}

	ctx.HTML(http.StatusOK, tplRepoW3DS)
}

func loadPlatformManifest(ctx *context.Context) (*w3ds.PlatformManifest, error) {
	if ctx.Repo.Commit == nil {
		return nil, nil
	}
	content, err := ctx.Repo.Commit.GetFileContent(w3ds.PlatformManifestPath, 64*1024)
	if git.IsErrNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest w3ds.PlatformManifest
	if err := json.Unmarshal([]byte(content), &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func loadPlatformPublicationStatus(ctx *context.Context) *w3ds.PublicationStatus {
	if !setting.PlatformManifestSync.Enabled || setting.PlatformManifestSync.URL == "" || setting.PlatformManifestSync.InternalToken == "" {
		return &w3ds.PublicationStatus{Status: "unavailable"}
	}
	client := &http.Client{Timeout: setting.PlatformManifestSync.Timeout}
	status, err := w3ds.FetchPublicationStatus(ctx, client, setting.PlatformManifestSync.URL, setting.PlatformManifestSync.InternalToken, ctx.Repo.Repository.ID)
	if err == nil {
		return status
	}
	if errors.Is(err, w3ds.ErrPublicationStatusNotFound) {
		return &w3ds.PublicationStatus{Status: "identity_pending"}
	}
	log.Warn("Load platform publication status for repository %d: %v", ctx.Repo.Repository.ID, err)
	return &w3ds.PublicationStatus{Status: "unavailable"}
}
