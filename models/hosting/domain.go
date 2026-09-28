// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// Domain kinds.
const (
	DomainPool   = "pool"
	DomainCustom = "custom"
)

// Domain statuses.
const (
	DomainProvisioning        = "provisioning"
	DomainAvailable           = "available"
	DomainAssigned            = "assigned"
	DomainPendingVerification = "pending_verification"
	DomainVerified            = "verified"
	DomainReleasing           = "releasing"
)

// Domain is a host name routed to a target. Pool domains are pre-created so
// a first deploy gets a working URL instantly; custom domains are attached
// by users and verified through DNS.
type Domain struct {
	ID               int64              `xorm:"pk autoincr"`
	FQDN             string             `xorm:"UNIQUE VARCHAR(255) NOT NULL"`
	Kind             string             `xorm:"VARCHAR(16) NOT NULL"`
	Status           string             `xorm:"INDEX VARCHAR(32) NOT NULL"`
	TargetID         int64              `xorm:"INDEX NOT NULL DEFAULT 0"`
	ProviderRecordID string             `xorm:"VARCHAR(255)"`
	DokployDomainID  string             `xorm:"VARCHAR(128)"`
	CreatedUnix      timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix      timeutil.TimeStamp `xorm:"updated"`
}

func (Domain) TableName() string { return "hosting_domain" }

func init() {
	db.RegisterModel(new(Domain))
}

// IsRoutable reports whether traffic for the domain can be sent to its target.
func (d *Domain) IsRoutable() bool {
	return d.Status == DomainAssigned || d.Status == DomainVerified
}

var (
	ErrDomainNotExist = util.NewNotExistErrorf("hosting domain does not exist")
	ErrDomainTaken    = util.NewAlreadyExistErrorf("domain is already in use")
	ErrPoolEmpty      = errors.New("no pre-provisioned domains are available right now")
)

func CreateDomain(ctx context.Context, domain *Domain) error {
	exists, err := db.GetEngine(ctx).Where("fqdn = ?", domain.FQDN).Exist(new(Domain))
	if err != nil {
		return err
	}
	if exists {
		return ErrDomainTaken
	}
	return db.Insert(ctx, domain)
}

func GetDomain(ctx context.Context, id int64) (*Domain, error) {
	domain, exists, err := db.Get[Domain](ctx, builder.Eq{"id": id})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrDomainNotExist
	}
	return domain, nil
}

func FQDNExists(ctx context.Context, fqdn string) (bool, error) {
	return db.GetEngine(ctx).Where("fqdn = ?", fqdn).Exist(new(Domain))
}

func ListTargetDomains(ctx context.Context, targetID int64) ([]*Domain, error) {
	domains := make([]*Domain, 0, 2)
	return domains, db.GetEngine(ctx).Where("target_id = ?", targetID).Asc("id").Find(&domains)
}

func ListDomainsByStatus(ctx context.Context, status string) ([]*Domain, error) {
	domains := make([]*Domain, 0, 8)
	return domains, db.GetEngine(ctx).Where("status = ?", status).Asc("id").Find(&domains)
}

func CountAvailablePoolDomains(ctx context.Context) (int64, error) {
	return db.GetEngine(ctx).Where("kind = ? AND status = ?", DomainPool, DomainAvailable).Count(new(Domain))
}

// ClaimPoolDomain assigns one available pool domain to a target. The
// conditional update makes concurrent claims safe on every database.
func ClaimPoolDomain(ctx context.Context, targetID int64) (*Domain, error) {
	for range 5 {
		candidate := &Domain{}
		has, err := db.GetEngine(ctx).Where("kind = ? AND status = ?", DomainPool, DomainAvailable).Asc("id").Get(candidate)
		if err != nil {
			return nil, err
		}
		if !has {
			return nil, ErrPoolEmpty
		}
		affected, err := db.GetEngine(ctx).Where("id = ? AND status = ?", candidate.ID, DomainAvailable).
			Cols("status", "target_id", "updated_unix").
			Update(&Domain{Status: DomainAssigned, TargetID: targetID})
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			candidate.Status = DomainAssigned
			candidate.TargetID = targetID
			return candidate, nil
		}
	}
	return nil, ErrPoolEmpty
}

func UpdateDomainCols(ctx context.Context, domain *Domain, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(domain.ID).Cols(cols...).Update(domain)
	return err
}

func DeleteDomain(ctx context.Context, id int64) error {
	_, err := db.GetEngine(ctx).ID(id).Delete(new(Domain))
	return err
}

// GetDomainByFQDN returns the domain with a host name.
func GetDomainByFQDN(ctx context.Context, fqdn string) (*Domain, error) {
	domain, exists, err := db.Get[Domain](ctx, builder.Eq{"fqdn": fqdn})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrDomainNotExist
	}
	return domain, nil
}
