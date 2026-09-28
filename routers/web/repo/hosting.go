// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
	"forgejo.org/services/context"
	hosting_service "forgejo.org/services/hosting"
)

const (
	deployTabManaged = "managed"
	deployTabSelf    = "self"
)

// deployTab picks the Deploy sub-tab. Simple Mode always shows Managed.
func deployTab(ctx *context.Context) string {
	if !hosting_service.Enabled() {
		return deployTabSelf
	}
	if simple, _ := ctx.Data["SimpleMode"].(bool); simple {
		return deployTabManaged
	}
	if ctx.FormString("tab") == deployTabSelf {
		return deployTabSelf
	}
	return deployTabManaged
}

type managedEnvVar struct {
	Key      string
	Value    string
	IsSecret bool
}

type managedActivity struct {
	*hosting_model.AuditEvent
	Actor string
}

type managedTarget struct {
	*hosting_service.TargetView
	// Subdomain is the label of the target's address under the base domain.
	Subdomain   string
	Deployments []*hosting_model.Deployment
	Env         []managedEnvVar
	DNSType     string
	DNSValue    string
	Pending     *hosting_model.Deployment
	// Failure is the newest deployment when it failed, shown with a fix prompt.
	Failure *hosting_model.Deployment
}

func renderManagedDeploy(ctx *context.Context, manifest *w3ds.PlatformManifest) {
	repo := ctx.Repo.Repository
	canDeploy := ctx.IsSigned && ctx.Repo.CanWrite(unit.TypeCode) && !repo.IsArchived
	canManage := ctx.IsSigned && ctx.Repo.IsAdmin() && !repo.IsArchived
	ctx.Data["CanManagedDeploy"] = canDeploy
	ctx.Data["CanManageHosting"] = canManage
	ctx.Data["HostingRequireW3DS"] = setting.Hosting.RequireW3DS
	ctx.Data["HostingAllowCustomDomains"] = setting.Hosting.Domains.AllowCustom

	releases, err := deploymentReleases(ctx, true)
	if err != nil {
		ctx.ServerError("deploymentReleases", err)
		return
	}
	platformEName := ""
	if manifest != nil {
		platformEName = platformENameForManifest(ctx, manifest)
	}
	if setting.Hosting.RequireW3DS && platformEName != "" && len(releases) > 0 {
		certifications, err := loadDeploymentCertifications(ctx, repo.ID, platformEName, releases)
		if err != nil {
			log.Warn("Load PPA certifications for repository %d: %v", repo.ID, err)
			ctx.Data["ManagedCertificationsUnavailable"] = true
		} else {
			for i := range releases {
				releases[i].PPACertified = certifications[releases[i].Version].Certified
			}
		}
	}
	ctx.Data["ManagedReleases"] = releases
	ctx.Data["PlatformEName"] = platformEName
	ctx.Data["ManagedBaseDomain"] = setting.Hosting.Domains.BaseDomain
	ctx.Data["ManagedAppReady"] = repoHasDeployConfig(ctx)
	ctx.Data["ManagedSuggestedSubdomain"] = suggestSubdomain(ctx)

	views, err := hosting_service.ViewTargets(ctx, repo.ID)
	if err != nil {
		ctx.ServerError("ViewTargets", err)
		return
	}
	targets := make([]*managedTarget, 0, len(views))
	for _, view := range views {
		target := &managedTarget{TargetView: view}
		if pool := hosting_service.PoolDomain(ctx, view.Target); pool != nil {
			target.Subdomain = subdomainOf("https://" + pool.FQDN)
		}
		if target.Deployments, err = hosting_model.ListDeployments(ctx, view.Target.ID, 10); err != nil {
			ctx.ServerError("ListDeployments", err)
			return
		}
		if len(target.Deployments) > 0 {
			switch newest := target.Deployments[0]; newest.Status {
			case hosting_model.StatusBuildFailed, hosting_model.StatusDeployFailed, hosting_model.StatusRolledBack:
				target.Failure = newest
			}
		}
		for _, deployment := range target.Deployments {
			if !deployment.Status.IsFinal() && deployment.Status != hosting_model.StatusLive {
				target.Pending = deployment
				break
			}
		}
		if canManage {
			vars, err := hosting_model.ListEnvVars(ctx, view.Target.ID)
			if err != nil {
				ctx.ServerError("ListEnvVars", err)
				return
			}
			for _, v := range vars {
				item := managedEnvVar{Key: v.Key, IsSecret: v.IsSecret}
				if !v.IsSecret {
					item.Value, _ = v.Value()
				}
				target.Env = append(target.Env, item)
			}
		}
		target.DNSType, target.DNSValue = hosting_service.DNSInstructions(ctx, view.Target)
		targets = append(targets, target)
	}
	ctx.Data["ManagedTargets"] = targets

	if len(targets) == 0 && setting.Hosting.RequireW3DS {
		walletEName, err := w3dsENameForUser(ctx, ctx.Doer)
		if err != nil {
			ctx.ServerError("w3dsENameForUser", err)
			return
		}
		switch {
		case manifest == nil:
			ctx.Data["ManagedBlocked"] = "not_platform"
		case platformEName == "":
			ctx.Data["ManagedBlocked"] = "identity_pending"
		case walletEName == "":
			ctx.Data["ManagedBlocked"] = "wallet"
		}
	}

	events, err := hosting_model.ListAuditEvents(ctx, repo.ID, 20)
	if err != nil {
		ctx.ServerError("ListAuditEvents", err)
		return
	}
	actorIDs := make([]int64, 0, len(events))
	for _, event := range events {
		actorIDs = append(actorIDs, event.ActorID)
	}
	actors, err := user_model.GetPossibleUserByIDs(ctx, actorIDs)
	if err != nil {
		ctx.ServerError("GetPossibleUserByIDs", err)
		return
	}
	names := make(map[int64]string, len(actors))
	for _, actor := range actors {
		names[actor.ID] = actor.Name
	}
	activity := make([]managedActivity, 0, len(events))
	for _, event := range events {
		name := names[event.ActorID]
		if name == "" {
			name = "GitW3"
		}
		activity = append(activity, managedActivity{AuditEvent: event, Actor: name})
	}
	ctx.Data["ManagedActivity"] = activity
	ctx.HTML(http.StatusOK, tplRepoDeploy)
}

