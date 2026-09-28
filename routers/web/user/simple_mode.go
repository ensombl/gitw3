// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"net/http"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/optional"
	"forgejo.org/services/context"
)

const tplSimpleDashboard base.TplName = "user/dashboard/simple"

// ToggleSimpleMode switches the signed-in user between Simple and Advanced mode.
func ToggleSimpleMode(ctx *context.Context) {
	if err := user_model.SetSimpleMode(ctx, ctx.Doer.ID, ctx.FormBool("enabled")); err != nil {
		ctx.ServerError("SetSimpleMode", err)
		return
	}
	ctx.JSONRedirect("")
}

// simpleDashboard lists the user's repositories and nothing else.
func simpleDashboard(ctx *context.Context, ctxUser *user_model.User) {
	repos, _, err := repo_model.SearchRepository(ctx, &repo_model.SearchRepoOptions{
		ListOptions: db.ListOptions{PageSize: 200},
		Actor:       ctx.Doer,
		OwnerID:     ctxUser.ID,
		Private:     true,
		Collaborate: optional.None[bool](),
		Archived:    optional.Some(false),
		OrderBy:     db.SearchOrderByRecentUpdated,
	})
	if err != nil {
		ctx.ServerError("SearchRepository", err)
		return
	}
	ctx.Data["SimpleRepos"] = repos
	ctx.HTML(http.StatusOK, tplSimpleDashboard)
}
