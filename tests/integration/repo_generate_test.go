// Copyright 2019 The Gitea Authors. All rights reserved.
// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/translation"
	"forgejo.org/modules/w3ds"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertPlatformCreateForm(t *testing.T, htmlDoc *HTMLDoc, owner *user_model.User) {
	form := htmlDoc.doc.Find("form#platform-onboarding-form[action='/repo/create/new']")
	assert.Equal(t, 1, form.Length(), "Expected the guided platform creation form")
	assert.Equal(t, 3, form.Find(".platform-wizard-step").Length(), "Expected three wizard steps")
	assert.Equal(t, 2, form.Find(".platform-wizard-choice").Length(), "Expected two accessible wizard choices")
	assert.Equal(t, 1, form.Find("[data-platform-ai-install].blue.message").Length(), "Expected a visible AI install message")
	assert.Equal(t, 2, form.Find("[data-platform-step].tw-hidden").Length(), "Expected only the first wizard panel to be visible")
	assert.Equal(t, 1, form.Find("#platform-step-back.tw-hidden").Length(), "Expected the back button to start hidden")
	assert.Equal(t, 1, form.Find("#platform-create-submit.tw-hidden").Length(), "Expected the submit button to start hidden")
	htmlDoc.AssertDropdownHasSelectedOption(t, "uid", strconv.FormatInt(owner.ID, 10))
	for _, name := range []string{"platform_name", "platform_display_name", "platform_description", "platform_url"} {
		assert.Equal(t, 1, form.Find(fmt.Sprintf("[name='%s']", name)).Length(), "missing %s", name)
	}
	assert.Greater(t, form.Find(".w3ds-domain-option input[name='platform_domains']").Length(), 0, "published domains should be visible choices")
	_, platformURLRequired := form.Find("[name='platform_url']").Attr("required")
	assert.False(t, platformURLRequired, "platform_url should be optional")
}

