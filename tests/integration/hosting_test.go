// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	hosting_service "forgejo.org/services/hosting"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hostingFakes struct {
	mu         sync.Mutex
	dispatched []hosting_service.BuildInputs
	images     map[string]string
	domains    []string
	registry   map[string]bool
	services   []hosting_service.ServiceStatus
}

func (f *hostingFakes) CreateApp(_ context.Context, spec hosting_service.AppSpec) (string, string, error) {
	return "app-" + spec.AppName, spec.AppName, nil
}
func (f *hostingFakes) UpdateApp(context.Context, string, hosting_service.AppSpec) error { return nil }
func (f *hostingFakes) DeleteApp(context.Context, string) error                          { return nil }
func (f *hostingFakes) SetEnv(context.Context, string, map[string]string) error          { return nil }
func (f *hostingFakes) DeployImage(_ context.Context, appID, ref, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[appID] = ref
	return nil
}

func (f *hostingFakes) AppState(context.Context, string) (*hosting_service.AppState, error) {
	return &hosting_service.AppState{AppName: "svc", Status: "done"}, nil
}

func (f *hostingFakes) CreateCompose(context.Context, hosting_service.ComposeSpec) (string, string, error) {
	return "compose", "stack", nil
}
func (f *hostingFakes) DeleteCompose(context.Context, string) error { return nil }
func (f *hostingFakes) DeployStack(context.Context, string, string, map[string]string, string) error {
	return nil
}

func (f *hostingFakes) ComposeState(context.Context, string) (*hosting_service.AppState, error) {
	return &hosting_service.AppState{Status: "done"}, nil
}

func (f *hostingFakes) AddDomain(_ context.Context, spec hosting_service.DomainSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains = append(f.domains, spec.Host)
	return fmt.Sprintf("domain-%d", len(f.domains)), nil
}
func (f *hostingFakes) RemoveDomain(context.Context, string) error { return nil }

func (f *hostingFakes) Dispatch(_ context.Context, _ *user_model.User, inputs hosting_service.BuildInputs) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, inputs)
	return 0, nil
}
func (f *hostingFakes) Cancel(context.Context, int64) error { return nil }
func (f *hostingFakes) Status(context.Context, int64) (hosting_service.BuildState, error) {
	return hosting_service.BuildStateRunning, nil
}
func (f *hostingFakes) RunLink(context.Context, int64) string { return "" }

func (f *hostingFakes) Exists(_ context.Context, image, digest string) (bool, error) {
	return f.registry[image+"@"+digest], nil
}

func (f *hostingFakes) CreateRecord(context.Context, string) (string, error) { return "", nil }
func (f *hostingFakes) DeleteRecord(context.Context, string) error           { return nil }

func (f *hostingFakes) Services(context.Context, string, string) ([]hosting_service.ServiceStatus, error) {
	return f.services, nil
}

func (f *hostingFakes) Diagnose(context.Context, string, string) (string, error) { return "", nil }

func (f *hostingFakes) Logs(context.Context, string, string, int) (string, error) {
	return "listening on 0.0.0.0:3000", nil
}

func (f *hostingFakes) Call(context.Context, string, string, any, any) error { return nil }

func setupHosting(t *testing.T) *hostingFakes {
	t.Helper()
	fakes := &hostingFakes{images: map[string]string{}, registry: map[string]bool{}}
	for _, restore := range []func(){
		test.MockVariableValue(&setting.Hosting.Enabled, true),
		test.MockVariableValue(&setting.Hosting.RequireW3DS, false),
		test.MockVariableValue(&setting.Hosting.CallbackSecret, "integration-secret"),
		test.MockVariableValue(&setting.Hosting.RegistryHost, "registry.test"),
		test.MockVariableValue(&setting.Hosting.RegistryOwner, "deployments"),
		test.MockVariableValue(&setting.Hosting.Domains.BaseDomain, "apps.test"),
		test.MockVariableValue(&setting.Hosting.Domains.AllowCustom, true),
		test.MockVariableValue(&setting.Hosting.Scan.Policy, setting.HostingScanPolicyBlock),
		test.MockVariableValue(&setting.Hosting.Scan.Severity, "CRITICAL"),
	} {
		t.Cleanup(restore)
	}
	hosting_service.SetClients(hosting_service.Clients{
		Dokploy: fakes, Builder: fakes, Registry: fakes, DNS: fakes, Swarm: fakes, Publisher: fakes,
	})
	t.Cleanup(func() { hosting_service.SetClients(hosting_service.Clients{}) })
	return fakes
}

