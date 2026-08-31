// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"
	"net/url"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/auth/source/oauth2"
	"forgejo.org/services/context"
)

const w3dsAuthSourceName = "W3DS"

// RejectAlternativeAuthentication rejects routes for every interactive web
// authentication mechanism other than W3DS while the GitW3 production policy
// is enabled. API tokens and Git transport authentication are unaffected.
func RejectAlternativeAuthentication(ctx *context.Context) {
	if setting.W3DSIdentity.OnlyAuthentication {
		ctx.Error(http.StatusForbidden)
	}
}

func w3dsAuthenticationProviderAllowed(provider string) bool {
	return !setting.W3DSIdentity.OnlyAuthentication || provider == w3dsAuthSourceName
}

func redirectToW3DSSignIn(ctx *context.Context) {
	if redirectTo := ctx.FormString("redirect_to"); redirectTo != "" {
		middleware.SetRedirectToCookie(ctx.Resp, redirectTo)
	}
	ctx.Redirect(setting.AppSubURL + "/user/login/w3ds")
}

// SignInW3DS is GitW3's stable, first-class entry point for W3DS authentication.
// The W3DS-to-OIDC bridge remains an implementation detail behind the named auth source.
func SignInW3DS(ctx *context.Context) {
	if ctx.IsSigned {
		RedirectAfterLogin(ctx)
		return
	}
	if redirectTo := ctx.FormString("redirect_to"); redirectTo != "" {
		middleware.SetRedirectToCookie(ctx.Resp, redirectTo)
	}
	if _, err := auth_model.GetActiveOAuth2SourceByName(ctx, w3dsAuthSourceName); err != nil {
		log.Warn("W3DS login requested without an active W3DS authentication source: %v", err)
		if setting.W3DSIdentity.OnlyAuthentication {
			ctx.Error(http.StatusServiceUnavailable, ctx.Locale.TrString("auth.w3ds_login_unavailable"))
		} else {
			ctx.Flash.Error(ctx.Tr("auth.w3ds_login_unavailable"))
			ctx.Redirect(setting.AppSubURL + "/user/login")
		}
		return
	}
	if !oauth2.IsProviderRegistered(w3dsAuthSourceName) {
		log.Warn("W3DS login requested while its authentication provider is unavailable")
		if setting.W3DSIdentity.OnlyAuthentication {
			ctx.Error(http.StatusServiceUnavailable, ctx.Locale.TrString("auth.w3ds_login_unavailable"))
		} else {
			ctx.Flash.Error(ctx.Tr("auth.w3ds_login_unavailable"))
			ctx.Redirect(setting.AppSubURL + "/user/login")
		}
		return
	}

	ctx.Redirect(setting.AppSubURL + "/user/oauth2/" + url.PathEscape(w3dsAuthSourceName))
}