func useTestDomainOntology(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/domains", request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"schemaId":"test-domain-schema",
			"domains":[
				{"id":"productivity","label":"Productivity","description":"Tools for getting things done."},
				{"id":"social","label":"Social","description":"Social applications."}
			]
		}`))
	}))
	previousURL := setting.PlatformManifestSync.OntologyURL
	previousTimeout := setting.PlatformManifestSync.Timeout
	setting.PlatformManifestSync.OntologyURL = server.URL
	setting.PlatformManifestSync.Timeout = 2 * time.Second
	t.Cleanup(func() {
		setting.PlatformManifestSync.OntologyURL = previousURL
		setting.PlatformManifestSync.Timeout = previousTimeout
		server.Close()
	})
}

func assertRepoCreateForm(t *testing.T, htmlDoc *HTMLDoc, owner *user_model.User, templateID string) {
	_, exists := htmlDoc.doc.Find("form.ui.form[action^='/repo/create']").Attr("action")
	assert.True(t, exists, "Expected the repo creation form")
	locale := translation.NewLocale("en-US")

	// Verify page title
	title := htmlDoc.doc.Find("title").Text()
	assert.Contains(t, title, locale.TrString("new_repo.title"))

	// Verify form header
	header := strings.TrimSpace(htmlDoc.doc.Find(".form[action='/repo/create'] .header").Text())
	assert.Equal(t, locale.TrString("new_repo.title"), header)

	htmlDoc.AssertDropdownHasSelectedOption(t, "uid", strconv.FormatInt(owner.ID, 10))

	// the template menu is loaded client-side, so don't assert the option exists
	assert.Equal(t, templateID, htmlDoc.GetInputValueByName("repo_template"), "Unexpected repo_template selection")

	for _, name := range []string{"issue_labels", "gitignores", "license"} {
		htmlDoc.AssertDropdownHasOptions(t, name)
	}

	if git.SupportHashSha256 {
		htmlDoc.AssertDropdownHasOptions(t, "object_format_name")
	}
}

func testRepoGenerateCommon(t *testing.T, session *TestSession, templateID, templateOwnerName, templateRepoName string, user, generateOwner *user_model.User, generateRepoName string) *RequestWrapper {
	// Step0: check the existence of the generated repo
	req := NewRequestf(t, "GET", "/%s/%s", generateOwner.Name, generateRepoName)
	session.MakeRequest(t, req, http.StatusNotFound)

	// Step1: go to the main page of template repo
	req = NewRequestf(t, "GET", "/%s/%s", templateOwnerName, templateRepoName)
	resp := session.MakeRequest(t, req, http.StatusOK)

	// Step2: click the "Use this template" button
	htmlDoc := NewHTMLParser(t, resp.Body)
	link, exists := htmlDoc.doc.Find("a.ui.button[href^=\"/repo/create\"]").Attr("href")
	assert.True(t, exists, "The template has changed")
	req = NewRequest(t, "GET", link)
	resp = session.MakeRequest(t, req, http.StatusOK)

	// Step3: test and submit form
	htmlDoc = NewHTMLParser(t, resp.Body)
	assertRepoCreateForm(t, htmlDoc, user, templateID)
	req = NewRequestWithValues(t, "POST", link, map[string]string{
		"uid":           fmt.Sprintf("%d", generateOwner.ID),
		"repo_name":     generateRepoName,
		"repo_template": templateID,
		"git_content":   "true",
	})
	return req
}

func testRepoGenerateSuccess(t *testing.T, session *TestSession, templateID, templateOwnerName, templateRepoName string, user, generateOwner *user_model.User, generateRepoName string) {
	req := testRepoGenerateCommon(t, session, templateID, templateOwnerName, templateRepoName, user, generateOwner, generateRepoName)
	session.MakeRequest(t, req, http.StatusSeeOther)

	// Step4: check the existence of the generated repo
	req = NewRequestf(t, "GET", "/%s/%s", generateOwner.Name, generateRepoName)
	session.MakeRequest(t, req, http.StatusOK)
}

func testRepoGenerateFailure(t *testing.T, session *TestSession, templateID, templateOwnerName, templateRepoName string, user, generateOwner *user_model.User, generateRepoName string) *httptest.ResponseRecorder {
	req := testRepoGenerateCommon(t, session, templateID, templateOwnerName, templateRepoName, user, generateOwner, generateRepoName)
	resp := session.MakeRequest(t, req, http.StatusInternalServerError)
	return resp
}

func testRepoGenerateWithFixture(t *testing.T, session *TestSession, templateID, templateOwnerName, templateRepoName string, user, generateOwner *user_model.User, generateRepoName string) {
	testRepoGenerateSuccess(t, session, templateID, templateOwnerName, templateRepoName, user, generateOwner, generateRepoName)

	// check substituted values in Readme
	req := NewRequestf(t, "GET", "/%s/%s/raw/branch/master/README.md", generateOwner.Name, generateRepoName)
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := fmt.Sprintf(`# %s Readme
Owner: %s
Link: /%s/%s
Clone URL: %s%s/%s.git`,
		generateRepoName,
		strings.ToUpper(generateOwner.Name),
		generateOwner.Name,
		generateRepoName,
		setting.AppURL,
		generateOwner.Name,
		generateRepoName)
	assert.Equal(t, body, resp.Body.String())

	// Step6: check substituted values in substituted file path ${REPO_NAME}
	req = NewRequestf(t, "GET", "/%s/%s/raw/branch/master/%s.log", generateOwner.Name, generateRepoName, generateRepoName)
	resp = session.MakeRequest(t, req, http.StatusOK)
	assert.Equal(t, generateRepoName, resp.Body.String())

	// The .gitea/template file should not be present in the generated repo
	req = NewRequestf(t, "GET", "/%s/%s/raw/branch/master/.gitea/template", generateOwner.Name, generateRepoName)
	session.MakeRequest(t, req, http.StatusNotFound)
}

// test form elements before and after POST error response
func TestRepoCreateForm(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	useTestDomainOntology(t)
	userName := "user1"
	session := loginUser(t, userName)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: userName})

	req := NewRequest(t, "GET", "/repo/create/new")
	resp := session.MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	assertPlatformCreateForm(t, htmlDoc, user)

	req = NewRequestWithValues(t, "POST", "/repo/create/new", map[string]string{
		"uid": strconv.FormatInt(user.ID, 10),
	})
	resp = session.MakeRequest(t, req, http.StatusOK)
	htmlDoc = NewHTMLParser(t, resp.Body)
	assertPlatformCreateForm(t, htmlDoc, user)
}

func TestPlatformCreateChoice(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user1")
	resp := session.MakeRequest(t, NewRequest(t, "GET", "/repo/create"), http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "a[href='/repo/create/new']", true)
	htmlDoc.AssertElement(t, "a[href='/repo/create/port']", true)
}

func TestPortExistingApplicationCreatesEmptyDestination(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user1")
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	formResp := session.MakeRequest(t, NewRequest(t, "GET", "/repo/create/port"), http.StatusOK)
	formPage := NewHTMLParser(t, formResp.Body)
	form := formPage.doc.Find("form#platform-port-form[action='/repo/create/port']")
	assert.Equal(t, 1, form.Length())
	formPage.AssertDropdownHasSelectedOption(t, "uid", strconv.FormatInt(user.ID, 10))
	assert.Equal(t, 1, form.Find("input[name='repo_name']").Length())
	assert.Equal(t, 1, form.Find("input[name='default_branch']").Length())
	assert.Equal(t, 0, form.Find("input[name='ename'], input[name='token']").Length())
	assert.Equal(t, 0, formPage.doc.Find("#platform-port-signing-modal").Length())

	repoName := "ported-application"
	create := NewRequestWithValues(t, "POST", "/repo/create/port", map[string]string{
		"uid":                strconv.FormatInt(user.ID, 10),
		"repo_name":          repoName,
		"default_branch":     "main",
		"object_format_name": "sha1",
	})
	createResp := session.MakeRequest(t, create, http.StatusSeeOther)
	redirect := "/" + user.Name + "/" + repoName + "/onboarding/port"
	assert.Equal(t, redirect, test.RedirectURL(createResp))

	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerID: user.ID, Name: repoName})
	assert.True(t, repository.IsEmpty)
	assert.Equal(t, "main", repository.DefaultBranch)

	handoffResp := session.MakeRequest(t, NewRequest(t, "GET", redirect), http.StatusOK)
	handoff := NewHTMLParser(t, handoffResp.Body)
	handoff.AssertElement(t, "#port-application-handoff", true)
	handoff.AssertElement(t, "button[data-clipboard-target='#port-agent-prompt']", true)
	handoff.AssertElement(t, "#repo-clone-https", true)
	handoff.AssertElement(t, ".port-handoff-stepper li.active:nth-child(1)", true)
	handoff.AssertElement(t, ".port-handoff-stepper li.locked:nth-child(2)", true)
	handoff.AssertElement(t, "form#platform-identity-migration-form", false)
	handoff.AssertElement(t, "#platform-port-signing-modal", false)
	prompt := handoff.doc.Find("#port-agent-prompt").Text()
	assert.Contains(t, prompt, setting.AppURL+user.Name+"/"+repoName+".git")
	assert.Contains(t, prompt, "GitW3 default branch: main")
	assert.Contains(t, prompt, ".w3ds/platform.json")
	assert.Contains(t, prompt, "ename` set to null")
	assert.Contains(t, prompt, "first available upstream-style name")
	assert.NotContains(t, strings.ToLower(prompt), "legacy-token")
	emptyResp := session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s", user.Name, repoName), http.StatusOK)
	empty := NewHTMLParser(t, emptyResp.Body)
	empty.AssertElement(t, "a[href='"+redirect+"']", true)

	migrateBeforePush := NewRequestWithValues(t, "POST", redirect+"/migrate", map[string]string{
		"ename": "@existing-platform",
		"token": "legacy-token",
	}).SetHeader("Accept", "application/json")
	migrateResp := session.MakeRequest(t, migrateBeforePush, http.StatusConflict)
	assert.Contains(t, migrateResp.Body.String(), "Push the existing application")

	nativeSubmit := NewRequestWithValues(t, "POST", redirect+"/migrate", map[string]string{
		"ename": "@existing-platform",
		"token": "legacy-token",
	})
	nativeResp := session.MakeRequest(t, nativeSubmit, http.StatusSeeOther)
	assert.Equal(t, redirect, test.RedirectURL(nativeResp))
	assert.NotContains(t, nativeResp.Body.String(), "identity_push_required")

	applicationCheckout := t.TempDir()
	doGitInitTestRepository(applicationCheckout, git.Sha1ObjectFormat)(t)
	_, _, err := git.NewCommand(t.Context(), "fetch").AddDynamicArguments(applicationCheckout, "master:main").RunStdString(&git.RunOpts{Dir: repository.RepoPath()})
	require.NoError(t, err)
	repository.IsEmpty = false
	require.NoError(t, repo_model.UpdateRepositoryCols(t.Context(), repository, "is_empty"))

	readyResp := session.MakeRequest(t, NewRequest(t, "GET", redirect), http.StatusOK)
	ready := NewHTMLParser(t, readyResp.Body)
	ready.AssertElement(t, "#port-agent-prompt", false)
	ready.AssertElement(t, ".port-handoff-stepper li.completed:nth-child(1)", true)
	ready.AssertElement(t, ".port-handoff-stepper li.active:nth-child(2)", true)
	identityForm := ready.doc.Find("form#platform-identity-migration-form[action='" + redirect + "/migrate'][data-platform-port]")
	assert.Equal(t, 1, identityForm.Length())
	assert.Equal(t, 1, identityForm.Find("input[name='ename']").Length())
	assert.Equal(t, 1, identityForm.Find("input[name='token'][type='password']").Length())
	ready.AssertElement(t, "#platform-port-signing-modal", true)

}

