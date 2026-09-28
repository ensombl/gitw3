// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/timeutil"
)

// EnvVar is one environment variable injected into a target's containers.
// Values are always encrypted; secrets are additionally never shown again.
type EnvVar struct {
	ID          int64              `xorm:"pk autoincr"`
	TargetID    int64              `xorm:"UNIQUE(target_key) INDEX NOT NULL"`
	Key         string             `xorm:"UNIQUE(target_key) VARCHAR(255) NOT NULL"`
	ValueEnc    []byte             `xorm:"BLOB"`
	IsSecret    bool               `xorm:"NOT NULL DEFAULT false"`
	CreatedUnix timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func (EnvVar) TableName() string { return "hosting_env_var" }

func init() {
	db.RegisterModel(new(EnvVar))
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

// ReservedEnvPrefix is used by variables GitW3 injects itself.
const ReservedEnvPrefix = "GITW3_"

// ValidateEnvKey checks a variable name.
func ValidateEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) {
		return errors.New("variable names use letters, digits and underscores and cannot start with a digit")
	}
	if strings.HasPrefix(strings.ToUpper(key), ReservedEnvPrefix) {
		return fmt.Errorf("variable names starting with %s are reserved", ReservedEnvPrefix)
	}
	return nil
}

func (v *EnvVar) setValue(value string) {
	v.ValueEnc = keying.HostingEnvVar.Encrypt([]byte(value), keying.ColumnAndID("value", v.ID))
}

// Value decrypts the variable.
func (v *EnvVar) Value() (string, error) {
	plain, err := keying.HostingEnvVar.Decrypt(v.ValueEnc, keying.ColumnAndID("value", v.ID))
	if err != nil {
		return "", fmt.Errorf("decrypt env var %d: %w", v.ID, err)
	}
	return string(plain), nil
}

// SetEnvVar inserts or replaces a variable.
func SetEnvVar(ctx context.Context, targetID int64, key, value string, secret bool) error {
	if err := ValidateEnvKey(key); err != nil {
		return err
	}
	return db.WithTx(ctx, func(ctx context.Context) error {
		existing := &EnvVar{}
		has, err := db.GetEngine(ctx).Where("target_id = ? AND `key` = ?", targetID, key).Get(existing)
		if err != nil {
			return err
		}
		if !has {
			existing = &EnvVar{TargetID: targetID, Key: key, IsSecret: secret}
			if err := db.Insert(ctx, existing); err != nil {
				return err
			}
		}
		existing.IsSecret = secret
		existing.setValue(value)
		_, err = db.GetEngine(ctx).ID(existing.ID).Cols("value_enc", "is_secret", "updated_unix").Update(existing)
		return err
	})
}

func DeleteEnvVar(ctx context.Context, targetID int64, key string) error {
	_, err := db.GetEngine(ctx).Where("target_id = ? AND `key` = ?", targetID, key).Delete(new(EnvVar))
	return err
}

func ListEnvVars(ctx context.Context, targetID int64) ([]*EnvVar, error) {
	vars := make([]*EnvVar, 0, 8)
	return vars, db.GetEngine(ctx).Where("target_id = ?", targetID).Asc("`key`").Find(&vars)
}

// EnvMap decrypts all variables of a target.
func EnvMap(ctx context.Context, targetID int64) (map[string]string, error) {
	vars, err := ListEnvVars(ctx, targetID)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(vars))
	for _, v := range vars {
		value, err := v.Value()
		if err != nil {
			return nil, err
		}
		env[v.Key] = value
	}
	return env, nil
}

// ReplaceEnv makes a target's variables equal to env, keeping secret flags
// for keys that remain. Used when a rollback restores an env snapshot.
func ReplaceEnv(ctx context.Context, targetID int64, env map[string]string) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		vars, err := ListEnvVars(ctx, targetID)
		if err != nil {
			return err
		}
		secret := make(map[string]bool, len(vars))
		for _, v := range vars {
			secret[v.Key] = v.IsSecret
			if _, ok := env[v.Key]; !ok {
				if err := DeleteEnvVar(ctx, targetID, v.Key); err != nil {
					return err
				}
			}
		}
		for key, value := range env {
			if err := SetEnvVar(ctx, targetID, key, value, secret[key]); err != nil {
				return err
			}
		}
		return nil
	})
}
