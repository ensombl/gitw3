// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"bytes"
	gocontext "context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forgejo.org/models/db"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	w3ds_model "forgejo.org/models/w3ds"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/w3ds"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"

	"github.com/google/uuid"
)

const tplRepoDeploy base.TplName = "repo/deploy"

const deploymentWaitingForW3DSMessage = "Signed and queued. Production W3DS is still rolling out deployment records; GitW3 will finish automatically when they are available."

type deploymentReleaseView struct {
	ID           int64
	Tag          string
	Version      string
	SHA          string
	URL          string
	PPACertified bool
	PPALevel     string
}

type publisherDeployment struct {
	ID                        string    `json:"id"`
	EName                     string    `json:"ename"`
	Status                    string    `json:"status"`
	DeploymentEName           string    `json:"deploymentEName"`
	VersionEName              string    `json:"versionEName"`
	BundlePayload             string    `json:"bundlePayload"`
	DeploymentKeyDocumentID   string    `json:"deploymentKeyDocumentId"`
	SoftwareVersionDocumentID string    `json:"softwareVersionDocumentId"`
	ProfileEnvelopeID         string    `json:"profileEnvelopeId"`
	LastError                 string    `json:"lastError"`
	Attempts                  int       `json:"attempts"`
	UpdatedAt                 time.Time `json:"updatedAt"`
}

type publisherDeploymentCertification struct {
	Certified bool                        `json:"certified"`
	Decision  *w3ds.AccreditationDecision `json:"decision,omitempty"`
}

type publisherDeploymentCertifications struct {
	Certifications map[string]publisherDeploymentCertification `json:"certifications"`
}