func TestPlatformCreateCommitsManifest(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	useTestDomainOntology(t)
	session := loginUser(t, "user1")
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})
	repoName := "guided-platform"
	req := NewRequestWithValues(t, "POST", "/repo/create/new", map[string]string{
		"uid":                   strconv.FormatInt(user.ID, 10),
		"repo_name":             repoName,
		"default_branch":        "master",
		"object_format_name":    "sha1",
		"platform_name":         "guided-platform",
		"platform_display_name": "Guided Platform",
		"platform_description":  "A platform created through the guided flow",
		"platform_domains":      "productivity",
	})
	resp := session.MakeRequest(t, req, http.StatusSeeOther)
	redirect := test.RedirectURL(resp)
	assert.Contains(t, redirect, "/"+user.Name+"/"+repoName+"/w3ds/welcome")
	pageResp := session.MakeRequest(t, NewRequest(t, "GET", redirect), http.StatusOK)
	page := NewHTMLParser(t, pageResp.Body)
	page.AssertElement(t, "#w3ds-welcome-page[data-w3ds-welcome-status-url='/"+user.Name+"/"+repoName+"/w3ds/welcome/status']", true)
	page.AssertElement(t, "[data-w3ds-welcome-pending]", true)
	page.AssertElement(t, "[data-w3ds-welcome-identity].tw-hidden", true)
	page.AssertElement(t, "[data-w3ds-welcome-empty].tw-hidden", true)
	welcomeStatusResp := session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds/welcome/status", user.Name, repoName), http.StatusOK)
	var welcomeStatus map[string]any
	require.NoError(t, json.Unmarshal(welcomeStatusResp.Body.Bytes(), &welcomeStatus))
	assert.Equal(t, false, welcomeStatus["ready"])
	assert.Empty(t, welcomeStatus["ename"])
	assert.Empty(t, welcomeStatus["versions"])

	pageResp = session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds", user.Name, repoName), http.StatusOK)
	page = NewHTMLParser(t, pageResp.Body)
	page.AssertElement(t, "#w3ds-platform-page", true)
	page.AssertElement(t, ".overflow-menu-items a.item.active[href='/"+user.Name+"/"+repoName+"/w3ds']", true)
	page.AssertElement(t, "#w3ds-publication-status", true)
	page.AssertElement(t, "#w3ds-platform-page[data-w3ds-status-url='/"+user.Name+"/"+repoName+"/w3ds/status']", true)
	page.AssertElement(t, "button[data-w3ds-ppa-apply][disabled]", true)
	page.AssertElement(t, "form[action='/"+user.Name+"/"+repoName+"/w3ds']", true)
	page.AssertElement(t, "form[action='/"+user.Name+"/"+repoName+"/w3ds/visibility'] button[role='switch'][aria-checked='false']", true)
	assert.Equal(t, "Guided Platform", page.GetInputValueByName("platform_display_name"))
	statusResp := session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds/status", user.Name, repoName), http.StatusOK)
	var status map[string]any
	require.NoError(t, json.Unmarshal(statusResp.Body.Bytes(), &status))
	assert.Equal(t, "unavailable", status["status"])
	assert.Equal(t, true, status["isDraft"])
	assert.Equal(t, false, status["inSubmission"])
	assert.NotEmpty(t, status["title"])
	assert.NotEmpty(t, status["identity"])

	raw := NewRequestf(t, "GET", "/%s/%s/raw/branch/master/%s", user.Name, repoName, w3ds.PlatformManifestPath)
	rawResp := session.MakeRequest(t, raw, http.StatusOK)
	var manifest w3ds.PlatformManifest
	require.NoError(t, json.Unmarshal(rawResp.Body.Bytes(), &manifest))
	assert.Equal(t, "guided-platform", manifest.PlatformName)
	assert.Equal(t, "Guided Platform", manifest.DisplayName)
	assert.Equal(t, []string{"productivity"}, manifest.Domains)
	assert.Empty(t, manifest.Category)
	assert.Empty(t, manifest.URL)
	assert.Empty(t, manifest.PublicKey)
	assert.Nil(t, manifest.EName)
	assert.True(t, manifest.IsDraft)
	assert.False(t, manifest.InSubmission)

	deployResp := session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/deploy", user.Name, repoName), http.StatusOK)
	deployPage := NewHTMLParser(t, deployResp.Body)
	deployPage.AssertElement(t, "form[data-deployment-form][data-platform-needs-identity='true']", true)
	assert.Equal(t, 4, deployPage.doc.Find("[data-deployment-step-indicator]").Length())
	assert.Equal(t, 4, deployPage.doc.Find("[data-deployment-step]").Length())
	deployPage.AssertElement(t, ".deploy-ppauth-card a[href='https://docs.w3ds.metastate.foundation']", true)
}