func hostingJSONError(ctx *context.Context, err error) {
	var userErr *hosting_service.UserError
	switch {
	case errors.As(err, &userErr):
		deploymentJSONError(ctx, http.StatusBadRequest, userErr.Message)
	case errors.Is(err, hosting_service.ErrDisabled):
		deploymentJSONError(ctx, http.StatusNotFound, err.Error())
	case errors.Is(err, hosting_model.ErrDomainTaken):
		deploymentJSONError(ctx, http.StatusConflict, err.Error())
	default:
		log.Error("Managed hosting for %s: %v", ctx.Repo.Repository.FullName(), err)
		deploymentJSONError(ctx, http.StatusBadGateway, ctx.Locale.TrString("platform.hosting.error_generic"))
	}
}

func managedStatusURL(ctx *context.Context, id int64) string {
	return ctx.Repo.RepoLink + "/deploy/managed/" + strconv.FormatInt(id, 10) + "/status"
}

// HostingDeploy starts a managed deployment of a release.
func HostingDeploy(ctx *context.Context) {
	release, err := repo_model.GetReleaseForRepoByID(ctx, ctx.Repo.Repository.ID, ctx.FormInt64("release_id"))
	if err != nil {
		deploymentJSONError(ctx, http.StatusBadRequest, ctx.Locale.TrString("platform.hosting.release_required"))
		return
	}
	opts := hosting_service.DeployOptions{
		Repo: ctx.Repo.Repository, Release: release, Actor: ctx.Doer,
		TargetName: strings.TrimSpace(ctx.FormString("target")), Trigger: hosting_model.TriggerManual,
		Subdomain: strings.TrimSpace(ctx.FormString("subdomain")),
	}
	if setting.Hosting.RequireW3DS {
		manifest, _, err := loadPlatformManifestForRepository(ctx, ctx.Repo.Repository)
		if err != nil {
			ctx.ServerError("loadPlatformManifestForRepository", err)
			return
		}
		if manifest != nil {
			opts.PlatformEName = platformENameForManifest(ctx, manifest)
			opts.PlatformName = manifest.DisplayName
		}
		if opts.DeployerEName, err = w3dsENameForUser(ctx, ctx.Doer); err != nil {
			ctx.ServerError("w3dsENameForUser", err)
			return
		}
	}
	result, err := hosting_service.Deploy(ctx, opts)
	if err != nil && (result == nil || result.Deployment == nil) {
		hostingJSONError(ctx, err)
		return
	}
	response := map[string]any{
		"id": result.Deployment.ID, "statusUrl": managedStatusURL(ctx, result.Deployment.ID),
	}
	if err != nil {
		response["message"] = err.Error()
	}
	if signing := result.Signing; signing != nil {
		uri, err := deploymentSigningURI(strings.TrimRight(setting.AppURL, "/")+"/w3ds/deploy/callback",
			signing.SigningPayload, signing.Message, signing.DeploymentEName, signing.VersionEName)
		if err != nil {
			ctx.ServerError("deploymentSigningURI", err)
			return
		}
		response["signing"] = map[string]string{
			"uri": uri, "deploymentEName": signing.DeploymentEName, "versionEName": signing.VersionEName,
			"expiresAt": signing.ExpiresAt.Format(time.RFC3339),
		}
	}
	ctx.JSON(http.StatusCreated, response)
}

