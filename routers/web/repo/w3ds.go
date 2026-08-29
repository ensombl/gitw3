// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"forgejo.org/models"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/base"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	files_service "forgejo.org/services/repository/files"
)

const tplRepoW3DS base.TplName = "repo/w3ds"

type w3dsGuideStep struct {
	Ready   bool   `json:"ready"`
	Tone    string `json:"tone"`
	Label   string `json:"label"`
	Message string `json:"message"`
}

type w3dsReleaseView struct {
	Tag     string
	Version string
	URL     string
}

type w3dsPublicationView struct {
	Status        string        `json:"status"`
	Tone          string        `json:"tone"`
	Title         string        `json:"title"`
	Message       string        `json:"message"`
	EName         string        `json:"ename"`
	LastError     string        `json:"lastError,omitempty"`
	IsDraft       bool          `json:"isDraft"`
	InSubmission  bool          `json:"inSubmission"`
	PPAStatus     string        `json:"ppaStatus"`
	PPALabel      string        `json:"ppaLabel"`
	PPAMessage    string        `json:"ppaMessage"`
	PPAButton     string        `json:"ppaButton"`
	PPAVersion    string        `json:"ppaVersion"`
	PPALevel      string        `json:"ppaLevel,omitempty"`
	ReleaseTag    string        `json:"releaseTag"`
	ReleaseURL    string        `json:"releaseUrl"`
	ReleaseAction string        `json:"releaseAction"`
	Identity      w3dsGuideStep `json:"identity"`
	Marketplace   w3dsGuideStep `json:"marketplace"`
	Application   w3dsGuideStep `json:"application"`
	Domains       w3dsGuideStep `json:"domains"`
	Release       w3dsGuideStep `json:"release"`
}

// W3DS renders the repository's W3DS platform workspace.
func W3DS(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	var form *forms.UpdatePlatformForm
	if manifest != nil {
		form = &forms.UpdatePlatformForm{
			PlatformDisplayName: manifest.DisplayName,
			PlatformDescription: manifest.Description,
			PlatformURL:         manifest.URL,
			PlatformLogoURL:     manifest.LogoURL,
			PlatformDomains:     append([]string(nil), manifest.Domains...),
			LastCommitID:        ctx.Repo.CommitID,
		}
	}
	prepareW3DSPage(ctx, manifest, form)
	ctx.HTML(http.StatusOK, tplRepoW3DS)
}

// W3DSUpdate commits edited platform profile fields back to the repository manifest.
func W3DSUpdate(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.UpdatePlatformForm)
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	if manifest == nil {
		ctx.NotFound("W3DS platform manifest", nil)
		return
	}
	catalog := prepareW3DSPage(ctx, manifest, form)
	if ctx.HasError() {
		ctx.RenderWithErr(ctx.Tr("platform.edit.invalid"), tplRepoW3DS, form)
		return
	}
	if catalog == nil {
		ctx.RenderWithErr(ctx.Tr("platform.domains.unavailable"), tplRepoW3DS, form)
		return
	}
	if err := w3ds.ValidateSelectedDomains(form.PlatformDomains, catalog); err != nil {
		ctx.RenderWithErr(ctx.Tr("platform.domains.invalid", err), tplRepoW3DS, form)
		return
	}

	updated := *manifest
	updated.DisplayName = form.PlatformDisplayName
	updated.Description = form.PlatformDescription
	updated.URL = form.PlatformURL
	updated.LogoURL = form.PlatformLogoURL
	updated.Domains = append([]string(nil), form.PlatformDomains...)
	updated.Category = ""
	if err := updated.Validate(!setting.IsProd); err != nil {
		ctx.RenderWithErr(ctx.Tr("platform.create.invalid_manifest", err), tplRepoW3DS, form)
		return
	}
	err = commitPlatformManifest(ctx, &updated, form.LastCommitID, "chore: update platform profile")
	if err != nil {
		switch {
		case models.IsErrCommitIDDoesNotMatch(err), git.IsErrPushOutOfDate(err):
			ctx.RenderWithErr(ctx.Tr("platform.edit.conflict"), tplRepoW3DS, form)
		case models.IsErrUserCannotCommit(err), models.IsErrFilePathProtected(err), git.IsErrPushRejected(err):
			ctx.RenderWithErr(ctx.Tr("platform.edit.protected"), tplRepoW3DS, form)
		default:
			log.Error("Update W3DS platform manifest for repository %d: %v", ctx.Repo.Repository.ID, err)
			ctx.RenderWithErr(ctx.Tr("platform.edit.failed"), tplRepoW3DS, form)
		}
		return
	}

	ctx.Flash.Success(ctx.Tr("platform.edit.saved"))
	ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
}