func TestW3DSEditPlatform(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		useTestDomainOntology(t)
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerID: user.ID, Name: "repo1"})
		manifest := w3ds.NewPlatformManifest(
			"fixture-platform",
			"Fixture Platform",
			"A platform profile used to test inline editing",
			"0.1.0",
			"",
			"",
			[]string{"productivity"},
		)
		manifest.PublicKey = "z0123456789"
		eName := "@fixture-platform.w3id"
		manifest.EName = &eName
		content, err := manifest.Marshal()
		require.NoError(t, err)
		_, err = files_service.ChangeRepoFiles(t.Context(), repo, user, &files_service.ChangeRepoFilesOptions{
			OldBranch: repo.DefaultBranch,
			NewBranch: repo.DefaultBranch,
			Message:   "test: add platform manifest",
			Files: []*files_service.ChangeRepoFile{{
				Operation:     "create",
				TreePath:      w3ds.PlatformManifestPath,
				ContentReader: strings.NewReader(string(content)),
			}},
		})
		require.NoError(t, err)
		createNewRelease(t, loginUser(t, user.Name), "/"+user.Name+"/"+repo.Name, "v0.1.0", "v0.1.0", false, false)

		session := loginUser(t, user.Name)
		pageResp := session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds", user.Name, repo.Name), http.StatusOK)
		page := NewHTMLParser(t, pageResp.Body)
		page.AssertElement(t, "form[action='/"+user.Name+"/"+repo.Name+"/w3ds']", true)
		assert.Equal(t, 2, page.Find(".w3ds-domain-option input[name='platform_domains']").Length())
		assert.Equal(t, "Fixture Platform", page.GetInputValueByName("platform_display_name"))

		update := NewRequestWithURLValues(t, "POST", "/"+user.Name+"/"+repo.Name+"/w3ds", url.Values{
			"platform_display_name": {"Fixture Platform Updated"},
			"platform_description":  {"The profile was edited directly from the W3DS tab"},
			"platform_domains":      {"social", "productivity"},
			"platform_url":          {"https://guided.example"},
			"platform_logo_url":     {"https://guided.example/logo.png"},
			"last_commit_id":        {page.GetInputValueByName("last_commit_id")},
		})
		updateResp := session.MakeRequest(t, update, http.StatusSeeOther)
		assert.Equal(t, "/"+user.Name+"/"+repo.Name+"/w3ds", test.RedirectURL(updateResp))

		raw := NewRequestf(t, "GET", "/%s/%s/raw/branch/%s/%s", user.Name, repo.Name, repo.DefaultBranch, w3ds.PlatformManifestPath)
		rawResp := session.MakeRequest(t, raw, http.StatusOK)
		var updated w3ds.PlatformManifest
		require.NoError(t, json.Unmarshal(rawResp.Body.Bytes(), &updated))
		assert.Equal(t, "fixture-platform", updated.PlatformName)
		assert.Equal(t, "Fixture Platform Updated", updated.DisplayName)
		assert.Equal(t, "The profile was edited directly from the W3DS tab", updated.Description)
		assert.Equal(t, "0.1.0", updated.Version)
		assert.Equal(t, []string{"social", "productivity"}, updated.Domains)
		assert.Empty(t, updated.Category)
		assert.Equal(t, "https://guided.example", updated.URL)
		assert.Equal(t, "https://guided.example/logo.png", updated.LogoURL)
		assert.Equal(t, "z0123456789", updated.PublicKey)
		assert.True(t, updated.IsDraft)

		pageResp = session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds", user.Name, repo.Name), http.StatusOK)
		page = NewHTMLParser(t, pageResp.Body)
		visibility := NewRequestWithValues(t, "POST", "/"+user.Name+"/"+repo.Name+"/w3ds/visibility", map[string]string{
			"last_commit_id": page.GetInputValueByName("last_commit_id"),
		})
		visibilityResp := session.MakeRequest(t, visibility, http.StatusSeeOther)
		assert.Equal(t, "/"+user.Name+"/"+repo.Name+"/w3ds", test.RedirectURL(visibilityResp))

		rawResp = session.MakeRequest(t, raw, http.StatusOK)
		require.NoError(t, json.Unmarshal(rawResp.Body.Bytes(), &updated))
		assert.False(t, updated.IsDraft)
		assert.Equal(t, "Fixture Platform Updated", updated.DisplayName)

		pageResp = session.MakeRequest(t, NewRequestf(t, "GET", "/%s/%s/w3ds", user.Name, repo.Name), http.StatusOK)
		page = NewHTMLParser(t, pageResp.Body)
		page.AssertElement(t, "form[action='/"+user.Name+"/"+repo.Name+"/w3ds/ppa'] button[data-w3ds-ppa-apply]:not([disabled])", true)
		apply := NewRequestWithValues(t, "POST", "/"+user.Name+"/"+repo.Name+"/w3ds/ppa", map[string]string{
			"last_commit_id": page.GetInputValueByName("last_commit_id"),
		})
		applyResp := session.MakeRequest(t, apply, http.StatusSeeOther)
		assert.Equal(t, "/"+user.Name+"/"+repo.Name+"/w3ds", test.RedirectURL(applyResp))

		rawResp = session.MakeRequest(t, raw, http.StatusOK)
		require.NoError(t, json.Unmarshal(rawResp.Body.Bytes(), &updated))
		assert.True(t, updated.InSubmission)
		assert.False(t, updated.IsDraft)
	})
}

func TestRepoCreateFormRepoLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	useTestDomainOntology(t)
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})
	userName := "user2"
	session := loginUser(t, userName)
	locale := translation.NewLocale("en-US")
	cannotCreateTr := locale.Tr("repo.form.cannot_create")

	// Test the case where a user has hit the global max creation limit, but can still create
	// a repo in an organization. Because the limit is greater than 0 we also show an alert
	// to tell the user they have hit the limit.
	t.Run("Limit above zero", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		maxCreationLimit := 1
		creationLimitTr := locale.TrN(maxCreationLimit, "repo.form.reach_limit_of_creation_1", "repo.form.reach_limit_of_creation_n", maxCreationLimit)
		defer test.MockVariableValue(&setting.Repository.MaxCreationLimit, maxCreationLimit)()

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/repo/create/new"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)
		assertPlatformCreateForm(t, htmlDoc, org)

		alert := htmlDoc.doc.Find("div.ui.negative.message").Text()
		assert.Contains(t, alert, creationLimitTr)
	})

	// Test the case where a user has hit the global max creation limit, but can still create
	// a repo in an organization. Because the limit is 0 we DO NOT show the alert.
	t.Run("Limit is zero", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		maxCreationLimit := 0
		defer test.MockVariableValue(&setting.Repository.MaxCreationLimit, maxCreationLimit)()

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/repo/create/new"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)
		assertPlatformCreateForm(t, htmlDoc, org)

		htmlDoc.AssertElement(t, "div.ui.negative.message", false)
	})

	// Test the case where a user has hit the global max creation limit, and also cannot create
	// a repo in any of their orgs. The form isnt shown, and we deisplay an alert telling the user
	// they can't create a repo.
	t.Run("Global limit", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		maxCreationLimit := 0
		defer test.MockVariableValue(&setting.Repository.MaxCreationLimit, maxCreationLimit)()

		session := loginUser(t, "user8")

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/repo/create/new"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)

		alert := htmlDoc.doc.Find("div.ui.negative.message").Text()
		assert.Contains(t, alert, cannotCreateTr)
	})
}

