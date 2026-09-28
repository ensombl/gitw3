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
	"strings"
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
	RepositoryID         int64                        `json:"repositoryId"`
	FullName             string                       `json:"fullName"`
	DefaultBranch        string                       `json:"defaultBranch"`
	TargetSHA            string                       `json:"targetSha"`
	LastSHA              string                       `json:"lastSha,omitempty"`
	EName                string                       `json:"ename,omitempty"`
	RegistryEntropy      string                       `json:"registryEntropy,omitempty"`
	Namespace            string                       `json:"namespace,omitempty"`
	IdentityProvisioned  bool                         `json:"identityProvisioned,omitempty"`
	MigrationActivated   bool                         `json:"migrationActivated,omitempty"`
	MigrationActivatedAt time.Time                    `json:"migrationActivatedAt,omitempty"`
	ProvisioningKey      string                       `json:"provisioningPublicKey,omitempty"`
	EnvelopeID           string                       `json:"envelopeId,omitempty"`
	PlatformName         string                       `json:"platformName,omitempty"`
	ReleaseTag           string                       `json:"releaseTag,omitempty"`
	ReleaseVersion       string                       `json:"releaseVersion,omitempty"`
	AuthorENames         []string                     `json:"authorEnames,omitempty"`
	Manifest             *w3ds.PlatformManifest       `json:"manifest,omitempty"`
	Decision             *w3ds.AccreditationDecision  `json:"decision,omitempty"`
	Decisions            []w3ds.AccreditationDecision `json:"decisions,omitempty"`
	DecisionCheckedAt    time.Time                    `json:"decisionCheckedAt,omitempty"`
	Archive              bool                         `json:"archive"`
	Status               Status                       `json:"status"`
	Attempts             int                          `json:"attempts"`
	LastError            string                       `json:"lastError,omitempty"`
	LastErrorStage       string                       `json:"lastErrorStage,omitempty"`
	LastErrorCode        string                       `json:"lastErrorCode,omitempty"`
	LastErrorPath        string                       `json:"lastErrorPath,omitempty"`
	LastScheduleKey      string                       `json:"lastScheduleKey,omitempty"`
	FailureScheduleKey   string                       `json:"failureScheduleKey,omitempty"`
	NextAttempt          time.Time                    `json:"nextAttempt"`
	CreatedAt            time.Time                    `json:"createdAt"`
	UpdatedAt            time.Time                    `json:"updatedAt"`
}

type DeploymentStatus string

const (
	DeploymentAwaitingSignature DeploymentStatus = "awaiting_signature"
	DeploymentPublishing        DeploymentStatus = "publishing"
	DeploymentCompleted         DeploymentStatus = "completed"
	DeploymentFailed            DeploymentStatus = "failed"
)

type DeploymentJob struct {
	ID                    string `json:"id"`
	RepositoryID          int64  `json:"repositoryId"`
	PlatformEName         string `json:"platformEName"`
	DeploymentEName       string `json:"deploymentEName"`
	VersionEName          string `json:"versionEName"`
	DeploymentName        string `json:"deploymentName"`
	Environment           string `json:"environment"`
	DeployerEName         string `json:"deployerEName"`
	Version               string `json:"version"`
	ReleaseTag            string `json:"releaseTag"`
	CommitSHA             string `json:"commitSha"`
	PublicKey             string `json:"publicKey"`
	RegistryEntropy       string `json:"registryEntropy"`
	Namespace             string `json:"namespace"`
	BundlePayload         string `json:"bundlePayload"`
	WalletSignature       string `json:"walletSignature,omitempty"`
	KeyBindingCertificate string `json:"keyBindingCertificate,omitempty"`
	// VersionSignature is the deployment key signature over VersionPayload,
	// set when a later release is published without a new wallet signature.
	VersionSignature          string           `json:"versionSignature,omitempty"`
	VersionPayload            string           `json:"versionPayload,omitempty"`
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
			activationReady := job.Status == StatusActivating && job.MigrationActivated
			if (job.Status == StatusIdentityPending || job.Status == StatusAwaitingDeploy || job.Status == StatusPublishing || activationReady || job.Status == StatusFailed) && !job.NextAttempt.After(now) {
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
	eventKey := fmt.Sprintf("schedule:%s:%t", strings.ToLower(strings.TrimSpace(sha)), archive)
	return s.ScheduleEvent(repositoryID, fullName, defaultBranch, sha, archive, eventKey)
}

// ScheduleEvent coalesces duplicate webhook deliveries without erasing the
// failure and backoff that make a stuck publication diagnosable. A new target
// wakes the durable job immediately, while retaining the previous error until
// reconciliation either succeeds or produces a more recent one.
func (s *Store) ScheduleEvent(repositoryID int64, fullName, defaultBranch, sha string, archive bool, eventKey string) error {
	if repositoryID <= 0 || strings.TrimSpace(fullName) == "" || strings.TrimSpace(defaultBranch) == "" {
		return errors.New("valid platform schedule is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(jobsBucket))
		key := jobKey(repositoryID)
		var job *Job
		if data := bucket.Get(key); data != nil {
			var decoded Job
			if err := json.Unmarshal(data, &decoded); err != nil {
				return err
			}
			job = &decoded
		}

		now := time.Now().UTC()
		if job == nil {
			job = &Job{RepositoryID: repositoryID, CreatedAt: now, Status: StatusIdentityPending}
		} else {
			if eventKey != "" && eventKey == job.LastScheduleKey {
				return nil
			}
			if sha != "" && strings.EqualFold(sha, job.TargetSHA) && archive == job.Archive {
				return nil
			}
		}

		job.FullName = fullName
		job.DefaultBranch = defaultBranch
		if sha != "" {
			job.TargetSHA = sha
		}
		job.Archive = archive
		job.LastScheduleKey = eventKey
		job.NextAttempt = now
		if job.Status != StatusFailed {
			if job.EName == "" {
				job.Status = StatusIdentityPending
			} else {
				job.Status = StatusPublishing
			}
		}
		job.UpdatedAt = now
		data, err := json.Marshal(job)
		if err != nil {
			return err
		}
		return bucket.Put(key, data)
	})
}
