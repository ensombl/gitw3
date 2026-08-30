// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"forgejo.org/modules/w3ds"

	bolt "go.etcd.io/bbolt"
)

const (
	jobsBucket           = "platform-jobs"
	deploymentJobsBucket = "deployment-jobs"
)

// Status is the user-visible publication lifecycle state.
type Status string

const (
	StatusIdentityPending Status = "identity_pending"
	StatusAwaitingDeploy  Status = "awaiting_deployment"
	StatusAwaitingCutover Status = "awaiting_cutover"
	StatusActivating      Status = "activating"
	StatusPublishing      Status = "publishing"
	StatusPublished       Status = "published"
	StatusFailed          Status = "failed"
	StatusArchived        Status = "archived"
)

// Job is the durable, repository-scoped reconciliation record.
type Job struct {
	RepositoryID        int64                        `json:"repositoryId"`
	FullName            string                       `json:"fullName"`
	DefaultBranch       string                       `json:"defaultBranch"`
	TargetSHA           string                       `json:"targetSha"`
	LastSHA             string                       `json:"lastSha,omitempty"`
	EName               string                       `json:"ename,omitempty"`
	RegistryEntropy     string                       `json:"registryEntropy,omitempty"`
	Namespace           string                       `json:"namespace,omitempty"`
	IdentityProvisioned bool                         `json:"identityProvisioned,omitempty"`
	ProvisioningKey     string                       `json:"provisioningPublicKey,omitempty"`
	EnvelopeID          string                       `json:"envelopeId,omitempty"`
	PlatformName        string                       `json:"platformName,omitempty"`
	ReleaseTag          string                       `json:"releaseTag,omitempty"`
	ReleaseVersion      string                       `json:"releaseVersion,omitempty"`
	AuthorENames        []string                     `json:"authorEnames,omitempty"`
	Manifest            *w3ds.PlatformManifest       `json:"manifest,omitempty"`
	Decision            *w3ds.AccreditationDecision  `json:"decision,omitempty"`
	Decisions           []w3ds.AccreditationDecision `json:"decisions,omitempty"`
	DecisionCheckedAt   time.Time                    `json:"decisionCheckedAt,omitempty"`
	Archive             bool                         `json:"archive"`
	Status              Status                       `json:"status"`
	Attempts            int                          `json:"attempts"`
	LastError           string                       `json:"lastError,omitempty"`
	NextAttempt         time.Time                    `json:"nextAttempt"`
	CreatedAt           time.Time                    `json:"createdAt"`
	UpdatedAt           time.Time                    `json:"updatedAt"`
}

type DeploymentStatus string

const (
	DeploymentAwaitingSignature DeploymentStatus = "awaiting_signature"
	DeploymentPublishing        DeploymentStatus = "publishing"
	DeploymentCompleted         DeploymentStatus = "completed"
	DeploymentFailed            DeploymentStatus = "failed"
)

type DeploymentJob struct {
	ID                        string           `json:"id"`
	RepositoryID              int64            `json:"repositoryId"`
	PlatformEName             string           `json:"platformEName"`
	DeploymentEName           string           `json:"deploymentEName"`
	VersionEName              string           `json:"versionEName"`
	DeploymentName            string           `json:"deploymentName"`
	Environment               string           `json:"environment"`
	DeployerEName             string           `json:"deployerEName"`
	Version                   string           `json:"version"`
	ReleaseTag                string           `json:"releaseTag"`
	CommitSHA                 string           `json:"commitSha"`
	PublicKey                 string           `json:"publicKey"`
	RegistryEntropy           string           `json:"registryEntropy"`
	Namespace                 string           `json:"namespace"`
	BundlePayload             string           `json:"bundlePayload"`
	WalletSignature           string           `json:"walletSignature,omitempty"`
	KeyBindingCertificate     string           `json:"keyBindingCertificate,omitempty"`
	ActivatesPlatform         bool             `json:"activatesPlatform,omitempty"`
	DeploymentKeyDocumentID   string           `json:"deploymentKeyDocumentId,omitempty"`
	SoftwareVersionDocumentID string           `json:"softwareVersionDocumentId,omitempty"`
	ProfileEnvelopeID         string           `json:"profileEnvelopeId,omitempty"`
	Status                    DeploymentStatus `json:"status"`
	Attempts                  int              `json:"attempts"`
	LastError                 string           `json:"lastError,omitempty"`
	NextAttempt               time.Time        `json:"nextAttempt"`
	CreatedAt                 time.Time        `json:"createdAt"`
	UpdatedAt                 time.Time        `json:"updatedAt"`
}

