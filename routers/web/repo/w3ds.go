// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	gocontext "context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	w3ds_model "forgejo.org/models/w3ds"
	"forgejo.org/modules/base"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/w3ds"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	files_service "forgejo.org/services/repository/files"
)

const (
	tplRepoW3DS             base.TplName = "repo/w3ds"
	platformManifestMaxSize              = 512 * 1024
)

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

type w3dsPPAHistoryEvent struct {
	Kind      string `json:"kind"`
	Tone      string `json:"tone"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Actor     string `json:"actor,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type w3dsPublicationView struct {
	Status           string                `json:"status"`
	Tone             string                `json:"tone"`
	Title            string                `json:"title"`
	Message          string                `json:"message"`
	EName            string                `json:"ename"`
	LastError        string                `json:"lastError,omitempty"`
	IsDraft          bool                  `json:"isDraft"`
	InSubmission     bool                  `json:"inSubmission"`
	PPAStatus        string                `json:"ppaStatus"`
	PPALabel         string                `json:"ppaLabel"`
	PPAMessage       string                `json:"ppaMessage"`
	PPAButton        string                `json:"ppaButton"`
	PPAActionMessage string                `json:"ppaActionMessage"`
	PPAVersion       string                `json:"ppaVersion"`
	PPALevel         string                `json:"ppaLevel,omitempty"`
	PPADecidedAt     string                `json:"ppaDecidedAt,omitempty"`
	PPAHistory       []w3dsPPAHistoryEvent `json:"ppaHistory"`
	ReleaseTag       string                `json:"releaseTag"`
	ReleaseURL       string                `json:"releaseUrl"`
	ReleaseAction    string                `json:"releaseAction"`
	Identity         w3dsGuideStep         `json:"identity"`
	Application      w3dsGuideStep         `json:"application"`
	Domains          w3dsGuideStep         `json:"domains"`
	Release          w3dsGuideStep         `json:"release"`
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
	if manifest.DisplayName != updated.DisplayName || manifest.Description != updated.Description || manifest.URL != updated.URL || manifest.LogoURL != updated.LogoURL || !slices.Equal(manifest.Domains, updated.Domains) {
		updated.InSubmission = false
		updated.SubmissionVersion = ""
		updated.SubmissionProof = nil
	}
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

const ppaSigningLifetime = 15 * time.Minute

// W3DSCreatePPASigningSession starts the documented w3ds://sign flow. The
// repository is not submitted until the wallet callback is verified.
func W3DSCreatePPASigningSession(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	if manifest == nil {
		ppaJSONError(ctx, http.StatusNotFound, ctx.Locale.TrString("platform.repo.setup_help"))
		return
	}
	release, err := loadLatestPlatformRelease(ctx)
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	if release == nil || release.Version == "" {
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.ppa.release_missing"))
		return
	}
	if manifest.Version != release.Version {
		ppaJSONError(ctx, http.StatusConflict, ctx.Locale.TrString("platform.ppa.release_syncing", release.Tag))
		return
	}
	status := loadPlatformPublicationStatus(ctx)
	decision := currentPPADecision(status, release.Version)
	if decision != nil && decision.Decision == "granted" {
		ppaJSONError(ctx, http.StatusConflict, ctx.Locale.TrString("platform.ppa.already_decided", release.Version))
		return
	}
	if currentPPASubmission(manifest) && (decision == nil || submissionSupersedesDecision(manifest, decision)) {
		ppaJSONError(ctx, http.StatusConflict, ctx.Locale.TrString("platform.ppa.already_submitted"))
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
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.ppa.requirements_missing"))
		return
	}
	catalog, err := preparePlatformDomains(ctx, manifest.Domains)
	if err != nil {
		log.Warn("Load W3DS domain ontology for PPA submission from repository %d: %v", ctx.Repo.Repository.ID, err)
		ppaJSONError(ctx, http.StatusServiceUnavailable, ctx.Locale.TrString("platform.domains.unavailable"))
		return
	}
	if err := w3ds.ValidateSelectedDomains(manifest.Domains, catalog); err != nil {
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.domains.invalid", err))
		return
	}
	signerEName, err := w3dsENameForUser(ctx, ctx.Doer)
	if err != nil {
		log.Error("Resolve W3DS signer for user %d: %v", ctx.Doer.ID, err)
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	if signerEName == "" {
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.ppa.wallet_required"))
		return
	}
	responseToDecision := strings.TrimSpace(ctx.FormString("response_to_decision"))
	if decision != nil && decision.Decision == "denied" && responseToDecision == "" {
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.ppa.response_required"))
		return
	}
	if utf8.RuneCountInString(responseToDecision) > 2048 {
		ppaJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.ppa.response_too_long"))
		return
	}
	if decision == nil {
		responseToDecision = ""
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	now := time.Now().UTC()
	statement := w3ds.PPASubmissionStatement{
		Type:             w3ds.PPASubmissionStatementType,
		SchemaVersion:    1,
		RepositoryID:     ctx.Repo.Repository.ID,
		Repository:       ctx.Repo.Repository.FullName(),
		PlatformEName:    eName,
		PlatformName:     manifest.PlatformName,
		ReleaseTag:       release.Tag,
		Version:          release.Version,
		ManifestCommitID: ctx.Repo.CommitID,
		Domains:          append([]string(nil), manifest.Domains...),
		SignerEName:      signerEName,
		IssuedAt:         now.Format(time.RFC3339),
		Nonce:            base64.RawURLEncoding.EncodeToString(nonce),
	}
	if decision != nil && decision.Decision == "denied" {
		statement.PreviousDecision = decision.Decision
		statement.PreviousDecisionAt = decision.CreatedAt
		statement.ResponseToDecision = responseToDecision
	}
	payload, err := statement.SigningPayload()
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	statementJSON, err := json.Marshal(statement)
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	expiresAt := now.Add(ppaSigningLifetime)
	if err := w3ds_model.CreatePPASigningSession(ctx, &w3ds_model.PPASigningSession{
		ID:               payload,
		RepositoryID:     ctx.Repo.Repository.ID,
		UserID:           ctx.Doer.ID,
		Version:          release.Version,
		ReleaseTag:       release.Tag,
		ManifestCommitID: ctx.Repo.CommitID,
		Statement:        string(statementJSON),
		Status:           w3ds_model.PPASigningPending,
		ExpiresUnix:      timeutil.TimeStamp(expiresAt.Unix()),
	}); err != nil {
		log.Error("Create PPA signing session for repository %d: %v", ctx.Repo.Repository.ID, err)
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}

	callbackURL := strings.TrimRight(setting.AppURL, "/") + "/w3ds/ppa/callback"
	query := url.Values{}
	query.Set("session", payload)
	displayJSON, err := json.Marshal(map[string]string{
		"message":   "Submit " + manifest.DisplayName + " " + release.Version + " for PPA review",
		"sessionId": payload,
	})
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	query.Set("data", base64.StdEncoding.EncodeToString(displayJSON))
	query.Set("redirect_uri", callbackURL)
	ctx.JSON(http.StatusCreated, map[string]any{
		"sessionId": payload,
		"uri":       "w3ds://sign?" + query.Encode(),
		"expiresAt": expiresAt.Format(time.RFC3339),
		"statusUrl": ctx.Repo.Repository.Link() + "/w3ds/ppa/" + url.PathEscape(payload),
	})
}

type w3dsWalletCallback struct {
	SessionID string `json:"sessionId"`
	Signature string `json:"signature"`
	W3ID      string `json:"w3id"`
	EName     string `json:"ename"`
	Message   string `json:"message"`
}

// W3DSPPACallback receives the cross-device eID wallet response. It carries no
// ambient authority: the one-time session, expected signer, repository admin
// permission, Registry certificate, and wallet signature are all rechecked.
func W3DSPPACallback(ctx *context.Context) {
	setW3DSWalletCORS(ctx)
	if ctx.Req.Method == http.MethodOptions {
		ctx.Status(http.StatusNoContent)
		return
	}
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64*1024)
	var callback w3dsWalletCallback
	if err := json.NewDecoder(ctx.Req.Body).Decode(&callback); err != nil {
		ppaJSONError(ctx, http.StatusBadRequest, "Invalid wallet response.")
		return
	}
	callback.SessionID = strings.TrimSpace(callback.SessionID)
	callback.Signature = strings.TrimSpace(callback.Signature)
	callback.W3ID = strings.TrimSpace(callback.W3ID)
	if callback.W3ID == "" {
		callback.W3ID = strings.TrimSpace(callback.EName)
	}
	if callback.SessionID == "" || callback.Signature == "" || callback.W3ID == "" || callback.Message != callback.SessionID {
		ppaJSONError(ctx, http.StatusBadRequest, "The wallet response is incomplete.")
		return
	}

	session, err := w3ds_model.GetPPASigningSession(ctx, callback.SessionID)
	if err != nil {
		log.Error("Load PPA signing session: %v", err)
		ppaJSONError(ctx, http.StatusInternalServerError, "Could not load the signing request.")
		return
	}
	if session == nil || session.Status != w3ds_model.PPASigningPending {
		ppaJSONError(ctx, http.StatusConflict, "This signing request is unknown, expired, or already used.")
		return
	}
	var statement w3ds.PPASubmissionStatement
	if err := json.Unmarshal([]byte(session.Statement), &statement); err != nil {
		finishPPASigningSession(ctx, session.ID, w3ds_model.PPASigningRejected, "invalid_statement")
		ppaJSONError(ctx, http.StatusInternalServerError, "The stored release statement is invalid.")
		return
	}
	if callback.W3ID != statement.SignerEName {
		ppaJSONError(ctx, http.StatusForbidden, "Use the eID wallet connected to the GitW3 owner or administrator who started this request.")
		return
	}

	httpClient := &http.Client{Timeout: setting.PlatformManifestSync.SignatureTimeout}
	verification, err := w3ds.VerifyWalletSignature(ctx, httpClient, setting.PlatformManifestSync.RegistryURL, callback.W3ID, callback.Signature, callback.SessionID)
	if err != nil {
		claimed, claimErr := w3ds_model.ClaimPPASigningSession(ctx, session.ID)
		if claimErr != nil {
			log.Error("Claim failed PPA signing session: %v", claimErr)
		}
		if claimed {
			finishPPASigningSession(ctx, session.ID, w3ds_model.PPASigningRejected, "verification_unavailable")
		}
		log.Warn("Verify PPA wallet signature for repository %d: %v", session.RepositoryID, err)
		ppaJSONError(ctx, http.StatusBadGateway, "GitW3 could not verify this wallet signature. Start a new signing request and try again.")
		return
	}
	if !verification.Valid {
		claimed, _ := w3ds_model.ClaimPPASigningSession(ctx, session.ID)
		if claimed {
			finishPPASigningSession(ctx, session.ID, w3ds_model.PPASigningRejected, "invalid_signature")
		}
		ppaJSONError(ctx, http.StatusUnauthorized, "The wallet signature is not valid.")
		return
	}
	claimed, err := w3ds_model.ClaimPPASigningSession(ctx, session.ID)
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, "Could not claim the signing request.")
		return
	}
	if !claimed {
		ppaJSONError(ctx, http.StatusConflict, "This signing request is expired or already used.")
		return
	}
	if err := completePPASubmission(ctx, session, &statement, callback.Signature, verification); err != nil {
		log.Warn("Complete signed PPA submission for repository %d: %v", session.RepositoryID, err)
		finishPPASigningSession(ctx, session.ID, w3ds_model.PPASigningRejected, "repository_changed")
		ppaJSONError(ctx, http.StatusConflict, "The repository or release changed while you were signing. Start a new request so you can review the current statement.")
		return
	}
	if err := w3ds_model.FinishPPASigningSession(ctx, session.ID, w3ds_model.PPASigningCompleted, ""); err != nil {
		log.Error("Finish PPA signing session %q: %v", session.ID, err)
	}
	ctx.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// W3DSPPASigningStatus lets the originating repository page follow a
// cross-device wallet callback without granting access to other users.
func W3DSPPASigningStatus(ctx *context.Context) {
	session, err := w3ds_model.GetPPASigningSession(ctx, ctx.Params("session"))
	if err != nil {
		ppaJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.ppa.signing_failed"))
		return
	}
	if session == nil || session.RepositoryID != ctx.Repo.Repository.ID || session.UserID != ctx.Doer.ID {
		ppaJSONError(ctx, http.StatusNotFound, ctx.Locale.TrString("platform.ppa.signing_unknown"))
		return
	}
	response := map[string]any{"status": session.Status}
	if session.Status == w3ds_model.PPASigningCompleted {
		response["redirect"] = ctx.Repo.Repository.Link() + "/w3ds"
	}
	if session.Status == w3ds_model.PPASigningRejected || session.Status == w3ds_model.PPASigningExpired {
		response["message"] = ctx.Locale.TrString("platform.ppa.signing_restart")
	}
	ctx.JSON(http.StatusOK, response)
}

func completePPASubmission(ctx gocontext.Context, session *w3ds_model.PPASigningSession, statement *w3ds.PPASubmissionStatement, signature string, verification *w3ds.SignatureVerification) error {
	repository, err := repo_model.GetRepositoryByID(ctx, session.RepositoryID)
	if err != nil {
		return err
	}
	user, err := user_model.GetUserByID(ctx, session.UserID)
	if err != nil {
		return err
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if err != nil {
		return err
	}
	if !permission.IsAdmin() || repository.IsArchived {
		return errors.New("signer is no longer a repository administrator")
	}
	expectedEName, err := w3dsENameForUser(ctx, user)
	if err != nil || expectedEName == "" || expectedEName != statement.SignerEName {
		return errors.New("signer is no longer connected to the expected W3DS identity")
	}

	manifest, commitID, err := loadPlatformManifestForRepository(ctx, repository)
	if err != nil {
		return err
	}
	if manifest == nil || commitID != session.ManifestCommitID {
		return errors.New("platform manifest changed during signing")
	}
	release, err := repo_model.GetLatestReleaseByRepoID(ctx, repository.ID)
	if err != nil {
		return err
	}
	version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
	if !valid || release.TagName != session.ReleaseTag || version != session.Version || manifest.Version != version {
		return errors.New("platform release changed during signing")
	}
	if manifest.EName == nil || *manifest.EName != statement.PlatformEName || manifest.PlatformName != statement.PlatformName || !slices.Equal(manifest.Domains, statement.Domains) {
		return errors.New("signed statement no longer matches the platform manifest")
	}
	payload, err := statement.SigningPayload()
	if err != nil || payload != session.ID {
		return errors.New("signed statement payload does not match the session")
	}
	status := loadPlatformPublicationStatusForRepository(ctx, repository.ID)
	decision := currentPPADecision(status, version)
	if decision != nil && decision.Decision == "granted" {
		return errors.New("platform release already has a granted decision")
	}
	if decision != nil && decision.Decision == "denied" {
		if statement.PreviousDecision != decision.Decision || statement.PreviousDecisionAt != decision.CreatedAt {
			return errors.New("PPA decision changed during signing")
		}
	} else if statement.PreviousDecision != "" || statement.PreviousDecisionAt != "" {
		return errors.New("PPA decision changed during signing")
	}
	if currentPPASubmission(manifest) && (decision == nil || submissionSupersedesDecision(manifest, decision)) {
		return errors.New("platform release is already submitted")
	}

	proof := &w3ds.PPASubmissionProof{
		Statement:             *statement,
		Payload:               session.ID,
		Signature:             signature,
		PublicKey:             verification.PublicKey,
		KeyBindingCertificate: verification.KeyBindingCertificate,
		VerifiedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	history, err := loadPPASubmissionHistoryForRepository(ctx, repository, commitID)
	if err != nil {
		return err
	}
	history = appendUniquePPASubmissionProofs(history, *proof)

	updated := *manifest
	updated.InSubmission = true
	updated.SubmissionVersion = version
	updated.SubmissionProof = proof
	updated.SubmissionHistory = history
	return commitPlatformManifestForUser(ctx, repository, user, &updated, commitID, "chore: submit signed PPA application")
}

func loadPlatformManifestForRepository(ctx gocontext.Context, repository *repo_model.Repository) (*w3ds.PlatformManifest, string, error) {
	gitRepository, err := gitrepo.OpenRepository(ctx, repository)
	if err != nil {
		return nil, "", err
	}
	defer gitRepository.Close()
	commit, err := gitRepository.GetBranchCommit(repository.DefaultBranch)
	if err != nil {
		return nil, "", err
	}
	content, err := commit.GetFileContent(w3ds.PlatformManifestPath, platformManifestMaxSize)
	if git.IsErrNotExist(err) {
		return nil, commit.ID.String(), nil
	}
	if err != nil {
		return nil, "", err
	}
	var manifest w3ds.PlatformManifest
	if err := json.Unmarshal([]byte(content), &manifest); err != nil {
		return nil, "", err
	}
	return &manifest, commit.ID.String(), nil
}

func w3dsENameForUser(ctx gocontext.Context, user *user_model.User) (string, error) {
	if user == nil {
		return "", nil
	}
	source, err := auth_model.GetActiveOAuth2SourceByName(ctx, "W3DS")
	if err != nil {
		return "", nil
	}
	links, err := db.Find[user_model.ExternalLoginUser](ctx, user_model.FindExternalUserOptions{UserID: user.ID})
	if err != nil {
		return "", err
	}
	for _, link := range links {
		if link.LoginSourceID == source.ID && strings.TrimSpace(link.ExternalID) != "" {
			return normalizeW3DSEName(link.ExternalID), nil
		}
	}
	if user.LoginSource == source.ID {
		return normalizeW3DSEName(user.Name), nil
	}
	return "", nil
}

func normalizeW3DSEName(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && !strings.HasPrefix(value, "@") {
		return "@" + value
	}
	return value
}

func finishPPASigningSession(ctx gocontext.Context, id, status, failure string) {
	if err := w3ds_model.FinishPPASigningSession(ctx, id, status, failure); err != nil {
		log.Error("Finish PPA signing session %q: %v", id, err)
	}
}

func ppaJSONError(ctx *context.Context, status int, message string) {
	ctx.JSON(status, map[string]string{"message": message})
}

func setW3DSWalletCORS(ctx *context.Context) {
	origin := ctx.Req.Header.Get("Origin")
	if origin != "" {
		ctx.Resp.Header().Set("Access-Control-Allow-Origin", origin)
		ctx.Resp.Header().Set("Vary", "Origin")
	}
	ctx.Resp.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	ctx.Resp.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	ctx.Resp.Header().Set("Access-Control-Max-Age", "600")
}

func commitPlatformManifest(ctx *context.Context, manifest *w3ds.PlatformManifest, lastCommitID, message string) error {
	return commitPlatformManifestForUser(ctx, ctx.Repo.Repository, ctx.Doer, manifest, lastCommitID, message)
}

func commitPlatformManifestForUser(ctx gocontext.Context, repository *repo_model.Repository, user *user_model.User, manifest *w3ds.PlatformManifest, lastCommitID, message string) error {
	if err := manifest.Validate(!setting.IsProd); err != nil {
		return err
	}
	content, err := manifest.Marshal()
	if err != nil {
		return err
	}
	_, err = files_service.ChangeRepoFiles(ctx, repository, user, &files_service.ChangeRepoFilesOptions{
		LastCommitID: lastCommitID,
		OldBranch:    repository.DefaultBranch,
		NewBranch:    repository.DefaultBranch,
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
	canAdmin := ctx.Repo.IsAdmin() && !ctx.Repo.Repository.IsArchived
	walletEName, walletErr := w3dsENameForUser(ctx, ctx.Doer)
	if walletErr != nil {
		log.Warn("Resolve W3DS wallet identity for user on repository %d: %v", ctx.Repo.Repository.ID, walletErr)
	}
	ctx.Data["CanAdminW3DS"] = canAdmin
	ctx.Data["PPAWalletEName"] = walletEName
	ctx.Data["PPAWalletReady"] = canAdmin && walletEName != ""
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
	if pending && submissionSupersedesDecision(manifest, decision) {
		decision = nil
	}
	domainsReady := len(manifest.Domains) > 0
	publication := newW3DSPublicationView(ctx, status, eName, release, releaseSynced, manifest.URL, domainsReady, manifest.IsDraft, pending, decision)
	proofs := currentPPASubmissionProofs(manifest)
	if ctx.Repo.GitRepo != nil {
		gitProofs, historyErr := loadPPASubmissionHistory(ctx.Repo.GitRepo, ctx.Repo.CommitID)
		if historyErr != nil {
			log.Warn("Load PPA submission history for repository %d: %v", ctx.Repo.Repository.ID, historyErr)
		} else {
			proofs = appendUniquePPASubmissionProofs(gitProofs, proofs...)
		}
	}
	publication.PPAHistory = ppaConversationHistory(ctx, status, version, proofs)
	ctx.Data["PlatformManifest"] = manifest
	ctx.Data["PlatformRelease"] = release
	ctx.Data["PlatformPublication"] = publication
	ctx.Data["PlatformEName"] = eName
	ctx.Data["PlatformIdentityReady"] = eName != ""
	ctx.Data["PPARequirementsReady"] = eName != "" && strings.TrimSpace(manifest.URL) != "" && domainsReady && releaseSynced
	ctx.Data["CanApplyPPA"] = canAdmin && walletEName != "" && (publication.PPAStatus == "ready" || publication.PPAStatus == "denied")
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
	if pending && submissionSupersedesDecision(manifest, decision) {
		decision = nil
	}
	publication := newW3DSPublicationView(ctx, status, eName, release, releaseSynced, manifest.URL, len(manifest.Domains) > 0, manifest.IsDraft, pending, decision)
	proofs := currentPPASubmissionProofs(manifest)
	if ctx.Repo.GitRepo != nil {
		gitProofs, historyErr := loadPPASubmissionHistory(ctx.Repo.GitRepo, ctx.Repo.CommitID)
		if historyErr != nil {
			log.Warn("Load PPA submission history for repository %d: %v", ctx.Repo.Repository.ID, historyErr)
		} else {
			proofs = appendUniquePPASubmissionProofs(gitProofs, proofs...)
		}
	}
	publication.PPAHistory = ppaConversationHistory(ctx, status, version, proofs)
	ctx.JSON(http.StatusOK, publication)
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
	case "awaiting_deployment":
		view.Tone = "info"
		view.Title = ctx.Locale.TrString("platform.status.awaiting_deployment")
		view.Message = ctx.Locale.TrString("platform.status.awaiting_deployment_help")
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
		if status.Status == "awaiting_deployment" {
			view.Identity.Label = ctx.Locale.TrString("platform.repo.waiting")
			view.Identity.Message = ctx.Locale.TrString("platform.repo.step_identity_deployment")
		} else {
			view.Identity.Label = ctx.Locale.TrString("platform.repo.automatic")
			view.Identity.Message = ctx.Locale.TrString("platform.repo.step_identity_pending")
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
		view.PPADecidedAt = decision.CreatedAt
		view.PPALabel = ctx.Locale.TrString("platform.ppa.granted")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.granted")
		if strings.TrimSpace(decision.Statement) != "" {
			view.PPAMessage = ctx.Locale.TrString("platform.ppa.granted_reason_help", decision.Level, version, decision.Statement)
		} else {
			view.PPAMessage = ctx.Locale.TrString("platform.ppa.granted_help", decision.Level, version)
		}
	case view.Release.Ready && decision != nil && decision.Decision == "denied":
		view.PPAStatus = "denied"
		view.PPADecidedAt = decision.CreatedAt
		view.PPALabel = ctx.Locale.TrString("platform.ppa.denied")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.reapply")
		view.PPAActionMessage = ctx.Locale.TrString("platform.ppa.reapply_help", version)
		if strings.TrimSpace(decision.Statement) != "" {
			view.PPAMessage = ctx.Locale.TrString("platform.ppa.denied_reason_help", version, decision.Statement)
		} else {
			view.PPAMessage = ctx.Locale.TrString("platform.ppa.denied_help", version)
		}
	case inSubmission:
		view.PPAStatus = "submitted"
		view.PPALabel = ctx.Locale.TrString("platform.ppa.submitted")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.submitted")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.submitted_version_help", version)
		view.PPAActionMessage = view.PPAMessage
	case view.Identity.Ready && view.Application.Ready && view.Domains.Ready && view.Release.Ready:
		view.PPAStatus = "ready"
		view.PPALabel = ctx.Locale.TrString("platform.ppa.ready_to_apply")
		view.PPAButton = ctx.Locale.TrString("platform.ppa.apply")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.apply_version_help", version)
		view.PPAActionMessage = view.PPAMessage
	default:
		view.PPAStatus = "incomplete"
		view.PPAButton = ctx.Locale.TrString("platform.ppa.apply")
		view.PPAMessage = ctx.Locale.TrString("platform.ppa.requirements_help")
		view.PPAActionMessage = view.PPAMessage
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
	if status == nil {
		return nil
	}
	for i := len(status.Decisions) - 1; i >= 0; i-- {
		if status.Decisions[i].PlatformVersion == version {
			return &status.Decisions[i]
		}
	}
	if status.Decision != nil && status.Decision.PlatformVersion == version {
		return status.Decision
	}
	return nil
}

func currentPPASubmissionProofs(manifest *w3ds.PlatformManifest) []w3ds.PPASubmissionProof {
	if manifest == nil {
		return nil
	}
	history := appendUniquePPASubmissionProofs(nil, manifest.SubmissionHistory...)
	if manifest.SubmissionProof != nil {
		history = appendUniquePPASubmissionProofs(history, *manifest.SubmissionProof)
	}
	return history
}

func appendUniquePPASubmissionProofs(history []w3ds.PPASubmissionProof, proofs ...w3ds.PPASubmissionProof) []w3ds.PPASubmissionProof {
	seen := make(map[string]struct{}, len(history)+len(proofs))
	for i := range history {
		seen[history[i].Payload] = struct{}{}
	}
	for _, proof := range proofs {
		if proof.Payload == "" {
			continue
		}
		if _, exists := seen[proof.Payload]; exists {
			continue
		}
		seen[proof.Payload] = struct{}{}
		history = append(history, proof)
	}
	return history
}

func loadPPASubmissionHistory(gitRepository *git.Repository, revision string) ([]w3ds.PPASubmissionProof, error) {
	commits, err := gitRepository.CommitsByFileAndRange(git.CommitsByFileAndRangeOptions{
		Revision: revision,
		File:     w3ds.PlatformManifestPath,
		Page:     1,
		PageSize: 100,
	})
	if err != nil {
		return nil, err
	}
	history := make([]w3ds.PPASubmissionProof, 0)
	for i := len(commits) - 1; i >= 0; i-- {
		content, err := commits[i].GetFileContent(w3ds.PlatformManifestPath, platformManifestMaxSize)
		if err != nil {
			continue
		}
		var historical w3ds.PlatformManifest
		if err := json.Unmarshal([]byte(content), &historical); err != nil {
			continue
		}
		if err := historical.Validate(!setting.IsProd); err != nil {
			continue
		}
		history = appendUniquePPASubmissionProofs(history, historical.SubmissionHistory...)
		if historical.SubmissionProof != nil {
			history = appendUniquePPASubmissionProofs(history, *historical.SubmissionProof)
		}
	}
	return history, nil
}

func loadPPASubmissionHistoryForRepository(ctx gocontext.Context, repository *repo_model.Repository, revision string) ([]w3ds.PPASubmissionProof, error) {
	gitRepository, err := gitrepo.OpenRepository(ctx, repository)
	if err != nil {
		return nil, err
	}
	defer gitRepository.Close()
	return loadPPASubmissionHistory(gitRepository, revision)
}

func ppaConversationHistory(ctx *context.Context, status *w3ds.PublicationStatus, version string, proofs []w3ds.PPASubmissionProof) []w3dsPPAHistoryEvent {
	decisions := make([]w3ds.AccreditationDecision, 0)
	if status != nil {
		for _, decision := range status.Decisions {
			if decision.PlatformVersion == version {
				decisions = append(decisions, decision)
			}
		}
		if len(decisions) == 0 && status.Decision != nil && status.Decision.PlatformVersion == version {
			decisions = append(decisions, *status.Decision)
		}
	}
	slices.SortStableFunc(decisions, func(a, b w3ds.AccreditationDecision) int {
		return strings.Compare(a.CreatedAt, b.CreatedAt)
	})

	history := make([]w3dsPPAHistoryEvent, 0, len(decisions)*2+1)
	for _, decision := range decisions {
		if response := strings.TrimSpace(decision.ApplicantResponse); response != "" {
			history = append(history, w3dsPPAHistoryEvent{
				Kind:      "response",
				Tone:      "info",
				Title:     ctx.Locale.TrString("platform.ppa.response_submitted"),
				Message:   response,
				CreatedAt: decision.ApplicantSubmittedAt,
			})
		}
		title := ctx.Locale.TrString("platform.ppa.denied")
		tone := "negative"
		message := strings.TrimSpace(decision.Statement)
		if decision.Decision == "granted" {
			title = ctx.Locale.TrString("platform.ppa.granted")
			tone = "positive"
			if message == "" {
				message = ctx.Locale.TrString("platform.ppa.granted_help", decision.Level, version)
			}
		} else if message == "" {
			message = ctx.Locale.TrString("platform.ppa.denied_help", version)
		}
		history = append(history, w3dsPPAHistoryEvent{
			Kind:      "decision",
			Tone:      tone,
			Title:     title,
			Message:   message,
			Actor:     decision.ReviewedByEName,
			CreatedAt: decision.CreatedAt,
		})
	}

	for i := range proofs {
		proof := &proofs[i]
		if proof.Statement.Version != version {
			continue
		}
		response := strings.TrimSpace(proof.Statement.ResponseToDecision)
		createdAt := proof.Statement.IssuedAt
		if createdAt == "" {
			createdAt = proof.VerifiedAt
		}
		alreadyIncluded := false
		for _, event := range history {
			if event.Kind == "response" && event.Message == response && event.CreatedAt == createdAt {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			event := w3dsPPAHistoryEvent{
				Kind:      "submission",
				Tone:      "info",
				Title:     ctx.Locale.TrString("platform.ppa.application_signed"),
				Message:   ctx.Locale.TrString("platform.ppa.application_signed_help", proof.Statement.SignerEName),
				Actor:     proof.Statement.SignerEName,
				CreatedAt: createdAt,
			}
			if response != "" {
				event.Kind = "response"
				event.Title = ctx.Locale.TrString("platform.ppa.response_submitted")
				event.Message = response
			}
			history = append(history, event)
		}
	}
	slices.SortStableFunc(history, func(a, b w3dsPPAHistoryEvent) int {
		return strings.Compare(a.CreatedAt, b.CreatedAt)
	})
	return history
}

func submissionSupersedesDecision(manifest *w3ds.PlatformManifest, decision *w3ds.AccreditationDecision) bool {
	if manifest == nil || manifest.SubmissionProof == nil || decision == nil {
		return false
	}
	submittedAt, submitErr := time.Parse(time.RFC3339, manifest.SubmissionProof.VerifiedAt)
	decidedAt, decisionErr := time.Parse(time.RFC3339, decision.CreatedAt)
	return submitErr == nil && decisionErr == nil && submittedAt.After(decidedAt)
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
	content, err := ctx.Repo.Commit.GetFileContent(w3ds.PlatformManifestPath, platformManifestMaxSize)
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
	return loadPlatformPublicationStatusForRepository(ctx, ctx.Repo.Repository.ID)
}

func loadPlatformPublicationStatusForRepository(ctx gocontext.Context, repositoryID int64) *w3ds.PublicationStatus {
	if !setting.PlatformManifestSync.Enabled || setting.PlatformManifestSync.URL == "" || setting.PlatformManifestSync.InternalToken == "" {
		return &w3ds.PublicationStatus{Status: "unavailable"}
	}
	client := &http.Client{Timeout: setting.PlatformManifestSync.Timeout}
	status, err := w3ds.FetchPublicationStatus(ctx, client, setting.PlatformManifestSync.URL, setting.PlatformManifestSync.InternalToken, repositoryID)
	if err == nil {
		return status
	}
	if errors.Is(err, w3ds.ErrPublicationStatusNotFound) {
		return &w3ds.PublicationStatus{Status: "identity_pending"}
	}
	log.Warn("Load platform publication status for repository %d: %v", repositoryID, err)
	return &w3ds.PublicationStatus{Status: "unavailable"}
}
