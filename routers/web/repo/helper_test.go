// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"testing"

	"forgejo.org/models/unittest"
	"forgejo.org/models/user"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakeSelfOnTop(t *testing.T) {
	users := MakeSelfOnTop(nil, []*user.User{{ID: 2}, {ID: 1}})
	assert.Len(t, users, 2)
	assert.EqualValues(t, 2, users[0].ID)

	users = MakeSelfOnTop(&user.User{ID: 1}, []*user.User{{ID: 2}, {ID: 1}})
	assert.Len(t, users, 2)
	assert.EqualValues(t, 1, users[0].ID)

	users = MakeSelfOnTop(&user.User{ID: 2}, []*user.User{{ID: 2}, {ID: 1}})
	assert.Len(t, users, 2)
	assert.EqualValues(t, 2, users[0].ID)

	users = MakeSelfOnTop(&user.User{ID: 2}, []*user.User{{ID: 1}})
	assert.Len(t, users, 1)
	assert.EqualValues(t, 1, users[0].ID)

	users = MakeSelfOnTop(&user.User{ID: 2}, []*user.User{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}})
	assert.Len(t, users, 4)
	assert.EqualValues(t, 2, users[0].ID)
	assert.EqualValues(t, 1, users[1].ID)
	assert.EqualValues(t, 3, users[2].ID)
	assert.EqualValues(t, 4, users[3].ID)
}

func TestAvailablePlatformRepositoryName(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx, _ := contexttest.MockContext(t, "/repo/create/new")
	owner := unittest.AssertExistsAndLoadBean(t, &user.User{ID: 2})

	name, err := availablePlatformRepositoryName(ctx, owner, "Repo 1")
	require.NoError(t, err)
	assert.Equal(t, "repo-1", name)

	name, err = availablePlatformRepositoryName(ctx, owner, "Repo1")
	require.NoError(t, err)
	assert.Equal(t, "repo1-2", name)
}