func managedDeployment(ctx *context.Context) *hosting_model.Deployment {
	deployment, err := hosting_model.GetDeployment(ctx, ctx.ParamsInt64("deployment"))
	if err != nil || deployment.RepoID != ctx.Repo.Repository.ID {
		deploymentJSONError(ctx, http.StatusNotFound, "Deployment not found.")
		return nil
	}
	return deployment
}

func managedTargetFromPath(ctx *context.Context) *hosting_model.Target {
	target, err := hosting_model.GetTargetByRepoAndName(ctx, ctx.Repo.Repository.ID, ctx.Params("target"))
	if err != nil {
		deploymentJSONError(ctx, http.StatusNotFound, "Target not found.")
		return nil
	}
	return target
}

// HostingDeploymentStatus reports progress of a managed deployment.
func HostingDeploymentStatus(ctx *context.Context) {
	deployment := managedDeployment(ctx)
	if deployment == nil {
		return
	}
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	if err != nil {
		deploymentJSONError(ctx, http.StatusNotFound, "Target not found.")
		return
	}
	response := map[string]any{
		"id": deployment.ID, "status": deployment.Status, "tag": deployment.TagName,
		"error": deployment.Error, "warning": deployment.Warning,
		"final": deployment.Status.IsFinal() || deployment.Status == hosting_model.StatusLive,
		"label": ctx.Locale.TrString("platform.hosting.status." + string(deployment.Status)),
	}
	if deployment.Status == hosting_model.StatusLive {
		response["url"] = hosting_service.PublicURL(ctx, target)
	}
	if deployment.Status == hosting_model.StatusAwaitingSignature {
		if signing := hosting_service.SigningRequestFor(ctx, target); signing != nil {
			uri, err := deploymentSigningURI(strings.TrimRight(setting.AppURL, "/")+"/w3ds/deploy/callback",
				signing.SigningPayload, signing.Message, signing.DeploymentEName, signing.VersionEName)
			if err == nil {
				response["signing"] = map[string]string{"uri": uri, "deploymentEName": signing.DeploymentEName, "versionEName": signing.VersionEName}
			}
		}
	}
	ctx.JSON(http.StatusOK, response)
}

