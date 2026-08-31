// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type publicationError struct {
	Stage string
	Code  string
	Path  string
	Err   error
}

func (e *publicationError) Error() string {
	if e == nil {
		return ""
	}
	message := "publication failed"
	if e.Err != nil {
		message = redactSensitiveText(e.Err.Error())
	}
	details := make([]string, 0, 2)
	if e.Code != "" {
		details = append(details, "code="+e.Code)
	}
	if e.Path != "" {
		details = append(details, "path="+e.Path)
	}
	if len(details) > 0 {
		message += " (" + strings.Join(details, ", ") + ")"
	}
	if e.Stage == "" {
		return message
	}
	return e.Stage + ": " + message
}

func (e *publicationError) Unwrap() error { return e.Err }

func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing *publicationError
	if errors.As(err, &existing) {
		return err
	}
	return &publicationError{Stage: stage, Err: err}
}

func publicationErrorMetadata(err error) (stage, code, path string) {
	var detail *publicationError
	if errors.As(err, &detail) {
		return detail.Stage, detail.Code, detail.Path
	}
	return "", "", ""
}

type graphQLError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions"`
}

type mutationPayloadError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

func graphQLErrors(stage string, errors []graphQLError) error {
	if len(errors) == 0 {
		return nil
	}
	first := errors[0]
	code, _ := first.Extensions["code"].(string)
	path := make([]string, 0, len(first.Path))
	for _, part := range first.Path {
		path = append(path, fmt.Sprint(part))
	}
	return &publicationError{
		Stage: stage,
		Code:  strings.TrimSpace(code),
		Path:  strings.Join(path, "."),
		Err:   errorsNewSafe(first.Message),
	}
}

func mutationPayloadErrors(stage string, errors []mutationPayloadError) error {
	if len(errors) == 0 {
		return nil
	}
	first := errors[0]
	return &publicationError{
		Stage: stage,
		Code:  strings.TrimSpace(first.Code),
		Path:  strings.TrimSpace(first.Field),
		Err:   errorsNewSafe(first.Message),
	}
}

func errorsNewSafe(message string) error {
	message = strings.TrimSpace(redactSensitiveText(message))
	if message == "" {
		message = "eVault returned an error without a safe message"
	}
	return errors.New(message)
}

var (
	jwtPattern       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	sensitivePattern = regexp.MustCompile(`(?i)(authorization|token|signature|certificate|secret)(["'[:space:]]*[:=]["'[:space:]]*)([^,"'[:space:]}]+)`)
)

func redactSensitiveText(text string) string {
	text = jwtPattern.ReplaceAllString(text, "[REDACTED]")
	return sensitivePattern.ReplaceAllString(text, "$1$2[REDACTED]")
}

func safeResponseBody(data []byte) string {
	var decoded any
	if json.Unmarshal(data, &decoded) == nil {
		redactSensitiveValue(decoded)
		if safe, err := json.Marshal(decoded); err == nil {
			return redactSensitiveText(string(safe))
		}
	}
	return redactSensitiveText(strings.TrimSpace(string(data)))
}

func redactSensitiveValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "authorization") ||
				strings.Contains(lower, "signature") || strings.Contains(lower, "certificate") || strings.Contains(lower, "secret") {
				typed[key] = "[REDACTED]"
				continue
			}
			redactSensitiveValue(child)
		}
	case []any:
		for _, child := range typed {
			redactSensitiveValue(child)
		}
	}
}
