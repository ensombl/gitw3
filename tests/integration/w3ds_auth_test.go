// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestW3DSOnlyAuthenticationRoutes(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.W3DSAllowAlternativeAuthenticationForTests, false)()

	response := MakeRequest(t, NewRequest(t, "GET", "/user/login?redirect_to=/platforms"), http.StatusSeeOther)
	assert.Equal(t, "/user/login/w3ds", test.RedirectURL(response))
	assert.NotContains(t, response.Body.String(), `name="user_name"`)
	assert.NotContains(t, response.Body.String(), `name="password"`)

	for _, request := range []*RequestWrapper{
		NewRequestWithValues(t, "POST", "/user/login", map[string]string{}),
		NewRequestWithValues(t, "POST", "/user/sign_up", map[string]string{}),
		NewRequest(t, "GET", "/user/login/openid"),
		NewRequest(t, "GET", "/user/link_account"),
		NewRequest(t, "GET", "/user/forgot_password"),
		NewRequest(t, "GET", "/user/oauth2/GitHub"),
		NewRequest(t, "GET", "/user/oauth2/GitHub/callback"),
	} {
		MakeRequest(t, request, http.StatusForbidden)
	}
}
