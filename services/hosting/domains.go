// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	hosting_model "forgejo.org/models/hosting"
	hosting_module "forgejo.org/modules/hosting"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
)

// DNSResolver looks up custom domains during verification.
type DNSResolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Resolver is swapped in tests to fake DNS lookups for custom domains.
var Resolver DNSResolver = net.DefaultResolver

func domainSpec(target *hosting_model.Target, spec *hosting_module.Target, host string) DomainSpec {
	domain := DomainSpec{Host: host, Port: spec.Port}
	if target.DokployComposeID != "" {
		domain.ComposeID = target.DokployComposeID
		domain.ServiceName = spec.Service
	} else {
		domain.AppID = target.DokployAppID
	}
	return domain
}

// routable reports whether a target exposes HTTP at all (compose targets
// without a public service are internal-only).
func routable(spec *hosting_module.Target) bool {
	return spec.Kind != hosting_module.KindCompose || spec.Service != ""
}

func attachPoolDomain(ctx context.Context, target *hosting_model.Target, spec *hosting_module.Target) error {
	if !routable(spec) {
		return nil
	}
	domain, err := hosting_model.ClaimPoolDomain(ctx, target.ID)
	if errors.Is(err, hosting_model.ErrPoolEmpty) {
		// The pool is refilled by cron; do not make a first deploy wait on it.
		domain, err = provisionPoolDomain(ctx, hosting_model.DomainAssigned, target.ID)
	}
	if err != nil {
		return fmt.Errorf("assign a domain: %w", err)
	}
	return routeDomain(ctx, target, spec, domain)
}

func routeDomain(ctx context.Context, target *hosting_model.Target, spec *hosting_module.Target, domain *hosting_model.Domain) error {
	if domain.DokployDomainID != "" {
		if err := current().Dokploy.RemoveDomain(ctx, domain.DokployDomainID); err != nil && !isDokployNotFound(err) {
			return fmt.Errorf("unroute %s: %w", domain.FQDN, err)
		}
	}
	id, err := current().Dokploy.AddDomain(ctx, domainSpec(target, spec, domain.FQDN))
	if err != nil {
		return fmt.Errorf("route %s: %w", domain.FQDN, err)
	}
	domain.DokployDomainID = id
	return hosting_model.UpdateDomainCols(ctx, domain, "dokploy_domain_id")
}

func rerouteDomains(ctx context.Context, target *hosting_model.Target, spec *hosting_module.Target) error {
	domains, err := hosting_model.ListTargetDomains(ctx, target.ID)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return attachPoolDomain(ctx, target, spec)
	}
	for _, domain := range domains {
		if !domain.IsRoutable() {
			continue
		}
		if !routable(spec) {
			if err := current().Dokploy.RemoveDomain(ctx, domain.DokployDomainID); err != nil && !isDokployNotFound(err) {
				return err
			}
			domain.DokployDomainID = ""
			if err := hosting_model.UpdateDomainCols(ctx, domain, "dokploy_domain_id"); err != nil {
				return err
			}
			continue
		}
		if err := routeDomain(ctx, target, spec, domain); err != nil {
			return err
		}
	}
	return nil
}