// Store persists jobs across restarts and process crashes.
type Store struct {
	db *bolt.DB
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	store := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(jobsBucket)); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte(deploymentJobsBucket))
		return err
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize state database: %w", err)
	}
	return store, nil
}

func (s *Store) GetDeployment(id string) (*DeploymentJob, error) {
	var job *DeploymentJob
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(deploymentJobsBucket)).Get([]byte(id))
		if data == nil {
			return nil
		}
		var decoded DeploymentJob
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		job = &decoded
		return nil
	})
	return job, err
}

func (s *Store) SaveDeployment(job *DeploymentJob) error {
	if job == nil || job.ID == "" {
		return errors.New("valid deployment job is required")
	}
	job.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(deploymentJobsBucket)).Put([]byte(job.ID), data)
	})
}

func (s *Store) ReadyDeployments(now time.Time, limit int) ([]*DeploymentJob, error) {
	jobs := make([]*DeploymentJob, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(deploymentJobsBucket)).ForEach(func(_, data []byte) error {
			if len(jobs) >= limit {
				return nil
			}
			var job DeploymentJob
			if err := json.Unmarshal(data, &job); err != nil {
				return err
			}
			if (job.Status == DeploymentPublishing || job.Status == DeploymentFailed) && !job.NextAttempt.After(now) {
				jobs = append(jobs, &job)
			}
			return nil
		})
	})
	return jobs, err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func jobKey(repositoryID int64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(repositoryID))
	return key
}

func (s *Store) Get(repositoryID int64) (*Job, error) {
	var job *Job
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(jobsBucket)).Get(jobKey(repositoryID))
		if data == nil {
			return nil
		}
		var decoded Job
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		job = &decoded
		return nil
	})
	return job, err
}

func (s *Store) Save(job *Job) error {
	if job == nil || job.RepositoryID <= 0 {
		return errors.New("valid platform job is required")
	}
	job.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).Put(jobKey(job.RepositoryID), data)
	})
}

func (s *Store) FindByEName(ename string, exceptRepositoryID int64) (*Job, error) {
	var found *Job
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).ForEach(func(_, data []byte) error {
			var job Job
			if err := json.Unmarshal(data, &job); err != nil {
				return err
			}
			if job.RepositoryID != exceptRepositoryID && job.EName == ename && job.Status != StatusArchived {
				copy := job
				found = &copy
			}
			return nil
		})
	})
	return found, err
}

func (s *Store) Delete(repositoryID int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).Delete(jobKey(repositoryID))
	})
}

func (s *Store) Ready(now time.Time, limit int) ([]*Job, error) {
	jobs := make([]*Job, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).ForEach(func(_, data []byte) error {
			if len(jobs) >= limit {
				return nil
			}
			var job Job
			if err := json.Unmarshal(data, &job); err != nil {
				return err
			}
			if (job.Status == StatusIdentityPending || job.Status == StatusPublishing || job.Status == StatusFailed) && !job.NextAttempt.After(now) {
				jobs = append(jobs, &job)
			}
			return nil
		})
	})
	return jobs, err
}

// Published returns active platform jobs whose version decisions should be refreshed.
func (s *Store) Published(limit int) ([]*Job, error) {
	jobs := make([]*Job, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).ForEach(func(_, data []byte) error {
			if len(jobs) >= limit {
				return nil
			}
			var job Job
			if err := json.Unmarshal(data, &job); err != nil {
				return err
			}
			if job.Status == StatusPublished && job.EName != "" && job.Manifest != nil {
				jobs = append(jobs, &job)
			}
			return nil
		})
	})
	return jobs, err
}

func (s *Store) Schedule(repositoryID int64, fullName, defaultBranch, sha string, archive bool) error {
	job, err := s.Get(repositoryID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if job == nil {
		job = &Job{RepositoryID: repositoryID, CreatedAt: now}
	}
	job.FullName = fullName
	job.DefaultBranch = defaultBranch
	job.TargetSHA = sha
	job.Archive = archive
	job.LastError = ""
	job.Attempts = 0
	job.NextAttempt = now
	if job.EName == "" {
		job.Status = StatusIdentityPending
	} else {
		job.Status = StatusPublishing
	}
	return s.Save(job)
}
