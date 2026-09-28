// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"errors"
	"regexp"
	"strings"
)

// Subdomain errors are shown to users as-is.
var (
	ErrSubdomainInvalid  = errors.New("use 3–40 lowercase letters, digits and dashes, starting with a letter")
	ErrSubdomainReserved = errors.New("that name is reserved")
)

var subdomainPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// reservedSubdomains can never be claimed: platform hosts, infrastructure
// and names that invite phishing.
var reservedSubdomains = map[string]bool{
	"infra": true, "www": true, "api": true, "app": true, "apps": true, "admin": true,
	"administrator": true, "root": true, "mail": true, "smtp": true, "imap": true, "pop": true,
	"ftp": true, "ns": true, "ns1": true, "ns2": true, "dns": true, "git": true, "gitw3": true,
	"registry": true, "docker": true, "dokploy": true, "traefik": true, "manager": true,
	"builder": true, "worker": true, "status": true, "docs": true, "help": true, "support": true,
	"login": true, "auth": true, "oauth": true, "sso": true, "id": true, "account": true,
	"accounts": true, "billing": true, "pay": true, "payment": true, "secure": true,
	"security": true, "wallet": true, "w3ds": true, "metastate": true, "evault": true,
	"ename": true, "ppa": true, "static": true, "cdn": true, "assets": true, "dev": true,
	"test": true, "staging": true, "localhost": true,
}

// NormalizeSubdomain lowercases and trims a user-typed name. Users may paste
// the full host; the base domain suffix is stripped.
func NormalizeSubdomain(value, baseDomain string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
	value = strings.TrimSuffix(value, "/")
	if baseDomain != "" {
		value = strings.TrimSuffix(value, "."+baseDomain)
	}
	return value
}

// ValidateSubdomain checks a single-label name under the base domain.
func ValidateSubdomain(label string) error {
	if !subdomainPattern.MatchString(label) || strings.Contains(label, "--") {
		return ErrSubdomainInvalid
	}
	if reservedSubdomains[label] {
		return ErrSubdomainReserved
	}
	return nil
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// SuggestSubdomain turns a repository name into a candidate subdomain.
func SuggestSubdomain(repoName string) string {
	slug := strings.Trim(slugInvalid.ReplaceAllString(strings.ToLower(repoName), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		slug = "app-" + slug
	}
	for len(slug) < 3 {
		slug += "-app"
	}
	if ValidateSubdomain(slug) != nil {
		return RandomName()
	}
	return slug
}
