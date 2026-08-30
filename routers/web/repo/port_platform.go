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

	"forgejo.org/models/organization"
	quota_model "forgejo.org/models/quota"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	w3ds_model "forgejo.org/models/w3ds"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/w3ds"
	"forgejo.org/services/context"
	repo_service "forgejo.org/services/repository"
)

const platformMigrationSigningLifetime = 15 * time.Minute

type migrationInspection struct {
	EName             string          `json:"ename"`
	ProfileEnvelopeID string          `json:"profileEnvelopeId"`
	ProfileDigest     string          `json:"profileDigest"`
	TokenFingerprint  string          `json:"tokenFingerprint"`
	Profile           json.RawMessage `json:"profile"`
	AuthorENames      []string        `json:"authorEnames"`
}

type sourcePlatformProfile struct {
	PlatformName      string                    `json:"platformName"`
	DisplayName       string                    `json:"displayName"`
	Description       string                    `json:"description"`
	Version           string                    `json:"version"`
	EName             string                    `json:"ename"`
	URL               string                    `json:"url"`
	LogoURL           string                    `json:"logoUrl"`
	Domains           []string                  `json:"domains"`
	Category          string                    `json:"category"`
	InSubmission      bool                      `json:"inSubmission"`
	SubmissionVersion string                    `json:"submissionVersion"`
	SubmissionProof   *w3ds.PPASubmissionProof  `json:"submissionProof"`
	SubmissionHistory []w3ds.PPASubmissionProof `json:"submissionHistory"`
	IsDraft           bool                      `json:"isDraft"`
}

func preparePortPlatformPage(ctx *context.Context, ownerID int64) *user_model.User {
	ctx.Data["Title"] = ctx.Tr("platform.port.title")
	ctx.Data["private"] = getRepoPrivate(ctx)
	ctx.Data["IsForcedPrivate"] = setting.Repository.ForcePrivate
	ctx.Data["default_branch"] = setting.Repository.DefaultBranch
	ctx.Data["CanCreateRepo"] = ctx.Doer.CanCreateRepo()
	ctx.Data["MaxCreationLimit"] = ctx.Doer.MaxCreationLimit()
	owner := checkContextUser(ctx, ownerID)
	if !ctx.Written() {
		ctx.Data["ContextUser"] = owner
	}
	return owner
}

func PortPlatform(ctx *context.Context) {
	preparePortPlatformPage(ctx, ctx.FormInt64("org"))
	if ctx.Written() {
		return
	}
	ctx.HTML(http.StatusOK, tplPortPlatform)
}

