// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"
	"strings"

	actions_model "forgejo.org/models/actions"
	hosting_model "forgejo.org/models/hosting"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/actions"
)

const (
	maxBuildLogLines = 5000
	appLogLines      = 300
)

// ErrAppLogsUnavailable means the cluster's logs cannot be read: no Swarm
// proxy is configured, or the app was never deployed.
var ErrAppLogsUnavailable = errors.New("app logs are unavailable")

// AppLog returns what a running app wrote to stdout and stderr, newest last.
func AppLog(ctx context.Context, target *hosting_model.Target) (string, error) {
	c := current()
	if c.Swarm == nil {
		return "", ErrAppLogsUnavailable
	}
	if target.DokployComposeID != "" {
		repo, err := repo_model.GetRepositoryByID(ctx, target.RepoID)
		if err != nil {
			return "", err
		}
		return c.Swarm.Logs(ctx, "", dokployAppName(repo, target), appLogLines)
	}
	if target.DokployAppID == "" {
		return "", ErrAppLogsUnavailable
	}
	state, err := c.Dokploy.AppState(ctx, target.DokployAppID)
	if err != nil {
		return "", err
	}
	return c.Swarm.Logs(ctx, state.AppName, "", appLogLines)
}

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
		if before, _, found := strings.Cut(line, "&sig="); found {
			lines[i] = before + "&sig=[redacted]"
		}
	}
	return strings.Join(lines, "\n")
}
