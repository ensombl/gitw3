// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"testing"

	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
)

func TestMain(m *testing.M) {
	setting.IsInTesting = true
	unittest.MainTest(m)
}