func PortPlatformStart(ctx *context.Context) {
	ownerID := ctx.FormInt64("uid")
	owner := preparePortPlatformPage(ctx, ownerID)
	if ctx.Written() {
		return
	}
	if owner == nil || !ctx.CheckQuota(quota_model.LimitSubjectSizeReposAll, owner.ID, owner.Name) {
		return
	}
	signerEName, err := w3dsENameForUser(ctx, ctx.Doer)
	if err != nil || signerEName == "" {
		migrationJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.port.wallet_required"))
		return
	}
	ename := strings.TrimSpace(ctx.FormString("ename"))
	token := strings.TrimSpace(ctx.FormString("token"))
	repositoryName := strings.TrimSpace(ctx.FormString("repo_name"))
	defaultBranch := strings.TrimSpace(ctx.FormString("default_branch"))
	if ename == "" || token == "" || repositoryName == "" || defaultBranch == "" {
		migrationJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.port.required"))
		return
	}
	if len(repositoryName) > 100 || repo_model.IsUsableRepoName(repositoryName) != nil {
		migrationJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("form.enterred_invalid_repo_name"))
		return
	}
	var inspection migrationInspection
	if err := callPlatformPublisher(ctx, http.MethodPost, "/api/v1/platforms/migrations/inspect", map[string]string{"ename": ename, "token": token}, &inspection); err != nil {
		log.Warn("Inspect platform migration for %s: %v", ename, err)
		migrationJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.port.authorization_failed"))
		return
	}
	if len(inspection.AuthorENames) > 0 && !slices.Contains(inspection.AuthorENames, signerEName) {
		migrationJSONError(ctx, http.StatusForbidden, ctx.Locale.TrString("platform.port.not_author"))
		return
	}
	var source sourcePlatformProfile
	if err := json.Unmarshal(inspection.Profile, &source); err != nil || source.EName != inspection.EName {
		migrationJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.port.profile_invalid"))
		return
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		migrationJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.port.signing_failed"))
		return
	}
	now := time.Now().UTC()
	statement := w3ds.PlatformMigrationStatement{
		Type: w3ds.PlatformMigrationStatementType, SchemaVersion: 1,
		PlatformEName: inspection.EName, ProfileEnvelopeID: inspection.ProfileEnvelopeID, ProfileDigest: inspection.ProfileDigest,
		TargetInstance: strings.TrimRight(setting.AppURL, "/"), TargetOwner: owner.Name, TargetRepository: repositoryName,
		SignerEName: signerEName, IssuedAt: now.Format(time.RFC3339), Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	payload, err := statement.SigningPayload()
	if err != nil {
		migrationJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.port.signing_failed"))
		return
	}
	statementJSON, _ := json.Marshal(statement)
	authorsJSON, _ := json.Marshal(inspection.AuthorENames)
	expiresAt := now.Add(platformMigrationSigningLifetime)
	if err := w3ds_model.CreatePlatformMigrationSession(ctx, &w3ds_model.PlatformMigrationSession{
		ID: payload, UserID: ctx.Doer.ID, OwnerID: owner.ID, RepositoryName: repositoryName,
		DefaultBranch: defaultBranch, IsPrivate: ctx.FormBool("private") || setting.Repository.ForcePrivate,
		EName: inspection.EName, ProfileEnvelopeID: inspection.ProfileEnvelopeID, ProfileDigest: inspection.ProfileDigest,
		TokenFingerprint: inspection.TokenFingerprint, Profile: string(inspection.Profile), AuthorENames: string(authorsJSON),
		Statement: string(statementJSON), Status: w3ds_model.MigrationSigningPending, ExpiresUnix: timeutil.TimeStamp(expiresAt.Unix()),
	}); err != nil {
		log.Error("Create platform migration signing session: %v", err)
		migrationJSONError(ctx, http.StatusInternalServerError, ctx.Locale.TrString("platform.port.signing_failed"))
		return
	}
	callbackURL := strings.TrimRight(setting.AppURL, "/") + "/w3ds/migrations/callback"
	query := url.Values{}
	query.Set("session", payload)
	display, _ := json.Marshal(map[string]string{"message": "Move " + source.DisplayName + " to GitW3", "sessionId": payload})
	query.Set("data", base64.StdEncoding.EncodeToString(display))
	query.Set("redirect_uri", callbackURL)
	ctx.JSON(http.StatusCreated, map[string]any{
		"sessionId": payload, "uri": "w3ds://sign?" + query.Encode(), "expiresAt": expiresAt.Format(time.RFC3339),
		"statusUrl": setting.AppSubURL + "/repo/create/port/" + url.PathEscape(payload),
	})
}