// W3DSToggleVisibility publishes or hides a platform by toggling isDraft in its manifest.
func W3DSToggleVisibility(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.PlatformManifestActionForm)
	if ctx.HasError() {
		ctx.Flash.Error(ctx.Tr("platform.action.invalid"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	if manifest == nil {
		ctx.NotFound("W3DS platform manifest", nil)
		return
	}

	updated := *manifest
	updated.IsDraft = !manifest.IsDraft
	if err := commitPlatformManifest(ctx, &updated, form.LastCommitID, "chore: update platform visibility"); err != nil {
		redirectPlatformActionError(ctx, err)
		return
	}
	if updated.IsDraft {
		ctx.Flash.Success(ctx.Tr("platform.visibility.saved_draft"))
	} else {
		ctx.Flash.Success(ctx.Tr("platform.visibility.saved_published"))
	}
	ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
}

// W3DSApplyPPA submits the published PlatformProfile for PPA review.
func W3DSApplyPPA(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.PlatformManifestActionForm)
	if ctx.HasError() {
		ctx.Flash.Error(ctx.Tr("platform.action.invalid"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	if manifest == nil {
		ctx.NotFound("W3DS platform manifest", nil)
		return
	}
	release, err := loadLatestPlatformRelease(ctx)
	if err != nil {
		ctx.ServerError("loadLatestPlatformRelease", err)
		return
	}
	if release == nil || release.Version == "" {
		ctx.Flash.Error(ctx.Tr("platform.ppa.release_missing"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	if manifest.Version != release.Version {
		ctx.Flash.Error(ctx.Tr("platform.ppa.release_syncing", release.Tag))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	status := loadPlatformPublicationStatus(ctx)
	if currentPPADecision(status, release.Version) != nil {
		ctx.Flash.Success(ctx.Tr("platform.ppa.already_decided", release.Version))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	if currentPPASubmission(manifest) {
		ctx.Flash.Success(ctx.Tr("platform.ppa.already_submitted"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	eName := ""
	if manifest.EName != nil {
		eName = strings.TrimSpace(*manifest.EName)
	}
	if eName == "" {
		eName = strings.TrimSpace(status.EName)
	}
	if eName == "" || strings.TrimSpace(manifest.URL) == "" {
		ctx.Flash.Error(ctx.Tr("platform.ppa.requirements_missing"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	catalog, err := preparePlatformDomains(ctx, manifest.Domains)
	if err != nil {
		log.Warn("Load W3DS domain ontology for PPA submission from repository %d: %v", ctx.Repo.Repository.ID, err)
		ctx.Flash.Error(ctx.Tr("platform.domains.unavailable"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	if err := w3ds.ValidateSelectedDomains(manifest.Domains, catalog); err != nil {
		ctx.Flash.Error(ctx.Tr("platform.domains.invalid", err))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}

	updated := *manifest
	updated.EName = &eName
	updated.InSubmission = true
	updated.SubmissionVersion = release.Version
	if err := commitPlatformManifest(ctx, &updated, form.LastCommitID, "chore: submit PPA application"); err != nil {
		redirectPlatformActionError(ctx, err)
		return
	}
	ctx.Flash.Success(ctx.Tr("platform.ppa.submitted_help"))
	ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
}

func commitPlatformManifest(ctx *context.Context, manifest *w3ds.PlatformManifest, lastCommitID, message string) error {
	if err := manifest.Validate(!setting.IsProd); err != nil {
		return err
	}
	content, err := manifest.Marshal()
	if err != nil {
		return err
	}
	_, err = files_service.ChangeRepoFiles(ctx, ctx.Repo.Repository, ctx.Doer, &files_service.ChangeRepoFilesOptions{
		LastCommitID: lastCommitID,
		OldBranch:    ctx.Repo.Repository.DefaultBranch,
		NewBranch:    ctx.Repo.Repository.DefaultBranch,
		Message:      message,
		Files: []*files_service.ChangeRepoFile{{
			Operation:     "update",
			TreePath:      w3ds.PlatformManifestPath,
			ContentReader: strings.NewReader(string(content)),
		}},
	})
	return err
}

func redirectPlatformActionError(ctx *context.Context, err error) {
	switch {
	case models.IsErrCommitIDDoesNotMatch(err), git.IsErrPushOutOfDate(err):
		ctx.Flash.Error(ctx.Tr("platform.edit.conflict"))
	case models.IsErrUserCannotCommit(err), models.IsErrFilePathProtected(err), git.IsErrPushRejected(err):
		ctx.Flash.Error(ctx.Tr("platform.edit.protected"))
	default:
		log.Error("Update W3DS platform manifest for repository %d: %v", ctx.Repo.Repository.ID, err)
		ctx.Flash.Error(ctx.Tr("platform.edit.failed"))
	}
	ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
}

func prepareW3DSPage(ctx *context.Context, manifest *w3ds.PlatformManifest, form *forms.UpdatePlatformForm) *w3ds.DomainCatalog {
	ctx.Data["Title"] = ctx.Tr("platform.repo.title")
	ctx.Data["PageIsW3DS"] = true
	ctx.Data["W3DSOnboarded"] = ctx.FormBool("w3ds_onboarded")
	ctx.Data["W3DSUseAI"] = ctx.FormBool("ai")
	ctx.Data["PlatformManifestPath"] = w3ds.PlatformManifestPath
	ctx.Data["PlatformEditForm"] = form
	selectedDomains := []string(nil)
	if form != nil {
		selectedDomains = form.PlatformDomains
	}
	catalog, ontologyErr := preparePlatformDomains(ctx, selectedDomains)
	if ontologyErr != nil {
		log.Warn("Load W3DS domain ontology for repository %d: %v", ctx.Repo.Repository.ID, ontologyErr)
	}
	canEdit := ctx.Repo.CanWrite(unit.TypeCode) && !ctx.Repo.Repository.IsArchived
	ctx.Data["CanEditW3DS"] = canEdit
	ctx.Data["PlatformLastCommitID"] = ctx.Repo.CommitID
	if manifest == nil {
		return catalog
	}
	status := loadPlatformPublicationStatus(ctx)
	release, err := loadLatestPlatformRelease(ctx)
	if err != nil {
		log.Warn("Load latest release for W3DS repository %d: %v", ctx.Repo.Repository.ID, err)
	}
	eName := strings.TrimSpace(status.EName)
	if eName == "" && manifest.EName != nil {
		eName = strings.TrimSpace(*manifest.EName)
	}
	ctx.Data["IsW3DSPlatform"] = true
	releaseSynced := release != nil && release.Version != "" && manifest.Version == release.Version
	version := ""
	if release != nil {
		version = release.Version
	}
	pending := releaseSynced && currentPPASubmission(manifest)
	decision := currentPPADecision(status, version)
	domainsReady := len(manifest.Domains) > 0
	publication := newW3DSPublicationView(ctx, status, eName, release, releaseSynced, manifest.URL, domainsReady, manifest.IsDraft, pending, decision)
	ctx.Data["PlatformManifest"] = manifest
	ctx.Data["PlatformRelease"] = release
	ctx.Data["PlatformPublication"] = publication
	ctx.Data["PlatformEName"] = eName
	ctx.Data["PlatformIdentityReady"] = eName != ""
	ctx.Data["PlatformPublished"] = publication.Marketplace.Ready
	ctx.Data["PPARequirementsReady"] = eName != "" && strings.TrimSpace(manifest.URL) != "" && domainsReady && releaseSynced
	ctx.Data["CanApplyPPA"] = canEdit && publication.PPAStatus == "ready"
	return catalog
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
	release, err := loadLatestPlatformRelease(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]string{"message": ctx.Locale.TrString("platform.status.refresh_failed")})
		return
	}
	eName := strings.TrimSpace(status.EName)
	if eName == "" && manifest.EName != nil {
		eName = strings.TrimSpace(*manifest.EName)
	}
	releaseSynced := release != nil && release.Version != "" && manifest.Version == release.Version
	version := ""
	if release != nil {
		version = release.Version
	}
	pending := releaseSynced && currentPPASubmission(manifest)
	decision := currentPPADecision(status, version)
	ctx.JSON(http.StatusOK, newW3DSPublicationView(ctx, status, eName, release, releaseSynced, manifest.URL, len(manifest.Domains) > 0, manifest.IsDraft, pending, decision))
}

func newW3DSPublicationView(ctx *context.Context, status *w3ds.PublicationStatus, eName string, release *w3dsReleaseView, releaseSynced bool, applicationURL string, domainsReady, isDraft, inSubmission bool, decision *w3ds.AccreditationDecision) w3dsPublicationView {
	version := ""
	if release != nil {
		version = release.Version
	}
	view := w3dsPublicationView{
		Status:       status.Status,
		Tone:         "info",
		EName:        eName,
		IsDraft:      isDraft,
		InSubmission: inSubmission,
		PPAVersion:   version,
	}
	if release != nil {
		view.ReleaseTag = release.Tag
		view.ReleaseURL = release.URL
		view.ReleaseAction = ctx.Locale.TrString("platform.repo.view_release")
	} else {
		view.ReleaseAction = ctx.Locale.TrString("platform.repo.create_release")
	}
	switch status.Status {
	case "published":
		view.Tone = "positive"
		if isDraft {
			view.Title = ctx.Locale.TrString("platform.visibility.draft_synced")
			view.Message = ctx.Locale.TrString("platform.visibility.draft_synced_help")
		} else {
			view.Title = ctx.Locale.TrString("platform.status.published")
			view.Message = ctx.Locale.TrString("platform.status.published_help", eName)
		}
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
	view.Marketplace.Ready = status.Status == "published" && !isDraft
	if view.Marketplace.Ready {
		view.Marketplace.Tone = "green"
		view.Marketplace.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Marketplace.Message = ctx.Locale.TrString("platform.repo.step_marketplace_ready")
	} else {
		view.Marketplace.Tone = "blue"
		if status.Status == "published" && isDraft {
			view.Marketplace.Label = ctx.Locale.TrString("platform.visibility.draft")
			view.Marketplace.Message = ctx.Locale.TrString("platform.repo.step_marketplace_draft")
		} else {
			view.Marketplace.Label = ctx.Locale.TrString("platform.repo.waiting")
			view.Marketplace.Message = ctx.Locale.TrString("platform.repo.step_marketplace_pending")
		}
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
	view.Domains.Ready = domainsReady
	if view.Domains.Ready {
		view.Domains.Tone = "green"
		view.Domains.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Domains.Message = ctx.Locale.TrString("platform.repo.step_domains_ready")
	} else {
		view.Domains.Tone = "grey"
		view.Domains.Label = ctx.Locale.TrString("platform.repo.waiting")
		view.Domains.Message = ctx.Locale.TrString("platform.repo.step_domains_missing")
	}
	view.Release.Ready = releaseSynced
	switch {
	case release == nil:
		view.Release.Tone = "grey"
		view.Release.Label = ctx.Locale.TrString("platform.repo.waiting")
		view.Release.Message = ctx.Locale.TrString("platform.repo.release_missing")
	case release.Version == "":
		view.Release.Tone = "grey"
		view.Release.Label = ctx.Locale.TrString("platform.repo.waiting")
		view.Release.Message = ctx.Locale.TrString("platform.repo.release_invalid", release.Tag)
	case !releaseSynced:
		view.Release.Tone = "blue"
		view.Release.Label = ctx.Locale.TrString("platform.repo.automatic")
		view.Release.Message = ctx.Locale.TrString("platform.repo.release_syncing", release.Tag)
	default:
		view.Release.Tone = "green"
		view.Release.Label = ctx.Locale.TrString("platform.repo.ready")
		view.Release.Message = ctx.Locale.TrString("platform.repo.release_ready", release.Tag)
	}

	switch {
	case view.Release.Ready && decision != nil && decision.Decision == "granted":
		view.PPAStatus = "granted"
		view.PPALevel = decision.Level
		view.PPALabel = ctx.Locale.TrString("platform.ppa.granted")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.granted")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.granted_help", decision.Level, version)
	case view.Release.Ready && decision != nil && decision.Decision == "denied":
		view.PPAStatus = "denied"
		view.PPALabel = ctx.Locale.TrString("platform.ppa.denied")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.denied")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.denied_help", version)
	case inSubmission:
		view.PPAStatus = "submitted"
		view.PPALabel = ctx.Locale.TrString("platform.ppa.submitted")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.submitted")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.submitted_version_help", version)
	case view.Identity.Ready && view.Application.Ready && view.Domains.Ready && view.Release.Ready:
		view.PPAStatus = "ready"
		view.PPALabel = ctx.Locale.TrString("platform.ppa.ready_to_apply")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.apply")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.apply_version_help", version)
	default:
		view.PPAStatus = "incomplete"
		view.PPAButton = ctx.Locale.TrString("platform.ppa.apply")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.requirements_help")
	}
	return view
}

func currentPPASubmission(manifest *w3ds.PlatformManifest) bool {
	if manifest == nil || !manifest.InSubmission {
		return false
	}
	// Older manifests did not record submissionVersion; treat their flag as a
	// submission for their current version until the version is edited in GitW3.
	return manifest.SubmissionVersion == "" || manifest.SubmissionVersion == manifest.Version
}

func currentPPADecision(status *w3ds.PublicationStatus, version string) *w3ds.AccreditationDecision {
	if status == nil || status.Decision == nil || status.Decision.PlatformVersion != version {
		return nil
	}
	return status.Decision
}

func loadLatestPlatformRelease(ctx *context.Context) (*w3dsReleaseView, error) {
	release, err := repo_model.GetLatestReleaseByRepoID(ctx, ctx.Repo.Repository.ID)
	if repo_model.IsErrReleaseNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
	if !valid {
		version = ""
	}
	release.Repo = ctx.Repo.Repository
	return &w3dsReleaseView{Tag: release.TagName, Version: version, URL: release.Link()}, nil
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
