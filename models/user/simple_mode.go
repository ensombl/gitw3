// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"context"
	"strconv"

	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
)

// SettingsKeySimpleMode stores whether a user sees GitW3's Simple Mode.
const SettingsKeySimpleMode = "ui.simple_mode"

// IsSimpleMode reports whether the user browses in Simple Mode, which only
// shows their repositories, the Deploy tab and releases.
func IsSimpleMode(ctx context.Context, u *User) bool {
	if u == nil {
		return false
	}
	value, err := GetSetting(ctx, u.ID, SettingsKeySimpleMode)
	if IsErrUserSettingIsNotExist(err) {
		return setting.UI.DefaultSimpleMode
	}
	if err != nil {
		log.Warn("Read simple mode setting of user %d: %v", u.ID, err)
		return setting.UI.DefaultSimpleMode
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return setting.UI.DefaultSimpleMode
	}
	return enabled
}

// SetSimpleMode switches a user between Simple and Advanced mode.
func SetSimpleMode(ctx context.Context, userID int64, enabled bool) error {
	return SetUserSetting(ctx, userID, SettingsKeySimpleMode, strconv.FormatBool(enabled))
}
