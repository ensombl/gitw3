// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CallbackSignatureHeader carries the hex HMAC-SHA256 of a builder callback body.
const CallbackSignatureHeader = "X-GitW3-Signature"

// SignCallback returns the hex HMAC-SHA256 of body under secret.
func SignCallback(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyCallback checks a builder callback signature in constant time.
func VerifyCallback(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	signature = strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	expected := SignCallback(secret, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// NewNonce returns a random single-use build nonce.
func NewNonce() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func sourceMAC(secret string, jobID, expires int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "gitw3-hosting-source:%d:%d", jobID, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignSourceURL returns the query string granting the builder time-limited
// read access to one build job's source archive.
func SignSourceURL(secret string, jobID int64, now time.Time, ttl time.Duration) string {
	expires := now.Add(ttl).Unix()
	return "exp=" + strconv.FormatInt(expires, 10) + "&sig=" + sourceMAC(secret, jobID, expires)
}

// VerifySourceURL checks a signed source archive request.
func VerifySourceURL(secret string, jobID int64, exp, sig string, now time.Time) bool {
	expires, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || secret == "" || now.Unix() > expires {
		return false
	}
	return hmac.Equal([]byte(sourceMAC(secret, jobID, expires)), []byte(sig))
}
