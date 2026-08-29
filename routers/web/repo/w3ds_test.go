// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"testing"

	"forgejo.org/modules/w3ds"

	"github.com/stretchr/testify/assert"
)

func TestCurrentPPASubmissionIsVersionScoped(t *testing.T) {
	manifest := &w3ds.PlatformManifest{Version: "2.0.0", InSubmission: true, SubmissionVersion: "1.0.0"}
	assert.False(t, currentPPASubmission(manifest))

	manifest.SubmissionVersion = "2.0.0"
	assert.True(t, currentPPASubmission(manifest))

	manifest.SubmissionVersion = ""
	assert.True(t, currentPPASubmission(manifest), "legacy submissions apply to their manifest's current version")
}

func TestCurrentPPADecisionIsVersionScoped(t *testing.T) {
	decision := &w3ds.AccreditationDecision{PlatformVersion: "1.0.0", Decision: "granted"}
	status := &w3ds.PublicationStatus{Decision: decision}
	assert.Same(t, decision, currentPPADecision(status, "1.0.0"))
	assert.Nil(t, currentPPADecision(status, "2.0.0"))
}
