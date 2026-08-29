// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package w3ds_test

import (
	"testing"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	w3ds_model "forgejo.org/models/w3ds"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentLifecycle(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	deployment := &w3ds_model.Deployment{
		ID: "deployment-test", SigningPayload: "gitw3:deployment:v1:test",
		RepositoryID: 1, UserID: 1, DeployerEName: "@deployer",
		Name: "Production", Environment: "production", ReleaseID: 1,
		Version: "1.2.3", ReleaseTag: "v1.2.3", CommitSHA: "a",
		PlatformEName: "@platform", VersionEName: "@version", DeploymentEName: "@deployment",
		PublicKey: "zKey", BundlePayload: "{}", Status: w3ds_model.DeploymentAwaitingSignature,
		ExpiresUnix: timeutil.TimeStamp(time.Now().Add(time.Minute).Unix()),
	}
	require.NoError(t, w3ds_model.CreateDeployment(ctx, deployment))
	claimed, err := w3ds_model.ClaimDeploymentSignature(ctx, deployment.SigningPayload)
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = w3ds_model.ClaimDeploymentSignature(ctx, deployment.SigningPayload)
	require.NoError(t, err)
	assert.False(t, claimed)
	require.NoError(t, w3ds_model.RecordDeploymentSignature(ctx, deployment.ID, "sig", "key", "cert"))
	require.NoError(t, w3ds_model.UpdateDeploymentPublication(ctx, deployment.ID, w3ds_model.DeploymentCompleted, "", "doc-1", "doc-2"))
	stored, err := w3ds_model.GetDeployment(ctx, deployment.ID)
	require.NoError(t, err)
	assert.Equal(t, w3ds_model.DeploymentCompleted, stored.Status)
	assert.Equal(t, "doc-1", stored.DeploymentKeyDocumentID)
}