// Deploy renders the native per-user deployment workspace.
func Deploy(ctx *context.Context) {
	manifest, err := loadPlatformManifest(ctx)
	if err != nil {
		ctx.ServerError("loadPlatformManifest", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("platform.deploy.title")
	ctx.Data["PageIsDeploy"] = true
	ctx.Data["IsW3DSPlatform"] = manifest != nil
	if manifest == nil {
		ctx.Data["DeployUnavailable"] = true
		ctx.HTML(http.StatusOK, tplRepoDeploy)
		return
	}
	platformEName := platformENameForManifest(ctx, manifest)
	platformActivated := strings.TrimSpace(manifest.PublicKey) != ""
	walletEName, err := w3dsENameForUser(ctx, ctx.Doer)
	if err != nil {
		ctx.ServerError("w3dsENameForUser", err)
		return
	}
	releases, err := deploymentReleases(ctx)
	if err != nil {
		ctx.ServerError("deploymentReleases", err)
		return
	}
	certificationsAvailable := true
	hasCertifiedRelease := false
	if platformActivated && platformEName != "" && len(releases) > 0 {
		certifications, certificationErr := loadDeploymentCertifications(ctx, ctx.Repo.Repository.ID, platformEName, releases)
		if certificationErr != nil {
			certificationsAvailable = false
			log.Warn("Load deployment PPA certifications for repository %d: %v", ctx.Repo.Repository.ID, certificationErr)
		} else {
			for i := range releases {
				certification := certifications[releases[i].Version]
				releases[i].PPACertified = certification.Certified
				if certification.Decision != nil {
					releases[i].PPALevel = certification.Decision.Level
				}
				hasCertifiedRelease = hasCertifiedRelease || certification.Certified
			}
		}
	}
	deployments, err := w3ds_model.ListDeploymentsForUser(ctx, ctx.Repo.Repository.ID, ctx.Doer.ID)
	if err != nil {
		ctx.ServerError("ListDeploymentsForUser", err)
		return
	}
	for _, deployment := range deployments {
		deployment.Status, deployment.Failure = deploymentPublicationPresentation(deployment.Status, deployment.Failure)
	}
	ctx.Data["PlatformManifest"] = manifest
	ctx.Data["PlatformEName"] = platformEName
	ctx.Data["DeploymentWalletEName"] = walletEName
	ctx.Data["DeploymentReleases"] = releases
	ctx.Data["DeploymentPublisherAvailable"] = setting.PlatformManifestSync.Enabled
	ctx.Data["DeploymentCertificationsAvailable"] = certificationsAvailable
	ctx.Data["HasCertifiedDeploymentRelease"] = hasCertifiedRelease
	ctx.Data["Deployments"] = deployments
	ctx.Data["PlatformNeedsDeploymentIdentity"] = !platformActivated
	ctx.Data["CanCreateDeployment"] = walletEName != "" && len(releases) > 0 && setting.PlatformManifestSync.Enabled &&
		platformEName != "" && (!platformActivated || (certificationsAvailable && hasCertifiedRelease))
	ctx.HTML(http.StatusOK, tplRepoDeploy)
}

func deploymentReleases(ctx *context.Context) ([]deploymentReleaseView, error) {
	releases, err := db.Find[repo_model.Release](ctx, repo_model.FindReleasesOptions{
		ListOptions: db.ListOptions{ListAll: true}, RepoID: ctx.Repo.Repository.ID,
		IncludeDrafts: false, IncludeTags: false, IsPreRelease: optional.Some(false),
	})
	if err != nil {
		return nil, err
	}
	result := make([]deploymentReleaseView, 0, len(releases))
	for _, release := range releases {
		version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
		if !valid || release.Sha1 == "" {
			continue
		}
		result = append(result, deploymentReleaseView{
			ID: release.ID, Tag: release.TagName, Version: version, SHA: release.Sha1,
			URL: ctx.Repo.Repository.Link() + "/releases/tag/" + url.PathEscape(release.TagName),
		})
	}
	return result, nil
}

// CreateDeployment reserves deployment identifiers and starts the wallet flow.
func CreateDeployment(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.CreateDeploymentForm)
	if ctx.HasError() {
		deploymentJSONError(ctx, http.StatusBadRequest, "Check the deployment details and try again.")
		return
	}
	manifest, err := loadPlatformManifest(ctx)
	if err != nil || manifest == nil {
		deploymentJSONError(ctx, http.StatusBadRequest, "This repository does not have a W3DS platform manifest.")
		return
	}
	platformEName := platformENameForManifest(ctx, manifest)
	deployerEName, err := w3dsENameForUser(ctx, ctx.Doer)
	if err != nil || deployerEName == "" {
		deploymentJSONError(ctx, http.StatusBadRequest, "Connect your W3DS eID wallet before creating a deployment.")
		return
	}
	release, err := repo_model.GetReleaseForRepoByID(ctx, ctx.Repo.Repository.ID, form.ReleaseID)
	if err != nil || release.IsDraft || release.IsPrerelease || release.IsTag || release.Sha1 == "" {
		deploymentJSONError(ctx, http.StatusBadRequest, "Choose a published stable release.")
		return
	}
	version, valid := w3ds.NormalizeReleaseVersion(release.TagName)
	if !valid {
		deploymentJSONError(ctx, http.StatusBadRequest, "The release tag must be a semantic version such as v1.2.3.")
		return
	}
	platformActivated := strings.TrimSpace(manifest.PublicKey) != ""
	if platformActivated && platformEName != "" {
		certifications, certificationErr := loadDeploymentCertifications(ctx, ctx.Repo.Repository.ID, platformEName, []deploymentReleaseView{{Version: version}})
		if certificationErr != nil {
			deploymentJSONError(ctx, http.StatusServiceUnavailable, ctx.Locale.TrString("platform.deploy.certification_unavailable"))
			return
		}
		if !certifications[version].Certified {
			deploymentJSONError(ctx, http.StatusForbidden, ctx.Locale.TrString("platform.deploy.certification_required", version))
			return
		}
	}
	environment := strings.TrimSpace(form.Environment)
	switch environment {
	case "production", "staging", "development":
	case "custom":
		environment = strings.TrimSpace(form.CustomEnvironment)
	default:
		environment = ""
	}
	if environment == "" || !strings.HasPrefix(form.PublicKey, "z") {
		deploymentJSONError(ctx, http.StatusBadRequest, "An environment and z-prefixed W3DS public key are required.")
		return
	}
	if platformEName == "" {
		deploymentJSONError(ctx, http.StatusConflict, "GitW3 is still reserving the permanent platform eName. Try again in a moment.")
		return
	}
	id := uuid.NewString()
	prepared, err := callDeploymentPublisher(ctx, http.MethodPost, "/api/v1/deployments/prepare", map[string]any{
		"id": id, "repositoryId": ctx.Repo.Repository.ID, "platformEName": platformEName,
		"deploymentName": form.DeploymentName, "environment": environment, "deployerEName": deployerEName,
		"version": version, "releaseTag": release.TagName, "commitSha": release.Sha1, "publicKey": form.PublicKey,
	})
	if err != nil {
		deploymentJSONError(ctx, http.StatusBadGateway, "GitW3 could not reserve the deployment identity: "+err.Error())
		return
	}
	signingPayload, err := w3ds.DeploymentSigningPayload(prepared.BundlePayload)
	if err != nil || prepared.DeploymentEName == "" || prepared.VersionEName == "" {
		deploymentJSONError(ctx, http.StatusBadGateway, "The deployment publisher returned an incomplete identity.")
		return
	}
	expiresAt := time.Now().UTC().Add(ppaSigningLifetime)
	deployment := &w3ds_model.Deployment{
		ID: id, SigningPayload: signingPayload, RepositoryID: ctx.Repo.Repository.ID, UserID: ctx.Doer.ID,
		DeployerEName: deployerEName, Name: form.DeploymentName, Environment: environment,
		ReleaseID: release.ID, Version: version, ReleaseTag: release.TagName, CommitSHA: strings.ToLower(release.Sha1),
		PlatformEName: platformEName, VersionEName: prepared.VersionEName, DeploymentEName: prepared.DeploymentEName,
		PublicKey: form.PublicKey, BundlePayload: prepared.BundlePayload,
		Status: w3ds_model.DeploymentAwaitingSignature, ExpiresUnix: timeutil.TimeStamp(expiresAt.Unix()),
	}
	if err := w3ds_model.CreateDeployment(ctx, deployment); err != nil {
		deploymentJSONError(ctx, http.StatusInternalServerError, "GitW3 could not save the deployment request.")
		return
	}
	callbackURL := strings.TrimRight(setting.AppURL, "/") + "/w3ds/deploy/callback"
	walletURI, err := deploymentSigningURI(
		callbackURL, signingPayload,
		"Deploy "+manifest.DisplayName+" "+version+" as "+form.DeploymentName,
		prepared.DeploymentEName, prepared.VersionEName,
	)
	if err != nil {
		deploymentJSONError(ctx, http.StatusInternalServerError, "GitW3 could not create the wallet signing request.")
		return
	}
	ctx.JSON(http.StatusCreated, map[string]any{
		"id": id, "sessionId": signingPayload, "uri": walletURI, "expiresAt": expiresAt.Format(time.RFC3339),
		"statusUrl":       ctx.Repo.Repository.Link() + "/deploy/" + id + "/status",
		"deploymentEName": prepared.DeploymentEName, "versionEName": prepared.VersionEName,
	})
}

func deploymentSigningURI(callbackURL, sessionID, message, deploymentEName, versionEName string) (string, error) {
	display, err := json.Marshal(map[string]string{
		"message": message, "sessionId": sessionID,
		"deploymentEName": deploymentEName, "versionEName": versionEName,
	})
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("session", sessionID)
	query.Set("data", base64.StdEncoding.EncodeToString(display))
	query.Set("redirect_uri", callbackURL)
	return "w3ds://sign?" + query.Encode(), nil
}

// DeploymentCallback validates the deployer's wallet proof and queues publication.
func DeploymentCallback(ctx *context.Context) {
	setW3DSWalletCORS(ctx)
	if ctx.Req.Method == http.MethodOptions {
		ctx.Status(http.StatusNoContent)
		return
	}
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64*1024)
	var callback w3dsWalletCallback
	if err := json.NewDecoder(ctx.Req.Body).Decode(&callback); err != nil {
		deploymentJSONError(ctx, http.StatusBadRequest, "Invalid wallet response.")
		return
	}
	callback.SessionID = strings.TrimSpace(callback.SessionID)
	callback.Signature = strings.TrimSpace(callback.Signature)
	callback.W3ID = strings.TrimSpace(callback.W3ID)
	if callback.W3ID == "" {
		callback.W3ID = strings.TrimSpace(callback.EName)
	}
	if callback.SessionID == "" || callback.Signature == "" || callback.W3ID == "" || callback.Message != callback.SessionID {
		deploymentJSONError(ctx, http.StatusBadRequest, "The wallet response is incomplete.")
		return
	}
	deployment, err := w3ds_model.GetDeploymentBySigningPayload(ctx, callback.SessionID)
	if err != nil || deployment == nil || deployment.Status != w3ds_model.DeploymentAwaitingSignature {
		deploymentJSONError(ctx, http.StatusConflict, "This deployment request is unknown, expired, or already used.")
		return
	}
	if callback.W3ID != deployment.DeployerEName {
		deploymentJSONError(ctx, http.StatusForbidden, "Use the eID wallet that started this deployment.")
		return
	}
	verification, err := w3ds.VerifyWalletSignature(ctx, &http.Client{Timeout: setting.PlatformManifestSync.SignatureTimeout}, setting.PlatformManifestSync.RegistryURL, callback.W3ID, callback.Signature, callback.SessionID)
	if err != nil || !verification.Valid {
		deploymentJSONError(ctx, http.StatusUnauthorized, "GitW3 could not verify this wallet signature.")
		return
	}
	claimed, err := w3ds_model.ClaimDeploymentSignature(ctx, callback.SessionID)
	if err != nil || !claimed {
		deploymentJSONError(ctx, http.StatusConflict, "This deployment request is expired or already used.")
		return
	}
	if err := validateDeploymentStillCurrent(ctx, deployment); err != nil {
		_ = w3ds_model.UpdateDeploymentPublication(ctx, deployment.ID, w3ds_model.DeploymentFailed, err.Error(), "", "")
		deploymentJSONError(ctx, http.StatusConflict, err.Error())
		return
	}
	if err := w3ds_model.RecordDeploymentSignature(ctx, deployment.ID, callback.Signature, verification.PublicKey, verification.KeyBindingCertificate); err != nil {
		deploymentJSONError(ctx, http.StatusInternalServerError, "Could not save the verified deployment signature.")
		return
	}
	_, err = callDeploymentPublisher(ctx, http.MethodPost, "/api/v1/deployments/"+url.PathEscape(deployment.ID)+"/finalize", map[string]string{
		"signerEName": callback.W3ID, "signature": callback.Signature,
		"keyBindingCertificate": verification.KeyBindingCertificate,
	})
	if err != nil {
		log.Warn("Queue signed deployment %s for publication: %v", deployment.ID, err)
	}
	ctx.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func validateDeploymentStillCurrent(ctx gocontext.Context, deployment *w3ds_model.Deployment) error {
	repository, err := repo_model.GetRepositoryByID(ctx, deployment.RepositoryID)
	if err != nil {
		return err
	}
	user, err := user_model.GetUserByID(ctx, deployment.UserID)
	if err != nil {
		return err
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if err != nil || !permission.CanRead(unit.TypeCode) || repository.IsArchived {
		return errors.New("you can no longer deploy this repository")
	}
	eName, err := w3dsENameForUser(ctx, user)
	if err != nil || eName != deployment.DeployerEName {
		return errors.New("the connected deployment wallet changed while signing")
	}
	release, err := repo_model.GetReleaseForRepoByID(ctx, repository.ID, deployment.ReleaseID)
	if err != nil || release.IsDraft || release.IsPrerelease || release.TagName != deployment.ReleaseTag || strings.ToLower(release.Sha1) != deployment.CommitSHA {
		return errors.New("the selected release changed while signing")
	}
	manifest, _, err := loadPlatformManifestForRepository(ctx, repository)
	if err != nil || manifest == nil || manifest.EName == nil || *manifest.EName != deployment.PlatformEName {
		return errors.New("the platform identity changed while signing")
	}
	return nil
}

// DeploymentStatus follows publisher progress without exposing another user's deployment.
func DeploymentStatus(ctx *context.Context) {
	deployment, err := w3ds_model.GetDeployment(ctx, ctx.Params("deployment"))
	if err != nil || deployment == nil || deployment.RepositoryID != ctx.Repo.Repository.ID || deployment.UserID != ctx.Doer.ID {
		deploymentJSONError(ctx, http.StatusNotFound, "Deployment not found.")
		return
	}
	if deployment.Status == w3ds_model.DeploymentPublishing || deployment.Status == w3ds_model.DeploymentWaitingForW3DS || deployment.Status == w3ds_model.DeploymentFailed {
		published, syncErr := callDeploymentPublisher(ctx, http.MethodGet, "/api/v1/deployments/"+url.PathEscape(deployment.ID), nil)
		if syncErr == nil {
			if published.Status == "awaiting_signature" && deployment.WalletSignature != "" {
				published, syncErr = callDeploymentPublisher(ctx, http.MethodPost, "/api/v1/deployments/"+url.PathEscape(deployment.ID)+"/finalize", map[string]string{
					"signerEName": deployment.DeployerEName, "signature": deployment.WalletSignature,
					"keyBindingCertificate": deployment.KeyBindingCertificate,
				})
			}
		}
		if syncErr == nil {
			status, failure := deploymentPublicationPresentation(published.Status, published.LastError)
			_ = w3ds_model.UpdateDeploymentPublication(ctx, deployment.ID, status, failure, published.DeploymentKeyDocumentID, published.SoftwareVersionDocumentID)
			deployment.Status, deployment.Failure = status, failure
			deployment.DeploymentKeyDocumentID = published.DeploymentKeyDocumentID
			deployment.SoftwareVersionDocumentID = published.SoftwareVersionDocumentID
		} else if deployment.WalletSignature != "" && deployment.Status != w3ds_model.DeploymentWaitingForW3DS {
			_ = w3ds_model.UpdateDeploymentPublication(ctx, deployment.ID, w3ds_model.DeploymentPublishing, "", deployment.DeploymentKeyDocumentID, deployment.SoftwareVersionDocumentID)
			deployment.Status, deployment.Failure = w3ds_model.DeploymentPublishing, ""
		}
	}
	response := map[string]any{
		"id": deployment.ID, "status": deployment.Status, "message": deployment.Failure,
		"deploymentEName": deployment.DeploymentEName, "versionEName": deployment.VersionEName,
		"deploymentKeyDocumentId":   deployment.DeploymentKeyDocumentID,
		"softwareVersionDocumentId": deployment.SoftwareVersionDocumentID,
	}
	if deployment.Status == w3ds_model.DeploymentCompleted {
		response["redirect"] = ctx.Repo.Repository.Link() + "/deploy"
	} else if deployment.Status == w3ds_model.DeploymentWaitingForW3DS {
		response["message"] = deploymentWaitingForW3DSMessage
	} else if deployment.Status == w3ds_model.DeploymentPublishing {
		response["message"] = "Signature verified. W3DS publication is queued and will retry automatically."
	}
	ctx.JSON(http.StatusOK, response)
}

func deploymentPublicationPresentation(status, failure string) (string, string) {
	if status == w3ds_model.DeploymentCompleted {
		return w3ds_model.DeploymentCompleted, ""
	}
	if status == w3ds_model.DeploymentWaitingForW3DS || (status == w3ds_model.DeploymentFailed && deploymentInfrastructurePending(failure)) {
		return w3ds_model.DeploymentWaitingForW3DS, deploymentWaitingForW3DSMessage
	}
	if status == w3ds_model.DeploymentFailed {
		return w3ds_model.DeploymentFailed, failure
	}
	return w3ds_model.DeploymentPublishing, ""
}

func deploymentInfrastructurePending(failure string) bool {
	failure = strings.ToLower(failure)
	return (strings.Contains(failure, "/records/software-versions") && (strings.Contains(failure, "404") || strings.Contains(failure, "not found"))) ||
		(strings.Contains(failure, "bindingdocumenttype") && strings.Contains(failure, "does not exist"))
}

func platformENameForManifest(ctx *context.Context, manifest *w3ds.PlatformManifest) string {
	if manifest != nil && manifest.EName != nil && strings.TrimSpace(*manifest.EName) != "" {
		return strings.TrimSpace(*manifest.EName)
	}
	return strings.TrimSpace(loadPlatformPublicationStatus(ctx).EName)
}

func loadDeploymentCertifications(ctx gocontext.Context, repositoryID int64, platformEName string, releases []deploymentReleaseView) (map[string]publisherDeploymentCertification, error) {
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		if release.Version != "" {
			versions = append(versions, release.Version)
		}
	}
	result := new(publisherDeploymentCertifications)
	err := callPlatformPublisher(ctx, http.MethodPost, "/api/v1/platforms/deployment-certifications", map[string]any{
		"repositoryId":  repositoryID,
		"platformEName": platformEName,
		"versions":      versions,
	}, result)
	if err != nil {
		return nil, err
	}
	if result.Certifications == nil {
		result.Certifications = make(map[string]publisherDeploymentCertification)
	}
	return result.Certifications, nil
}

func callDeploymentPublisher(ctx gocontext.Context, method, path string, input any) (*publisherDeployment, error) {
	result := new(publisherDeployment)
	if err := callPlatformPublisher(ctx, method, path, input, result); err != nil {
		return nil, err
	}
	return result, nil
}

func callPlatformPublisher(ctx gocontext.Context, method, path string, input, output any) error {
	if !setting.PlatformManifestSync.Enabled || setting.PlatformManifestSync.URL == "" || setting.PlatformManifestSync.InternalToken == "" {
		return errors.New("deployment publisher is not configured")
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(setting.PlatformManifestSync.URL, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+setting.PlatformManifestSync.InternalToken)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: setting.PlatformManifestSync.SignatureTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("publisher returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return err
	}
	return nil
}

func deploymentJSONError(ctx *context.Context, status int, message string) {
	ctx.JSON(status, map[string]string{"message": message})
}
