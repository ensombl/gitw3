// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package platformsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Processor reconciles one repository manifest into one W3DS PlatformProfile.
type Processor struct {
	config  Config
	store   *Store
	forgejo *forgejoClient
	w3ds    *w3dsClient
}

func NewProcessor(config Config, store *Store, client *http.Client) *Processor {
	return &Processor{
		config:  config,
		store:   store,
		forgejo: newForgejoClient(config, client),
		w3ds:    newW3DSClient(config, client),
	}
}

// Reconcile processes the latest default-branch state and is safe to repeat.
func (p *Processor) Reconcile(ctx context.Context, job *Job) error {
	job.Status = StatusPublishing
	job.LastError = ""
	if err := p.store.Save(job); err != nil {
		return err
	}

	manifest, fileSHA, err := p.forgejo.manifest(ctx, job.FullName, job.DefaultBranch)
	if errors.Is(err, ErrManifestNotFound) {
		if job.EName == "" || job.Manifest == nil {
			return p.store.Delete(job.RepositoryID)
		}
		job.Archive = true
		manifest = job.Manifest
	} else if err != nil {
		return err
	}

	if err := manifest.Validate(false); err != nil {
		return fmt.Errorf("validate %s: %w", job.FullName, err)
	}
	if job.PlatformName != "" && manifest.PlatformName != job.PlatformName {
		return errors.New("platformName is immutable after first publication")
	}
	if job.EName == "" && manifest.EName != nil {
		return errors.New("an existing eName cannot be claimed through new-platform onboarding")
	}
	if job.EName != "" && manifest.EName != nil && *manifest.EName != job.EName {
		return errors.New("ename is immutable after provisioning")
	}

	if job.EName == "" {
		ename, err := p.w3ds.provision(ctx, manifest.PublicKey)
		if err != nil {
			return err
		}
		job.EName = ename
		job.PlatformName = manifest.PlatformName
		job.EnvelopeID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(p.config.ForgejoURL+fmt.Sprintf("/repositories/%d", job.RepositoryID))).String()
		if err := p.store.Save(job); err != nil {
			return err
		}
	}

	manifestChanged := false
	manifestMessage := "chore: sync platform metadata"
	if manifest.EName == nil {
		manifest.EName = &job.EName
		manifestChanged = true
	}
	if !job.Archive {
		release, err := p.forgejo.latestRelease(ctx, job.FullName)
		switch {
		case errors.Is(err, ErrReleaseNotFound):
			job.ReleaseTag = ""
			job.ReleaseVersion = ""
			job.Decision = nil
			job.DecisionCheckedAt = time.Time{}
			if manifest.InSubmission {
				manifest.InSubmission = false
				manifest.SubmissionVersion = ""
				manifestChanged = true
			}
		case err != nil:
			return err
		default:
			releaseChanged := job.ReleaseVersion != release.Version
			job.ReleaseTag = release.TagName
			job.ReleaseVersion = release.Version
			if manifest.Version != release.Version {
				manifest.Version = release.Version
				manifest.InSubmission = false
				manifest.SubmissionVersion = ""
				manifestChanged = true
			} else if manifest.InSubmission && manifest.SubmissionVersion != "" && manifest.SubmissionVersion != release.Version {
				manifest.InSubmission = false
				manifest.SubmissionVersion = ""
				manifestChanged = true
			}
			if releaseChanged {
				job.Decision = nil
				job.DecisionCheckedAt = time.Time{}
			}
			manifestMessage = "chore: sync latest platform release"
		}
	}
	if manifestChanged && !job.Archive {
		if err := p.forgejo.updateManifest(ctx, job.FullName, job.DefaultBranch, fileSHA, manifestMessage, manifest); err != nil {
			return err
		}
	}
	if job.EnvelopeID == "" {
		job.EnvelopeID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(p.config.ForgejoURL+fmt.Sprintf("/repositories/%d", job.RepositoryID))).String()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if !job.Archive {
		ref := job.TargetSHA
		if ref == "" {
			ref = job.DefaultBranch
		}
		authorENames, err := p.forgejo.authorENames(ctx, job.FullName, ref)
		if err != nil {
			return err
		}
		job.AuthorENames = authorENames
	}
	if err := p.w3ds.publish(ctx, job.EnvelopeID, manifest, job.CreatedAt, job.Archive, job.AuthorENames); err != nil {
		return err
	}

	job.Manifest = manifest
	job.PlatformName = manifest.PlatformName
	job.LastSHA = job.TargetSHA
	job.Attempts = 0
	job.LastError = ""
	job.NextAttempt = time.Time{}
	if job.Archive {
		job.Status = StatusArchived
	} else {
		job.Status = StatusPublished
	}
	return p.store.Save(job)
}

// RefreshAccreditation reads the decision for the currently published version from the platform eVault.
func (p *Processor) RefreshAccreditation(ctx context.Context, job *Job) error {
	if job == nil || job.EName == "" || job.Manifest == nil || job.Status != StatusPublished {
		return nil
	}
	if job.ReleaseVersion == "" || job.Manifest.Version != job.ReleaseVersion {
		return nil
	}
	decision, err := p.w3ds.accreditation(ctx, job.EName, job.ReleaseVersion)
	if err != nil {
		return err
	}
	job.Decision = decision
	job.DecisionCheckedAt = time.Now().UTC()
	return p.store.Save(job)
}

// Worker drains durable jobs until the context is cancelled.
type Worker struct {
	store               *Store
	processor           *Processor
	period              time.Duration
	accreditationPeriod time.Duration
}

func NewWorker(store *Store, processor *Processor, period, accreditationPeriod time.Duration) *Worker {
	return &Worker{store: store, processor: processor, period: period, accreditationPeriod: accreditationPeriod}
}

func (w *Worker) Run(ctx context.Context) {
	w.reconcileReady(ctx)
	w.refreshAccreditations(ctx)
	reconcileTicker := time.NewTicker(w.period)
	defer reconcileTicker.Stop()
	accreditationTicker := time.NewTicker(w.accreditationPeriod)
	defer accreditationTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileTicker.C:
			w.reconcileReady(ctx)
		case <-accreditationTicker.C:
			w.refreshAccreditations(ctx)
		}
	}
}

func (w *Worker) reconcileReady(ctx context.Context) {
	jobs, err := w.store.Ready(time.Now().UTC(), 50)
	if err != nil {
		slog.Error("load platform publication jobs", "error", err)
		return
	}
	for _, job := range jobs {
		if err := w.processor.Reconcile(ctx, job); err != nil {
			job.Attempts++
			job.Status = StatusFailed
			job.LastError = err.Error()
			delay := time.Duration(math.Min(math.Pow(2, float64(job.Attempts)), 300)) * time.Second
			job.NextAttempt = time.Now().UTC().Add(delay)
			if saveErr := w.store.Save(job); saveErr != nil {
				slog.Error("save failed platform publication", "repository_id", job.RepositoryID, "error", saveErr)
			}
			slog.Warn("platform publication will retry", "repository_id", job.RepositoryID, "attempt", job.Attempts, "error", err)
		}
	}
}

func (w *Worker) refreshAccreditations(ctx context.Context) {
	jobs, err := w.store.Published(100)
	if err != nil {
		slog.Error("load published platforms for PPA refresh", "error", err)
		return
	}
	for _, job := range jobs {
		if err := w.processor.RefreshAccreditation(ctx, job); err != nil {
			slog.Warn("refresh platform PPA decision", "repository_id", job.RepositoryID, "version", job.Manifest.Version, "error", err)
		}
	}
}