func TestHostingManagedDeployFlow(t *testing.T) {
	onApplicationRun(t, testHostingManagedDeployFlow)
}

func testHostingManagedDeployFlow(t *testing.T, _ *url.URL) {
	fakes := setupHosting(t)
	ctx := t.Context()
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo, _, cleanup := tests.CreateDeclarativeRepo(t, owner, "hosted-app", nil, nil, []*files_service.ChangeRepoFile{{
		Operation: "create", TreePath: "Dockerfile",
		ContentReader: strings.NewReader("FROM scratch\nEXPOSE 8080\n"),
	}})
	defer cleanup()

	session := loginUser(t, owner.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)
	release := createNewReleaseUsingAPI(t, token, owner, repo, "v1.0.0", repo.DefaultBranch, "v1.0.0", "")

	page := session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/deploy"), http.StatusOK)
	doc := NewHTMLParser(t, page.Body)
	assert.Equal(t, 1, doc.Find("[data-managed-deploy-form]").Length())
	assert.Equal(t, 1, doc.Find(".deploy-mode-tabs").Length())
	session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/deploy?tab=self"), http.StatusOK)

	check := session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/deploy/managed/subdomain?name=Hosted-App-Live"), http.StatusOK)
	var availability struct {
		Available bool   `json:"available"`
		URL       string `json:"url"`
	}
	DecodeJSON(t, check, &availability)
	assert.True(t, availability.Available)
	assert.Equal(t, "https://hosted-app-live.apps.test", availability.URL)

	response := session.MakeRequest(t, NewRequestWithValues(t, "POST", repo.Link()+"/deploy/managed", map[string]string{
		"release_id": fmt.Sprint(release.ID),
		"subdomain":  "hosted-app-live",
	}), http.StatusCreated)
	var started struct {
		ID        int64  `json:"id"`
		StatusURL string `json:"statusUrl"`
	}
	DecodeJSON(t, response, &started)
	require.Len(t, fakes.dispatched, 1)
	inputs := fakes.dispatched[0]
	specJSON, err := base64.StdEncoding.DecodeString(inputs.Spec)
	require.NoError(t, err)
	var spec hosting_service.BuildSpec
	require.NoError(t, json.Unmarshal(specJSON, &spec))
	require.Len(t, spec.Images, 1)
	assert.Equal(t, "deployments/user2-hosted-app-web", spec.Images[0].Repository)
	assert.Equal(t, "registry.test", spec.Registry)

	// The builder fetches the source with its signed URL; tampering is refused.
	source, err := url.Parse(inputs.SourceURL)
	require.NoError(t, err)
	archive := MakeRequest(t, NewRequest(t, "GET", source.RequestURI()), http.StatusOK)
	assert.Equal(t, []byte{0x1f, 0x8b}, archive.Body.Bytes()[:2], "gzip archive")
	MakeRequest(t, NewRequest(t, "GET", strings.Replace(source.RequestURI(), "sig=", "sig=0", 1)), http.StatusForbidden)

	digest := "sha256:" + strings.Repeat("d", 64)
	body, _ := json.Marshal(hosting_service.BuildCallback{
		JobID: spec.JobID, Nonce: spec.Nonce, Status: "success", Digests: map[string]string{"": digest},
	})
	forged := NewRequestWithBody(t, "POST", "/-/hosting/callback", strings.NewReader(string(body)))
	forged.Header.Set(hosting_module.CallbackSignatureHeader, "sha256="+strings.Repeat("0", 64))
	MakeRequest(t, forged, http.StatusUnauthorized)

	fakes.registry["user2-hosted-app-web@"+digest] = true
	callback := NewRequestWithBody(t, "POST", "/-/hosting/callback", strings.NewReader(string(body)))
	callback.Header.Set(hosting_module.CallbackSignatureHeader, "sha256="+hosting_module.SignCallback("integration-secret", body))
	MakeRequest(t, callback, http.StatusOK)
	replay := NewRequestWithBody(t, "POST", "/-/hosting/callback", strings.NewReader(string(body)))
	replay.Header.Set(hosting_module.CallbackSignatureHeader, "sha256="+hosting_module.SignCallback("integration-secret", body))
	MakeRequest(t, replay, http.StatusConflict)

	deployment, err := hosting_model.GetDeployment(ctx, started.ID)
	require.NoError(t, err)
	assert.Equal(t, hosting_model.StatusDeploying, deployment.Status)
	target, err := hosting_model.GetTarget(ctx, deployment.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "registry.test/deployments/user2-hosted-app-web@"+digest, fakes.images[target.DokployAppID])

	fakes.services = []hosting_service.ServiceStatus{{Name: "svc", Image: "x@" + digest, UpdateState: "completed", Desired: 1, Running: 1}}
	require.NoError(t, hosting_service.SyncDeployments(ctx))
	status := session.MakeRequest(t, NewRequest(t, "GET", started.StatusURL), http.StatusOK)
	var live struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	DecodeJSON(t, status, &live)
	assert.Equal(t, "live", live.Status)
	assert.Equal(t, "https://hosted-app-live.apps.test", live.URL, "the chosen address is used")
	assert.Contains(t, fakes.domains, "hosted-app-live.apps.test")

	page = session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/deploy"), http.StatusOK)
	assert.Contains(t, page.Body.String(), live.URL)
	logsURL := repo.Link() + "/deploy/managed/targets/" + target.Name + "/logs"
	assert.Positive(t, NewHTMLParser(t, page.Body).Find("[data-managed-log='"+logsURL+"']").Length())
	logs := session.MakeRequest(t, NewRequest(t, "GET", logsURL), http.StatusOK)
	assert.Equal(t, "listening on 0.0.0.0:3000", logs.Body.String())

	// Readers can watch but not deploy, and app output (which can hold user
	// data) is for deployers only.
	reader := loginUser(t, "user4")
	reader.MakeRequest(t, NewRequestWithValues(t, "POST", repo.Link()+"/deploy/managed", map[string]string{
		"release_id": fmt.Sprint(release.ID),
	}), http.StatusNotFound)
	reader.MakeRequest(t, NewRequest(t, "GET", logsURL), http.StatusNotFound)
}