func TestRepoGenerate(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	userName := "user1"
	session := loginUser(t, userName)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: userName})

	testRepoGenerateWithFixture(t, session, "44", "user27", "template1", user, user, "generated1")
}

func TestRepoGenerateToOrg(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	userName := "user2"
	session := loginUser(t, userName)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: userName})
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})

	testRepoGenerateWithFixture(t, session, "44", "user27", "template1", user, org, "generated2")
}

func TestRepoCreateFormTrimSpace(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user.Name)

	req := NewRequestWithValues(t, "POST", "/repo/create", map[string]string{
		"uid":       "2",
		"repo_name": " spaced-name ",
	})
	resp := session.MakeRequest(t, req, http.StatusSeeOther)

	assert.Equal(t, "/user2/spaced-name", test.RedirectURL(resp))
	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerID: 2, Name: "spaced-name"})
}

func TestRepoGenerateTemplating(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		input := `# $REPO_NAME
	This is a Repo By $REPO_OWNER
	ThisIsThe${REPO_NAME}InAnInlineWay
	CI token ${CI_DEPLOY_TOKEN} is left untouched`
		expected := `# %s
	This is a Repo By %s
	ThisIsThe%sInAnInlineWay
	CI token ${CI_DEPLOY_TOKEN} is left untouched`

		template := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
			IsTemplate: true,
			Files: forgery.MapFS{
				".forgejo/template":                             forgery.MapFile("**/Readme.md"),
				"dira-${REPO_NAME}/dirb-${REPO_NAME}/Readme.md": forgery.MapFile(input),
			},
		})
		user := template.Owner
		session := loginUser(t, user.Name)

		// The repo.TemplateID field is not initialized. Luckily, the ID field holds the expected value
		templateID := strconv.FormatInt(template.ID, 10)
		generatedName := "my_generated"

		testRepoGenerateSuccess(
			t,
			session,
			templateID,
			user.Name,
			template.Name,
			user,
			user,
			generatedName,
		)

		req := NewRequestf(
			t,
			"GET", "/%s/%[2]s/raw/branch/%s/dira-%[2]s/dirb-%[2]s/Readme.md",
			user.Name,
			generatedName,
			template.DefaultBranch,
		)
		resp := session.MakeRequest(t, req, http.StatusOK)
		body := fmt.Sprintf(expected,
			generatedName,
			user.Name,
			generatedName)
		assert.Equal(t, body, resp.Body.String())

		// The .forgejo/template file should not be present in the generated repo
		req = NewRequestf(
			t,
			"GET", "/%s/%s/raw/branch/%s/.forgejo/template",
			user.Name,
			generatedName,
			template.DefaultBranch,
		)
		session.MakeRequest(t, req, http.StatusNotFound)
	})
}

