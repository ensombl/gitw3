// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"regexp"
	"strings"
)

// Preflight is the result of the instant checks run before a build is
// queued. Problems stop the deploy; warnings are shown with it.
type Preflight struct {
	Problems []string
	Warnings []string
}

// OK reports whether the build may start.
func (p *Preflight) OK() bool {
	return len(p.Problems) == 0
}

var (
	fromPattern      = regexp.MustCompile(`(?im)^\s*FROM\s+\S+`)
	portEnvPattern   = regexp.MustCompile(`(?im)^\s*ENV\s+PORT[\s=]`)
	copyEnvPattern   = regexp.MustCompile(`(?im)^\s*(COPY|ADD)\b[^\n]*[\s/]\.env(\.(local|production|prod|development|dev|staging|test))?(\s|$)`)
	localhostPattern = regexp.MustCompile(`(?im)^\s*(CMD|ENTRYPOINT)\b[^\n]*(localhost|127\.0\.0\.1)`)
	copyAllPattern   = regexp.MustCompile(`(?im)^\s*(COPY|ADD)\s+(--\S+\s+)*\.\s+\S+`)
)

// CheckDockerfile catches the mistakes that most often break a first
// deploy, in plain words, without starting a build. hasDockerignore says
// whether a .dockerignore exists next to the build context.
func CheckDockerfile(dockerfile string, hasDockerignore bool) *Preflight {
	result := &Preflight{}
	if strings.TrimSpace(dockerfile) == "" {
		result.Problems = append(result.Problems, "The Dockerfile is empty.")
		return result
	}
	if !fromPattern.MatchString(dockerfile) {
		result.Problems = append(result.Problems, "The Dockerfile has no FROM line, so there is no base image to build on.")
	}
	if copyEnvPattern.MatchString(dockerfile) {
		result.Problems = append(result.Problems, "The Dockerfile copies a .env file into the image. Secrets must not be built into images: remove that line and add the variables in GitW3 instead.")
	}
	if localhostPattern.MatchString(dockerfile) {
		result.Warnings = append(result.Warnings, "The start command mentions localhost/127.0.0.1. Inside a container the app must listen on 0.0.0.0, or it will be unreachable.")
	}
	if !exposePattern.MatchString(dockerfile) && !portEnvPattern.MatchString(dockerfile) {
		result.Warnings = append(result.Warnings, "The Dockerfile does not say which port the app uses (no EXPOSE or ENV PORT). GitW3 assumes 3000 and sets PORT; make sure the app listens on $PORT.")
	}
	if !hasDockerignore && copyAllPattern.MatchString(dockerfile) {
		result.Warnings = append(result.Warnings, "There is no .dockerignore, so COPY . copies everything (including .git and local dependencies). Builds will be slower and may leak local files.")
	}
	return result
}
