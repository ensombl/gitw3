// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package webhook

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	api "forgejo.org/modules/structs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWechatWorkPayload(t *testing.T) {
	wc := wechatworkConvertor{}

	t.Run("WorkflowJob", func(t *testing.T) {
		testCases := []struct {
			jobStatus    actions_model.Status
			expectedText string
		}{
			{
				jobStatus: actions_model.StatusBlocked,
				expectedText: `[acme/test] Workflow job "build-and-test" is blocked

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusCancelled,
				expectedText: `[acme/test] Workflow job "build-and-test" was cancelled

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusFailure,
				expectedText: `[acme/test] Workflow job "build-and-test" has failed

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusRunning,
				expectedText: `[acme/test] Workflow job "build-and-test" has started running

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusSkipped,
				expectedText: `[acme/test] Workflow job "build-and-test" was skipped

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusSuccess,
				expectedText: `[acme/test] Workflow job "build-and-test" has completed successfully

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
			{
				jobStatus: actions_model.StatusWaiting,
				expectedText: `[acme/test] Workflow job "build-and-test" is waiting

Repository: acme/test
Run: Update README.md
Job: build-and-test

View details on https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1.
`,
			},
		}

		for _, testCase := range testCases {
			t.Run(testCase.jobStatus.String(), func(t *testing.T) {
				inputPayload := &api.WorkflowJobPayload{
					Action: api.HookNewWorkflowJobAttempt,
					Job: &api.ActionRunJob{
						Name:    "build-and-test",
						HTMLURL: "https://example.com/acme/test/actions/runs/196540/jobs/3/attempt/1",
						Status:  testCase.jobStatus.String(),
					},
					Run: &api.ActionRun{
						Title: "Update README.md",
						TriggerUser: &api.User{
							UserName:  "jane",
							AvatarURL: "https://example.com/avatars/7dc9cf?size=64",
						},
					},
					Repository: &api.Repository{
						FullName: "acme/test",
					},
				}

				payload, err := wc.WorkflowJob(inputPayload)
				require.NoError(t, err)

				assert.Equal(t, testCase.expectedText, payload.Markdown.Content)
			})
		}
	})
}
