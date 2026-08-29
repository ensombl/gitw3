// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxWebhookBody = 2 << 20

type webhookRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

type webhookPayload struct {
	Action     string            `json:"action"`
	Ref        string            `json:"ref"`
	After      string            `json:"after"`
	Repository webhookRepository `json:"repository"`
}

// Server exposes the signed webhook and authenticated status API.
type Server struct {
	config Config
	store  *Store
	mux    *http.ServeMux
}

func NewServer(config Config, store *Store) *Server {
	server := &Server{config: config, store: store, mux: http.NewServeMux()}
	server.mux.HandleFunc("POST /webhooks/forgejo", server.handleWebhook)
	server.mux.HandleFunc("GET /api/v1/status/{repositoryID}", server.handleStatus)
	server.mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	return server
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) handleWebhook(response http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebhookBody))
	if err != nil {
		http.Error(response, "invalid webhook body", http.StatusBadRequest)
		return
	}
	if !validSignature(body, request.Header.Get("X-Forgejo-Signature"), s.config.WebhookSecret) {
		http.Error(response, "invalid webhook signature", http.StatusUnauthorized)
		return
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(response, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if payload.Repository.ID <= 0 || payload.Repository.FullName == "" || payload.Repository.DefaultBranch == "" {
		http.Error(response, "missing repository details", http.StatusBadRequest)
		return
	}

	event := request.Header.Get("X-Forgejo-Event")
	archive := false
	switch event {
	case "repository":
		switch payload.Action {
		case "created":
		case "deleted":
			archive = true
		default:
			response.WriteHeader(http.StatusNoContent)
			return
		}
	case "push":
		if payload.Ref != "refs/heads/"+payload.Repository.DefaultBranch {
			response.WriteHeader(http.StatusNoContent)
			return
		}
	case "release":
		switch payload.Action {
		case "published", "updated", "deleted":
			payload.After = ""
		default:
			response.WriteHeader(http.StatusNoContent)
			return
		}
	default:
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.Schedule(payload.Repository.ID, payload.Repository.FullName, payload.Repository.DefaultBranch, payload.After, archive); err != nil {
		http.Error(response, "could not schedule publication", http.StatusInternalServerError)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleStatus(response http.ResponseWriter, request *http.Request) {
	if !hmac.Equal([]byte(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")), []byte(s.config.InternalToken)) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	repositoryID, err := strconv.ParseInt(request.PathValue("repositoryID"), 10, 64)
	if err != nil || repositoryID <= 0 {
		http.Error(response, "invalid repository id", http.StatusBadRequest)
		return
	}
	job, err := s.store.Get(repositoryID)
	if err != nil {
		http.Error(response, "could not load publication status", http.StatusInternalServerError)
		return
	}
	if job == nil {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	json.NewEncoder(response).Encode(map[string]any{
		"repositoryId":      job.RepositoryID,
		"status":            job.Status,
		"ename":             job.EName,
		"releaseTag":        job.ReleaseTag,
		"releaseVersion":    job.ReleaseVersion,
		"lastError":         job.LastError,
		"attempts":          job.Attempts,
		"decision":          job.Decision,
		"decisions":         job.Decisions,
		"decisionCheckedAt": job.DecisionCheckedAt,
		"updatedAt":         job.UpdatedAt,
	})
}

func validSignature(body []byte, signature, secret string) bool {
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}