func TestSimpleMode(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.UI.DefaultSimpleMode, true)()
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	session := loginUser(t, "user2")

	dashboard := session.MakeRequest(t, NewRequest(t, "GET", "/"), http.StatusOK)
	doc := NewHTMLParser(t, dashboard.Body)
	assert.Positive(t, doc.Find(".simple-repo-list a[href='/user2/repo1']").Length())
	assert.Zero(t, doc.Find("#dashboard-repo-list").Length())
	assert.Zero(t, doc.Find("#navbar a[href='/issues']").Length())

	home := session.MakeRequest(t, NewRequest(t, "GET", repo.Link()), http.StatusSeeOther)
	assert.Equal(t, repo.Link()+"/deploy", test.RedirectURL(home))

	releases := session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/releases"), http.StatusOK)
	tabs := NewHTMLParser(t, releases.Body).Find(".overflow-menu-items a")
	require.Equal(t, 5, tabs.Length(), "repo admins keep Settings (collaborators and the like)")
	assert.Equal(t, repo.Link()+"/deploy", tabs.Eq(0).AttrOr("href", ""))
	assert.Equal(t, repo.Link()+"/w3ds", tabs.Eq(1).AttrOr("href", ""))
	assert.Equal(t, repo.Link()+"/releases", tabs.Eq(2).AttrOr("href", ""))
	codeLink := tabs.Eq(3).AttrOr("href", "")
	assert.Equal(t, repo.Link()+"/src/branch/"+repo.DefaultBranch, codeLink, "code is reachable, but not the main tab")

	assert.Equal(t, repo.Link()+"/settings", tabs.Eq(4).AttrOr("href", ""))
	session.MakeRequest(t, NewRequest(t, "GET", repo.Link()+"/settings/collaboration"), http.StatusOK)

	code := session.MakeRequest(t, NewRequest(t, "GET", codeLink), http.StatusOK)
	assert.Positive(t, NewHTMLParser(t, code.Body).Find(".overflow-menu-items a.active[href='"+codeLink+"']").Length())

	session.MakeRequest(t, NewRequest(t, "POST", "/user/simple-mode?enabled=false"), http.StatusOK)
	session.MakeRequest(t, NewRequest(t, "GET", repo.Link()), http.StatusOK)
	dashboard = session.MakeRequest(t, NewRequest(t, "GET", "/"), http.StatusOK)
	assert.Equal(t, 1, NewHTMLParser(t, dashboard.Body).Find("#dashboard-repo-list").Length())
}