func PlatformMigrationCallback(ctx *context.Context) {
	setW3DSWalletCORS(ctx)
	if ctx.Req.Method == http.MethodOptions {
		ctx.Status(http.StatusNoContent)
		return
	}
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64*1024)
	var callback w3dsWalletCallback
	if err := json.NewDecoder(ctx.Req.Body).Decode(&callback); err != nil {
		migrationJSONError(ctx, http.StatusBadRequest, "Invalid wallet response.")
		return
	}
	callback.SessionID = strings.TrimSpace(callback.SessionID)
	callback.Signature = strings.TrimSpace(callback.Signature)
	callback.W3ID = strings.TrimSpace(callback.W3ID)
	if callback.W3ID == "" {
		callback.W3ID = strings.TrimSpace(callback.EName)
	}
	session, err := w3ds_model.GetPlatformMigrationSession(ctx, callback.SessionID)
	if err != nil || session == nil || session.Status != w3ds_model.MigrationSigningPending || callback.Message != callback.SessionID {
		migrationJSONError(ctx, http.StatusConflict, "This migration signing request is unknown, expired, or already used.")
		return
	}
	var statement w3ds.PlatformMigrationStatement
	if json.Unmarshal([]byte(session.Statement), &statement) != nil || callback.W3ID != statement.SignerEName {
		migrationJSONError(ctx, http.StatusForbidden, "Use the same eID wallet that started this migration.")
		return
	}
	verification, err := w3ds.VerifyWalletSignature(ctx, &http.Client{Timeout: setting.PlatformManifestSync.SignatureTimeout}, setting.PlatformManifestSync.RegistryURL, callback.W3ID, callback.Signature, callback.SessionID)
	if err != nil || !verification.Valid {
		migrationJSONError(ctx, http.StatusUnauthorized, "GitW3 could not verify this wallet signature.")
		return
	}
	claimed, err := w3ds_model.ClaimPlatformMigrationSession(ctx, session.ID)
	if err != nil || !claimed {
		migrationJSONError(ctx, http.StatusConflict, "This signing request is expired or already used.")
		return
	}
	proof := w3ds.PlatformMigrationProof{
		Statement: statement, Payload: callback.SessionID, Signature: callback.Signature,
		PublicKey: verification.PublicKey, KeyBindingCertificate: verification.KeyBindingCertificate, VerifiedAt: time.Now().UTC().Format(time.RFC3339),
	}
	proofJSON, _ := json.Marshal(proof)
	session.Proof = string(proofJSON)
	var authors []string
	_ = json.Unmarshal([]byte(session.AuthorENames), &authors)
	if len(authors) == 0 {
		session.Status = w3ds_model.MigrationSigningReview
		session.ExpiresUnix = timeutil.TimeStamp(time.Now().Add(7 * 24 * time.Hour).Unix())
		_ = w3ds_model.UpdatePlatformMigrationSession(ctx, session, "proof", "status", "expires_unix")
		ctx.JSON(http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	if err := completePlatformMigration(ctx, session); err != nil {
		log.Warn("Complete platform migration: %v", err)
		session.Status = w3ds_model.MigrationSigningRejected
		session.Failure = err.Error()
		_ = w3ds_model.UpdatePlatformMigrationSession(ctx, session, "proof", "status", "failure")
		migrationJSONError(ctx, http.StatusConflict, "GitW3 could not create the staged repository.")
		return
	}
	ctx.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func PlatformMigrationStatus(ctx *context.Context) {
	session, err := w3ds_model.GetPlatformMigrationSession(ctx, ctx.Params("session"))
	if err != nil || session == nil || session.UserID != ctx.Doer.ID {
		migrationJSONError(ctx, http.StatusNotFound, ctx.Locale.TrString("platform.port.signing_unknown"))
		return
	}
	response := map[string]any{"status": session.Status}
	if session.Status == w3ds_model.MigrationSigningCompleted && session.RepositoryID > 0 {
		if repository, err := repo_model.GetRepositoryByID(ctx, session.RepositoryID); err == nil {
			response["redirect"] = repository.Link() + "/w3ds"
		}
	}
	if session.Status == w3ds_model.MigrationSigningReview {
		response["message"] = ctx.Tr("platform.port.awaiting_review")
	}
	if session.Status == w3ds_model.MigrationSigningRejected || session.Status == w3ds_model.MigrationSigningExpired {
		response["message"] = ctx.Tr("platform.port.signing_restart")
	}
	ctx.JSON(http.StatusOK, response)
}

func ActivatePlatformMigration(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil || manifest == nil || manifest.Migration == nil || manifest.Migration.Status != "staged" || manifest.EName == nil {
		ctx.Flash.Error(ctx.Tr("platform.port.activation_not_staged"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	token := strings.TrimSpace(ctx.FormString("token"))
	if token == "" {
		ctx.Flash.Error(ctx.Tr("platform.port.activation_token_required"))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	input := map[string]any{
		"repositoryId": ctx.Repo.Repository.ID, "ename": *manifest.EName,
		"profileEnvelopeId": manifest.Migration.ProfileEnvelopeID, "profileDigest": manifest.Migration.ProfileDigest,
		"token": token,
	}
	var result map[string]any
	if err := callPlatformPublisher(ctx, http.MethodPost, "/api/v1/platforms/migrations/activate", input, &result); err != nil {
		log.Warn("Activate platform migration for repository %d: %v", ctx.Repo.Repository.ID, err)
		ctx.Flash.Error(ctx.Tr("platform.port.activation_failed", err))
		ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
		return
	}
	ctx.Flash.Success(ctx.Tr("platform.port.activation_complete"))
	ctx.Redirect(ctx.Repo.Repository.Link() + "/w3ds")
}

func PlatformMigrationReviews(ctx *context.Context) {
	if !ctx.IsUserSiteAdmin() {
		ctx.Error(http.StatusForbidden)
		return
	}
	sessions, err := w3ds_model.PendingPlatformMigrationReviews(ctx)
	if err != nil {
		ctx.ServerError("PendingPlatformMigrationReviews", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("platform.port.review_title")
	ctx.Data["MigrationReviews"] = sessions
	ctx.HTML(http.StatusOK, "repo/platform_migration_reviews")
}

func ApprovePlatformMigration(ctx *context.Context) {
	if !ctx.IsUserSiteAdmin() {
		ctx.Error(http.StatusForbidden)
		return
	}
	session, err := w3ds_model.GetPlatformMigrationSession(ctx, ctx.Params("session"))
	if err != nil || session == nil || session.Status != w3ds_model.MigrationSigningReview {
		ctx.NotFound("PlatformMigrationSession", err)
		return
	}
	if err := completePlatformMigration(ctx, session); err != nil {
		ctx.ServerError("completePlatformMigration", err)
		return
	}
	ctx.Redirect(setting.AppSubURL + "/repo/create/port/reviews")
}

func completePlatformMigration(ctx gocontext.Context, session *w3ds_model.PlatformMigrationSession) error {
	user, err := user_model.GetUserByID(ctx, session.UserID)
	if err != nil {
		return err
	}
	owner, err := user_model.GetUserByID(ctx, session.OwnerID)
	if err != nil {
		return err
	}
	if owner.ID != user.ID {
		if !owner.IsOrganization() {
			return errors.New("migration repository owner is no longer valid")
		}
		canCreate, err := organization.OrgFromUser(owner).CanCreateOrgRepo(ctx, user.ID)
		if err != nil || (!canCreate && !user.IsAdmin) {
			return errors.New("the applicant can no longer create repositories for this organization")
		}
	}
	var source sourcePlatformProfile
	var proof w3ds.PlatformMigrationProof
	var authors []string
	if err := json.Unmarshal([]byte(session.Profile), &source); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(session.Proof), &proof); err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(session.AuthorENames), &authors)
	ename := session.EName
	manifest := &w3ds.PlatformManifest{
		SchemaVersion: w3ds.PlatformManifestVersion, PlatformName: source.PlatformName, DisplayName: source.DisplayName,
		Description: source.Description, Version: source.Version, EName: &ename, URL: source.URL, LogoURL: source.LogoURL,
		Domains: source.Domains, Category: source.Category, InSubmission: source.InSubmission, SubmissionVersion: source.SubmissionVersion,
		SubmissionProof: source.SubmissionProof, SubmissionHistory: source.SubmissionHistory, IsDraft: source.IsDraft,
		Migration: &w3ds.PlatformMigration{
			Status: "staged", ProfileEnvelopeID: session.ProfileEnvelopeID, ProfileDigest: session.ProfileDigest,
			LegacyTokenFingerprint: session.TokenFingerprint, SourceProfile: json.RawMessage(session.Profile),
			SourceAuthorENames: authors, Proof: &proof,
		},
	}
	if err := manifest.Validate(!setting.IsProd); err != nil {
		return err
	}
	repository, err := repo_service.CreateRepository(ctx, user, owner, repo_service.CreateRepoOptions{
		Name: session.RepositoryName, Description: source.Description, Readme: "Default", IsPrivate: session.IsPrivate,
		DefaultBranch: session.DefaultBranch, AutoInit: true, TrustModel: repo_model.DefaultTrustModel,
		ObjectFormatName: git.Sha1ObjectFormat.Name(), PlatformManifest: manifest,
	})
	if err != nil {
		return err
	}
	session.RepositoryID = repository.ID
	session.Status = w3ds_model.MigrationSigningCompleted
	session.Failure = ""
	return w3ds_model.UpdatePlatformMigrationSession(ctx, session, "repository_id", "status", "failure")
}

func migrationJSONError(ctx *context.Context, status int, message string) {
	ctx.JSON(status, map[string]string{"message": message})
}
