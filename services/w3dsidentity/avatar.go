// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package w3dsidentity keeps GitW3 accounts in step with their owners' W3DS
// profiles. The eVault is the source of truth: profiles are read from
// Awareness-as-a-Service at every sign-in and periodically after that.
package w3dsidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/hostmatcher"
	"forgejo.org/modules/log"
	"forgejo.org/modules/proxy"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/w3ds"
	user_service "forgejo.org/services/user"
)

// AuthSourceName is the OAuth2 source W3DS users sign in with.
const AuthSourceName = "W3DS"

// avatarSourceSetting remembers which photo a user's avatar was made from,
// so an unchanged profile is not downloaded again.
const avatarSourceSetting = "w3ds.avatar_source"

// Enabled reports whether profiles can be read from AaaS.
func Enabled() bool {
	return setting.W3DSIdentity.AwarenessAPIKey != ""
}

// FetchProfile reads a user's W3DS profile from AaaS.
func FetchProfile(ctx context.Context, eName string) (*w3ds.PersonProfile, error) {
	client := &http.Client{Timeout: setting.W3DSIdentity.Timeout}
	return w3ds.FetchPersonProfile(ctx, client, setting.W3DSIdentity.AwarenessURL, setting.W3DSIdentity.AwarenessAPIKey, eName)
}

// ApplyAvatar makes source (an http(s) URL or data:image URI from the user's
// W3DS profile) the user's avatar, unless it already is. It reports whether
// the avatar changed.
func ApplyAvatar(ctx context.Context, u *user_model.User, source string) (bool, error) {
	if !w3ds.UsableAvatar(source) {
		return false, nil
	}
	sum := sha256.Sum256([]byte(source))
	fingerprint := hex.EncodeToString(sum[:])
	if u.UseCustomAvatar {
		if current, err := user_model.GetUserSetting(ctx, u.ID, avatarSourceSetting); err == nil && current == fingerprint {
			return false, nil
		}
	}
	data, err := loadAvatar(ctx, source)
	if err != nil {
		return false, err
	}
	if err := user_service.UploadAvatar(ctx, u, data); err != nil {
		return false, fmt.Errorf("store avatar: %w", err)
	}
	return true, user_model.SetUserSetting(ctx, u.ID, avatarSourceSetting, fingerprint)
}

func loadAvatar(ctx context.Context, source string) ([]byte, error) {
	if data, ok := w3ds.DecodeDataImage(source); ok {
		if int64(len(data)) > setting.Avatar.MaxFileSize {
			return nil, errors.New("embedded avatar is too large")
		}
		return data, nil
	}
	// Profiles are user-controlled: only fetch from allowed (by default,
	// external) hosts.
	allowedHosts := hostmatcher.ParseHostMatchList("w3ds_identity.AVATAR_ALLOWED_HOST_LIST", setting.W3DSIdentity.AvatarAllowedHostList)
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:       proxy.Proxy(),
			DialContext: hostmatcher.NewDialContext("W3DS avatar", allowedHosts, nil, setting.Proxy.ProxyURLFixed),
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avatar host returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, setting.Avatar.MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > setting.Avatar.MaxFileSize {
		return nil, errors.New("avatar is too large")
	}
	return data, nil
}

// SyncAvatars refreshes the avatar of every W3DS user from their profile.
func SyncAvatars(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	source, err := auth_model.GetActiveOAuth2SourceByName(ctx, AuthSourceName)
	if err != nil {
		return err
	}
	users := make([]*user_model.User, 0, 64)
	if err := db.GetEngine(ctx).Where("login_source = ? AND type = ?", source.ID, user_model.UserTypeIndividual).Find(&users); err != nil {
		return err
	}
	updated := 0
	for _, u := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		eName := strings.TrimSpace(u.LoginName)
		if eName == "" {
			continue
		}
		profile, err := FetchProfile(ctx, eName)
		if err != nil {
			log.Warn("W3DS avatar sync: profile of %s: %v", u.Name, err)
			continue
		}
		if profile == nil || profile.AvatarURL == "" {
			continue
		}
		changed, err := ApplyAvatar(ctx, u, profile.AvatarURL)
		if err != nil {
			log.Warn("W3DS avatar sync: avatar of %s: %v", u.Name, err)
			continue
		}
		if changed {
			updated++
		}
	}
	if updated > 0 {
		log.Info("W3DS avatar sync updated %d avatars", updated)
	}
	return nil
}