func TestHostingPublishesVersionTags(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		setupHosting(t)
		owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo, _, cleanup := tests.CreateDeclarativeRepo(t, owner, "tagged-app", nil, nil, []*files_service.ChangeRepoFile{
			{Operation: "create", TreePath: "Dockerfile", ContentReader: strings.NewReader("FROM scratch\nEXPOSE 8080\n")},
			{Operation: "create", TreePath: ".w3ds/platform.json", ContentReader: strings.NewReader(`{"schemaVersion":1,"platformName":"tagged-app","displayName":"tagged-app","description":"d","version":"0.1.0","ename":"","url":"","logoUrl":"","domains":["identity"],"inSubmission":false,"isDraft":true}`)},
		})
		defer cleanup()
		released := func(tag string) bool {
			rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: repo.ID, TagName: tag})
			return !rel.IsTag
		}

		// git tag v0.1.0 && git push --tags is all a vibe coder does.
		dstPath := t.TempDir()
		u.Path = NewAPITestContext(t, owner.Name, repo.Name, auth_model.AccessTokenScopeReadRepository).GitPath()
		u.User = url.UserPassword(owner.Name, userPassword)
		doGitClone(dstPath, u)(t)
		for _, tag := range []string{"v0.1.0", "not-a-version"} {
			_, _, err := git.NewCommand(git.DefaultContext, "tag").AddDynamicArguments(tag).RunStdString(&git.RunOpts{Dir: dstPath})
			require.NoError(t, err)
		}
		_, _, err := git.NewCommand(git.DefaultContext, "push", "--tags").RunStdString(&git.RunOpts{Dir: dstPath})
		require.NoError(t, err)
		assert.Eventually(t, func() bool { return released("v0.1.0") }, 15*time.Second, 200*time.Millisecond,
			"a version tag of a W3DS platform becomes a release the PPA can certify")
		assert.False(t, released("not-a-version"))

		// A tag that is still only a tag is published when it is deployed.
		session := loginUser(t, owner.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)
		createNewTagUsingAPI(t, token, owner.Name, repo.Name, "v0.2.0", repo.DefaultBranch, "")
		require.False(t, released("v0.2.0"))
		tag := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: repo.ID, TagName: "v0.2.0"})
		session.MakeRequest(t, NewRequestWithValues(t, "POST", repo.Link()+"/deploy/managed", map[string]string{
			"release_id": fmt.Sprint(tag.ID),
		}), http.StatusCreated)
		assert.True(t, released("v0.2.0"))
	})
}

func TestHostingSimpleDeployPage(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		setupHosting(t)
		defer test.MockVariableValue(&setting.UI.DefaultSimpleMode, true)()
		owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, owner.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

		// A repository without a Dockerfile gets the AI prompt as step 1.
		empty, _, cleanupEmpty := tests.CreateDeclarativeRepo(t, owner, "vibe-app", nil, nil, []*files_service.ChangeRepoFile{{
			Operation: "create", TreePath: "index.js", ContentReader: strings.NewReader("console.log('hi')\n"),
		}})
		defer cleanupEmpty()
		page := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", empty.Link()+"/deploy"), http.StatusOK).Body)
		assert.Equal(t, 1, page.Find(".managed-simple").Length())
		assert.Zero(t, page.Find(".deploy-mode-tabs").Length(), "simple mode has no managed/self-hosted switch")
		assert.Contains(t, page.Find("#managed-ai-prompt").Text(), "Dockerfile")
		assert.Equal(t, "vibe-app", page.Find("[data-managed-subdomain] input").AttrOr("value", ""))

		check := session.MakeRequest(t, NewRequest(t, "GET", empty.Link()+"/deploy/managed/subdomain?name=infra"), http.StatusOK)
		var availability struct {
			Available bool   `json:"available"`
			Message   string `json:"message"`
		}
		DecodeJSON(t, check, &availability)
		assert.False(t, availability.Available)
		assert.NotEmpty(t, availability.Message)

		// Preflight stops an image that would bake secrets in, before any build.
		leaky, _, cleanupLeaky := tests.CreateDeclarativeRepo(t, owner, "leaky-app", nil, nil, []*files_service.ChangeRepoFile{{
			Operation: "create", TreePath: "Dockerfile",
			ContentReader: strings.NewReader("FROM node:22\nCOPY .env ./\nEXPOSE 3000\n"),
		}})
		defer cleanupLeaky()
		release := createNewReleaseUsingAPI(t, token, owner, leaky, "v1.0.0", leaky.DefaultBranch, "v1.0.0", "")
		rejected := session.MakeRequest(t, NewRequestWithValues(t, "POST", leaky.Link()+"/deploy/managed", map[string]string{
			"release_id": fmt.Sprint(release.ID), "subdomain": "leaky-app",
		}), http.StatusBadRequest)
		assert.Contains(t, rejected.Body.String(), ".env")
	})
}