// provisionPoolDomain creates one new pool name and its DNS record.
func provisionPoolDomain(ctx context.Context, status string, targetID int64) (*hosting_model.Domain, error) {
	base := setting.Hosting.Domains.BaseDomain
	var fqdn string
	for range 20 {
		candidate := hosting_module.PoolFQDN(hosting_module.RandomName(), base)
		exists, err := hosting_model.FQDNExists(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if !exists {
			fqdn = candidate
			break
		}
	}
	if fqdn == "" {
		return nil, errors.New("could not find a free domain name")
	}
	domain := &hosting_model.Domain{FQDN: fqdn, Kind: hosting_model.DomainPool, Status: hosting_model.DomainProvisioning}
	if err := hosting_model.CreateDomain(ctx, domain); err != nil {
		return nil, err
	}
	recordID, err := current().DNS.CreateRecord(ctx, fqdn)
	if err != nil {
		_ = hosting_model.DeleteDomain(ctx, domain.ID)
		return nil, err
	}
	domain.ProviderRecordID, domain.Status, domain.TargetID = recordID, status, targetID
	return domain, hosting_model.UpdateDomainCols(ctx, domain, "provider_record_id", "status", "target_id")
}

// RefillDomainPool keeps POOL_SIZE ready domains so first deploys get a URL
// immediately. It also retries pool rows stuck in provisioning.
func RefillDomainPool(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	stuck, err := hosting_model.ListDomainsByStatus(ctx, hosting_model.DomainProvisioning)
	if err != nil {
		return err
	}
	for _, domain := range stuck {
		if domain.Kind == hosting_model.DomainPool && domain.TargetID == 0 {
			_ = hosting_model.DeleteDomain(ctx, domain.ID)
		}
	}
	available, err := hosting_model.CountAvailablePoolDomains(ctx)
	if err != nil {
		return err
	}
	for i := available; i < int64(setting.Hosting.Domains.PoolSize); i++ {
		if _, err := provisionPoolDomain(ctx, hosting_model.DomainAvailable, 0); err != nil {
			return fmt.Errorf("refill domain pool: %w", err)
		}
	}
	return nil
}

func releaseDomain(ctx context.Context, domain *hosting_model.Domain) error {
	if domain.DokployDomainID != "" {
		if err := current().Dokploy.RemoveDomain(ctx, domain.DokployDomainID); err != nil && !isDokployNotFound(err) {
			return fmt.Errorf("unroute %s: %w", domain.FQDN, err)
		}
	}
	if domain.Kind == hosting_model.DomainPool {
		if err := current().DNS.DeleteRecord(ctx, domain.ProviderRecordID); err != nil {
			return fmt.Errorf("delete DNS record for %s: %w", domain.FQDN, err)
		}
	}
	return hosting_model.DeleteDomain(ctx, domain.ID)
}

// AddCustomDomain records a user-owned domain. It becomes routable once
// VerifyCustomDomain confirms its DNS points at the target.
func AddCustomDomain(ctx context.Context, target *hosting_model.Target, host string, actorID int64) (*hosting_model.Domain, error) {
	if !setting.Hosting.Domains.AllowCustom {
		return nil, errors.New("custom domains are disabled on this instance")
	}
	fqdn, err := hosting_module.NormalizeDomain(host)
	if err != nil {
		return nil, err
	}
	if base := setting.Hosting.Domains.BaseDomain; fqdn == base || strings.HasSuffix(fqdn, "."+base) {
		return nil, fmt.Errorf("names under %s are assigned automatically", base)
	}
	domain := &hosting_model.Domain{FQDN: fqdn, Kind: hosting_model.DomainCustom, Status: hosting_model.DomainPendingVerification, TargetID: target.ID}
	if err := hosting_model.CreateDomain(ctx, domain); err != nil {
		return nil, err
	}
	hosting_model.Audit(ctx, actorID, target.RepoID, target.ID, 0, hosting_model.AuditDomainAttached, map[string]any{"domain": fqdn})
	return domain, nil
}

// DNSInstructions returns the record a user should create for a custom domain.
func DNSInstructions(ctx context.Context, target *hosting_model.Target) (recordType, value string) {
	if pool := PoolDomain(ctx, target); pool != nil {
		return "CNAME", pool.FQDN
	}
	return "A", setting.Hosting.Domains.TargetIP
}

// VerifyCustomDomain checks that a custom domain resolves to the target and,
// if so, routes it through Traefik with a Let's Encrypt certificate.
func VerifyCustomDomain(ctx context.Context, target *hosting_model.Target, domain *hosting_model.Domain, actorID int64) error {
	if domain.TargetID != target.ID || domain.Kind != hosting_model.DomainCustom {
		return hosting_model.ErrDomainNotExist
	}
	if domain.Status == hosting_model.DomainVerified {
		return nil
	}
	if !pointsAtTarget(ctx, target, domain.FQDN) {
		recordType, value := DNSInstructions(ctx, target)
		return fmt.Errorf("%s does not point here yet: add a %s record with the value %s (DNS changes can take a few minutes)", domain.FQDN, recordType, value)
	}
	spec, err := target.Spec()
	if err != nil {
		return err
	}
	domain.Status = hosting_model.DomainVerified
	if err := hosting_model.UpdateDomainCols(ctx, domain, "status"); err != nil {
		return err
	}
	if routable(spec) {
		if err := routeDomain(ctx, target, spec, domain); err != nil {
			return err
		}
	}
	hosting_model.Audit(ctx, actorID, target.RepoID, target.ID, 0, hosting_model.AuditDomainVerified, map[string]any{"domain": domain.FQDN})
	return nil
}

func pointsAtTarget(ctx context.Context, target *hosting_model.Target, fqdn string) bool {
	expected := map[string]bool{}
	if ip := setting.Hosting.Domains.TargetIP; ip != "" {
		expected[ip] = true
	}
	if pool := PoolDomain(ctx, target); pool != nil {
		if cname, err := Resolver.LookupCNAME(ctx, fqdn); err == nil && strings.TrimSuffix(cname, ".") == pool.FQDN {
			return true
		}
		if addresses, err := Resolver.LookupHost(ctx, pool.FQDN); err == nil {
			for _, address := range addresses {
				expected[address] = true
			}
		}
	}
	addresses, err := Resolver.LookupHost(ctx, fqdn)
	if err != nil || len(expected) == 0 {
		return false
	}
	return slices.ContainsFunc(addresses, func(address string) bool { return expected[address] })
}

// RemoveDomain detaches a custom domain. Pool domains stay with the target.
func RemoveDomain(ctx context.Context, target *hosting_model.Target, domain *hosting_model.Domain, actorID int64) error {
	if domain.TargetID != target.ID || domain.Kind != hosting_model.DomainCustom {
		return hosting_model.ErrDomainNotExist
	}
	if err := releaseDomain(ctx, domain); err != nil {
		return err
	}
	hosting_model.Audit(ctx, actorID, target.RepoID, target.ID, 0, hosting_model.AuditDomainRemoved, map[string]any{"domain": domain.FQDN})
	return nil
}

// PoolDomain returns the target's automatically assigned domain, if any.
func PoolDomain(ctx context.Context, target *hosting_model.Target) *hosting_model.Domain {
	domains, err := hosting_model.ListTargetDomains(ctx, target.ID)
	if err != nil {
		log.Error("List domains of target %d: %v", target.ID, err)
		return nil
	}
	for _, domain := range domains {
		if domain.Kind == hosting_model.DomainPool {
			return domain
		}
	}
	return nil
}

// PublicURL is the preferred public URL of a target: a verified custom
// domain, else its pool domain.
func PublicURL(ctx context.Context, target *hosting_model.Target) string {
	domains, err := hosting_model.ListTargetDomains(ctx, target.ID)
	if err != nil {
		return ""
	}
	var pool string
	for _, domain := range domains {
		if domain.Kind == hosting_model.DomainCustom && domain.Status == hosting_model.DomainVerified {
			return "https://" + domain.FQDN
		}
		if domain.Kind == hosting_model.DomainPool && domain.IsRoutable() {
			pool = "https://" + domain.FQDN
		}
	}
	return pool
}