func TestRepoGenerateTemplatingSymlink(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user := forgery.CreateUser(t, &forgery.CreateUserOptions{
			IsAdmin: true, // required to see the detailed error message on the error response
		})
		session := loginUser(t, user.Name)

		testCases := []struct {
			name          string
			symlinkTarget string
			expectedError string
		}{
			{
				name:          "abs out-of-tree symlink",
				symlinkTarget: "/etc/passwd",
				expectedError: "openat problem/Readme.md: path escapes from parent",
			},
			{
				name:          "rel out-of-tree symlink",
				symlinkTarget: "../../../../../../../../../../../../../../etc/passwd",
				expectedError: "openat problem/Readme.md: path escapes from parent",
			},
			{
				name:          "rel in-tree symlink",
				symlinkTarget: "../actual-contents.txt",
			},
		}

		for i, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				template := forgery.CreateRepository(t, user, &forgery.CreateRepositoryOptions{
					IsTemplate: true,
					Files: forgery.MapFS{
						".forgejo/template":   forgery.MapFile("**/Readme.md"),
						"actual-contents.txt": forgery.MapFile("Here are some contents. $REPO_NAME"),
						"problem/Readme.md":   forgery.MapSymlink(tc.symlinkTarget),
					},
				})

				// The repo.TemplateID field is not initialized. Luckily, the ID field holds the expected value
				templateID := strconv.FormatInt(template.ID, 10)
				generatedName := fmt.Sprintf("my_generated-%d", i)

				if tc.expectedError != "" {
					resp := testRepoGenerateFailure(
						t,
						session,
						templateID,
						user.Name,
						template.Name,
						user,
						user,
						generatedName,
					)
					assert.Contains(t, resp.Body.String(), "openat problem/Readme.md: path escapes from parent")
				} else {
					testRepoGenerateSuccess(
						t,
						session,
						templateID,
						user.Name,
						template.Name,
						user,
						user,
						generatedName,
					)

					// Write gets redirected to the in-repo symlink
					req := NewRequestf(
						t,
						"GET", "/%s/%[2]s/raw/branch/%s/actual-contents.txt",
						user.Name,
						generatedName,
						template.DefaultBranch,
					)
					resp := session.MakeRequest(t, req, http.StatusOK)
					assert.Equal(t, fmt.Sprintf("Here are some contents. %s", generatedName), resp.Body.String())

					// Symlink file still exists and contents are a symlink; no API available to verify it has correct symlink mode though
					req = NewRequestf(
						t,
						"GET", "/%s/%[2]s/raw/branch/%s/problem/Readme.md",
						user.Name,
						generatedName,
						template.DefaultBranch,
					)
					resp = session.MakeRequest(t, req, http.StatusOK)
					assert.Equal(t, tc.symlinkTarget, resp.Body.String())
				}
			})
		}
	})
}

func TestRepoGenerateTemplatingSymlinkGlobFile(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user := forgery.CreateUser(t, &forgery.CreateUserOptions{
			IsAdmin: true, // required to see the detailed error message on the error response
		})
		session := loginUser(t, user.Name)

		template := forgery.CreateRepository(t, user, &forgery.CreateRepositoryOptions{
			IsTemplate: true,
			Files: forgery.MapFS{
				".forgejo/template": forgery.MapSymlink("/etc/passwd"),
			},
		})

		// The repo.TemplateID field is not initialized. Luckily, the ID field holds the expected value
		templateID := strconv.FormatInt(template.ID, 10)
		generatedName := "my_generated"

		resp := testRepoGenerateFailure(
			t,
			session,
			templateID,
			user.Name,
			template.Name,
			user,
			user,
			generatedName,
		)
		assert.Contains(t, resp.Body.String(), "statat .forgejo/template: path escapes from parent")
	})
}
