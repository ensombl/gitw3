// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/url"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

const w3dsAuthSourceName = "W3DS"

// SignInW3DS is GitW3's stable, first-class entry point for W3DS authentication.
// The W3DS-to-OIDC bridge remains an implementation detail behind the named auth source.
func SignInW3DS(ctx *context.Context) {
	if _, err := auth_model.GetActiveOAuth2SourceByName(ctx, w3dsAuthSourceName); err != nil {
		log.Warn("W3DS login requested without an active W3DS authentication source: %v", err)
		ctx.Flash.Error(ctx.Tr("auth.w3ds_login_unavailable"))
		ctx.Redirect(setting.AppSubURL + "/user/login")
		return
	}

	ctx.Redirect(setting.AppSubURL + "/user/oauth2/" + url.PathEscape(w3dsAuthSourceName))
}