// HostingBuildLog shows the builder output of a deployment.
func HostingBuildLog(ctx *context.Context) {
	deployment := managedDeployment(ctx)
	if deployment == nil {
		return
	}
	output, err := hosting_service.BuildLog(ctx, deployment)
	if err != nil {
		log.Warn("Build log of deployment %d: %v", deployment.ID, err)
	}
	if output == "" {
		output = ctx.Locale.TrString("platform.hosting.log_empty")
	}
	ctx.Resp.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	_, _ = ctx.Resp.Write([]byte(output))
}

// HostingCancel cancels a deployment that has not started rolling out.
func HostingCancel(ctx *context.Context) {
	deployment := managedDeployment(ctx)
	if deployment == nil {
		return
	}
	if err := hosting_service.Cancel(ctx, deployment, ctx.Doer); err != nil {
		hostingJSONError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": true})
}

// HostingRollback redeploys an earlier live deployment.
func HostingRollback(ctx *context.Context) {
	previous := managedDeployment(ctx)
	if previous == nil {
		return
	}
	target, err := hosting_model.GetTarget(ctx, previous.TargetID)
	if err != nil {
		deploymentJSONError(ctx, http.StatusNotFound, "Target not found.")
		return
	}
	deployment, err := hosting_service.Rollback(ctx, target, previous, ctx.Doer)
	if err != nil {
		hostingJSONError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, map[string]any{"id": deployment.ID, "statusUrl": managedStatusURL(ctx, deployment.ID)})
}

// HostingSetEnv adds or replaces an environment variable.
func HostingSetEnv(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	key := strings.TrimSpace(ctx.FormString("key"))
	secret := ctx.FormBool("secret")
	if err := hosting_model.SetEnvVar(ctx, target.ID, key, ctx.FormString("value"), secret); err != nil {
		deploymentJSONError(ctx, http.StatusBadRequest, err.Error())
		return
	}
	hosting_model.Audit(ctx, ctx.Doer.ID, target.RepoID, target.ID, 0, hosting_model.AuditEnvSet, map[string]any{"key": key, "secret": secret})
	ctx.JSON(http.StatusOK, map[string]any{"ok": true})
}

// HostingDeleteEnv removes an environment variable.
func HostingDeleteEnv(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	key := strings.TrimSpace(ctx.FormString("key"))
	if err := hosting_model.DeleteEnvVar(ctx, target.ID, key); err != nil {
		hostingJSONError(ctx, err)
		return
	}
	hosting_model.Audit(ctx, ctx.Doer.ID, target.RepoID, target.ID, 0, hosting_model.AuditEnvDeleted, map[string]any{"key": key})
	ctx.JSON(http.StatusOK, map[string]any{"ok": true})
}

// HostingSetAutoDeploy turns deploy-on-release on or off for a target.
func HostingSetAutoDeploy(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	target.AutoDeploy = ctx.FormBool("enabled")
	if err := hosting_model.UpdateTargetCols(ctx, target, "auto_deploy"); err != nil {
		hostingJSONError(ctx, err)
		return
	}
	hosting_model.Audit(ctx, ctx.Doer.ID, target.RepoID, target.ID, 0, hosting_model.AuditTargetUpdated, map[string]any{"autoDeploy": target.AutoDeploy})
	ctx.JSON(http.StatusOK, map[string]any{"ok": true, "enabled": target.AutoDeploy})
}

// HostingAddDomain attaches a custom domain to a target.
func HostingAddDomain(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	domain, err := hosting_service.AddCustomDomain(ctx, target, ctx.FormString("domain"), ctx.Doer.ID)
	if err != nil {
		if errors.Is(err, hosting_model.ErrDomainTaken) {
			hostingJSONError(ctx, err)
			return
		}
		deploymentJSONError(ctx, http.StatusBadRequest, err.Error())
		return
	}
	recordType, value := hosting_service.DNSInstructions(ctx, target)
	ctx.JSON(http.StatusCreated, map[string]any{"id": domain.ID, "domain": domain.FQDN, "recordType": recordType, "recordValue": value})
}

func managedDomain(ctx *context.Context, target *hosting_model.Target) *hosting_model.Domain {
	domain, err := hosting_model.GetDomain(ctx, ctx.ParamsInt64("domain"))
	if err != nil || domain.TargetID != target.ID {
		deploymentJSONError(ctx, http.StatusNotFound, "Domain not found.")
		return nil
	}
	return domain
}

// HostingVerifyDomain checks DNS for a custom domain and routes it.
func HostingVerifyDomain(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	domain := managedDomain(ctx, target)
	if domain == nil {
		return
	}
	if err := hosting_service.VerifyCustomDomain(ctx, target, domain, ctx.Doer.ID); err != nil {
		deploymentJSONError(ctx, http.StatusConflict, err.Error())
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": true, "url": "https://" + domain.FQDN})
}

// HostingRemoveDomain detaches a custom domain.
func HostingRemoveDomain(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	domain := managedDomain(ctx, target)
	if domain == nil {
		return
	}
	if err := hosting_service.RemoveDomain(ctx, target, domain, ctx.Doer.ID); err != nil {
		hostingJSONError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": true})
}

// HostingDeleteTarget stops and removes a target.
func HostingDeleteTarget(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	if err := hosting_service.DeleteTarget(ctx, target, ctx.Doer); err != nil {
		hostingJSONError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": true, "redirect": ctx.Repo.RepoLink + "/deploy"})
}

// HostingCheckSubdomain reports whether an address under the base domain is
// free, for the live check next to the address field.
func HostingCheckSubdomain(ctx *context.Context) {
	var targetID int64
	if name := ctx.FormString("target"); name != "" {
		if target, err := hosting_model.GetTargetByRepoAndName(ctx, ctx.Repo.Repository.ID, name); err == nil {
			targetID = target.ID
		}
	}
	fqdn, err := hosting_service.CheckSubdomain(ctx, ctx.FormString("name"), targetID)
	if err != nil {
		ctx.JSON(http.StatusOK, map[string]any{"available": false, "message": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"available": true, "fqdn": fqdn, "url": "https://" + fqdn})
}

// HostingSetSubdomain moves a target to a new address under the base domain.
func HostingSetSubdomain(ctx *context.Context) {
	target := managedTargetFromPath(ctx)
	if target == nil {
		return
	}
	domain, err := hosting_service.ClaimSubdomain(ctx, target, ctx.FormString("subdomain"), ctx.Doer.ID)
	if err != nil {
		var userErr *hosting_service.UserError
		if errors.Is(err, hosting_service.ErrSubdomainTaken) || errors.Is(err, hosting_module.ErrSubdomainInvalid) ||
			errors.Is(err, hosting_module.ErrSubdomainReserved) || errors.As(err, &userErr) {
			deploymentJSONError(ctx, http.StatusBadRequest, err.Error())
			return
		}
		hostingJSONError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": true, "url": "https://" + domain.FQDN})
}

// repoHasDeployConfig reports whether the default branch has what managed
// hosting needs to build the app.
func repoHasDeployConfig(ctx *context.Context) bool {
	commit := ctx.Repo.Commit
	if commit == nil {
		return false
	}
	for _, path := range []string{"Dockerfile", hosting_module.ConfigPath} {
		if _, err := commit.GetTreeEntryByPath(path); err == nil {
			return true
		}
	}
	return false
}

// suggestSubdomain proposes a free address named after the repository.
func suggestSubdomain(ctx *context.Context) string {
	repo := ctx.Repo.Repository
	for _, candidate := range []string{
		hosting_module.SuggestSubdomain(repo.Name),
		hosting_module.SuggestSubdomain(repo.OwnerName + "-" + repo.Name),
	} {
		if _, err := hosting_service.CheckSubdomain(ctx, candidate, 0); err == nil {
			return candidate
		}
	}
	return hosting_module.RandomName()
}

// subdomainOf returns the label of a URL under the base domain, or "".
func subdomainOf(url string) string {
	host := strings.TrimPrefix(url, "https://")
	label, found := strings.CutSuffix(host, "."+setting.Hosting.Domains.BaseDomain)
	if !found || strings.Contains(label, ".") {
		return ""
	}
	return label
}
