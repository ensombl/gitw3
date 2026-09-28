// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"strings"

	actions_model "forgejo.org/models/actions"
	hosting_model "forgejo.org/models/hosting"
	"forgejo.org/modules/actions"
)

const maxBuildLogLines = 5000

// BuildLog returns the builder output of a deployment. The builder repo is
// private to operators, so app owners read their logs through GitW3 instead
// of the Actions UI; permission is checked against the app repository.
func BuildLog(ctx context.Context, deployment *hosting_model.Deployment) (string, error) {
	job, err := hosting_model.GetBuildJobByDeployment(ctx, deployment.ID)
	if err != nil || job.RunID == 0 {
		return "", err
	}
	jobs, err := actions_model.GetRunJobsByRunID(ctx, job.RunID)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, runJob := range jobs {
		if runJob.TaskID == 0 {
			continue
		}
		task, err := actions_model.GetTaskByID(ctx, runJob.TaskID)
		if err != nil {
			return "", err
		}
		rows, err := actions.ReadLogs(ctx, task.LogInStorage, task.LogFilename, 0, maxBuildLogLines)
		if err != nil {
			return out.String(), err
		}
		out.WriteString("== " + runJob.Name + " ==\n")
		for _, row := range rows {
			out.WriteString(row.Content)
			out.WriteByte('\n')
		}
	}
	return redactLog(out.String()), nil
}

// redactLog removes anything that looks like a signed source URL query, in
// case a build step echoed it.
func redactLog(log string) string {
	lines := strings.Split(log, "\n")
	for i, line := range lines {
		if index := strings.Index(line, "&sig="); index >= 0 {
			lines[i] = line[:index] + "&sig=[redacted]"
		}
	}
	return strings.Join(lines, "\n")
}
