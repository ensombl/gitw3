// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strings"

	"forgejo.org/models/unit"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/w3ds"
	"forgejo.org/services/context"
)

const tplRepoW3DSWelcome base.TplName = "repo/w3ds_welcome"

type w3dsWelcomeVersion struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	EName   string `json:"ename"`
	URL     string `json:"url"`
}

type w3dsWelcomeView struct {
	Ready     bool                 `json:"ready"`
	Status    string               `json:"status"`
	EName     string               `json:"ename"`
	Message   string               `json:"message"`
	LastError string               `json:"lastError,omitempty"`
	Versions  []w3dsWelcomeVersion `json:"versions"`
}

// W3DSWelcome renders the final step of new-platform creation.
func W3DSWelcome(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	if manifest == nil {
		ctx.NotFound("W3DS platform manifest", nil)
		return
	}
	view, err := newW3DSWelcomeView(ctx, manifest)
	if err != nil {
		ctx.ServerError("newW3DSWelcomeView", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("platform.welcome.title")
	ctx.Data["PageIsW3DS"] = true
	ctx.Data["PlatformWelcome"] = view
	ctx.Data["W3DSUseAI"] = ctx.FormBool("ai")
	ctx.Data["CanCreateRelease"] = ctx.Repo.CanWrite(unit.TypeReleases) && !ctx.Repo.Repository.IsArchived
	ctx.HTML(http.StatusOK, tplRepoW3DSWelcome)
}

// W3DSWelcomeStatus returns the identity and released-version records shown on
// the final creation screen.
func W3DSWelcomeStatus(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]string{"message": ctx.Locale.TrString("platform.status.refresh_failed")})
		return
	}
	if manifest == nil {
		ctx.JSON(http.StatusNotFound, map[string]string{"message": ctx.Locale.TrString("platform.repo.setup_help")})
		return
	}
	view, err := newW3DSWelcomeView(ctx, manifest)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]string{"message": ctx.Locale.TrString("platform.status.refresh_failed")})
		return
	}
	ctx.JSON(http.StatusOK, view)
}

func newW3DSWelcomeView(ctx *context.Context, manifest *w3ds.PlatformManifest) (w3dsWelcomeView, error) {
	status := loadPlatformPublicationStatus(ctx)
	eName := strings.TrimSpace(status.EName)
	if eName == "" && manifest.EName != nil {
		eName = strings.TrimSpace(*manifest.EName)
	}
	view := w3dsWelcomeView{
		Ready:   eName != "",
		Status:  status.Status,
		EName:   eName,
		Message: ctx.Locale.TrString("platform.welcome.reserving"),
	}
	if eName != "" {
		view.Message = ctx.Locale.TrString("platform.welcome.ready")
	}
	if status.Status == "failed" && ctx.Repo.IsAdmin() {
		view.LastError = status.LastError
	}
	releases, err := deploymentReleases(ctx)
	if err != nil {
		return w3dsWelcomeView{}, err
	}
	view.Versions = make([]w3dsWelcomeVersion, 0, len(releases))
	if eName == "" {
		return view, nil
	}
	for _, release := range releases {
		versionEName, err := w3ds.SoftwareVersionEName(eName, release.Version)
		if err != nil {
			log.Warn("Derive software version eName for repository %d release %q: %v", ctx.Repo.Repository.ID, release.Tag, err)
			continue
		}
		view.Versions = append(view.Versions, w3dsWelcomeVersion{
			Tag: release.Tag, Version: release.Version, EName: versionEName, URL: release.URL,
		})
	}
	return view, nil
}
