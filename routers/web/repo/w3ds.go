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

type w3dsGuideStep struct {
	Ready   bool   `json:"ready"`
	Tone    string `json:"tone"`
	Label   string `json:"label"`
	Message string `json:"message"`
}

type w3dsPublicationView struct {
	Status      string        `json:"status"`
	Tone        string        `json:"tone"`
	Title       string        `json:"title"`
	Message     string        `json:"message"`
	EName       string        `json:"ename"`
	LastError   string        `json:"lastError,omitempty"`
	Identity    w3dsGuideStep `json:"identity"`
	Marketplace w3dsGuideStep `json:"marketplace"`
	Application w3dsGuideStep `json:"application"`
}

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

// W3DSStatus returns the current publication state for the repository workspace.
func W3DSStatus(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]string{"message": ctx.Locale.TrString("platform.status.refresh_failed")})
		return
	}
	if manifest == nil {
		ctx.JSON(http.StatusNotFound, map[string]string{"message": ctx.Locale.TrString("platform.repo.setup_help")})
		return
	}
	status := loadPlatformPublicationStatus(ctx)
	eName := strings.TrimSpace(status.EName)
	if eName == "" && manifest.EName != nil {
		eName = strings.TrimSpace(*manifest.EName)
	}
	ctx.JSON(http.StatusOK, newW3DSPublicationView(ctx, status, eName, manifest.URL))
}

func newW3DSPublicationView(ctx *context.Context, status *w3ds.PublicationStatus, eName, applicationURL string) w3dsPublicationView {
	view := w3dsPublicationView{
		Status: status.Status,
		Tone:   "info",
		EName:  eName,
	}
	switch status.Status {
	case "published":
		view.Tone = "positive"
		view.Title = ctx.Locale.TrString("platform.status.published")
		view.Message = ctx.Locale.TrString("platform.status.published_help", eName)
	case "failed":
		view.Tone = "warning"
		view.Title = ctx.Locale.TrString("platform.status.failed")
		view.Message = ctx.Locale.TrString("platform.status.failed_help")
		if ctx.Repo.IsAdmin() {
			view.LastError = status.LastError
		}
	case "archived":
		view.Tone = "warning"
		view.Title = ctx.Locale.TrString("platform.status.archived")
	case "publishing":
		view.Title = ctx.Locale.TrString("platform.status.publishing")
		view.Message = ctx.Locale.TrString("platform.status.pending_help")
	case "unavailable":
		view.Title = ctx.Locale.TrString("platform.status.unavailable")
		view.Message = ctx.Locale.TrString("platform.status.unavailable_help")
	default:
		view.Status = "identity_pending"
		view.Title = ctx.Locale.TrString("platform.status.identity_pending")
		view.Message = ctx.Locale.TrString("platform.status.pending_help")
	}

	view.Identity.Ready = eName != ""
	if view.Identity.Ready {
		view.Identity.Tone = "green"
		view.Identity.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Identity.Message = ctx.Locale.TrString("platform.repo.step_identity_ready", eName)
	} else {
		view.Identity.Tone = "blue"
		view.Identity.Label = ctx.Locale.TrString("platform.repo.automatic")
		view.Identity.Message = ctx.Locale.TrString("platform.repo.step_identity_pending")
	}
	view.Marketplace.Ready = status.Status == "published"
	if view.Marketplace.Ready {
		view.Marketplace.Tone = "green"
		view.Marketplace.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Marketplace.Message = ctx.Locale.TrString("platform.repo.step_marketplace_ready")
	} else {
		view.Marketplace.Tone = "blue"
		view.Marketplace.Label = ctx.Locale.TrString("platform.repo.waiting")
		view.Marketplace.Message = ctx.Locale.TrString("platform.repo.step_marketplace_pending")
	}
	view.Application.Ready = applicationURL != ""
	if view.Application.Ready {
		view.Application.Tone = "green"
		view.Application.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Application.Message = ctx.Locale.TrString("platform.repo.step_application_ready", applicationURL)
	} else {
		view.Application.Tone = "grey"
		view.Application.Label = ctx.Locale.TrString("platform.repo.waiting")
		view.Application.Message = ctx.Locale.TrString("platform.repo.step_application_missing")
	}
	return view
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
