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
	"strings"
	"time"

	"forgejo.org/modules/w3ds"

	"github.com/google/uuid"
)

// Processor reconciles one repository manifest into one W3DS PlatformProfile.
type Processor struct {
	config  Config
	store   *Store
	forgejo *forgejoClient
	w3ds    *w3dsClient
}

type PrepareDeploymentRequest struct {
	ID             string `json:"id"`
	RepositoryID   int64  `json:"repositoryId"`
	PlatformEName  string `json:"platformEName"`
	DeploymentName string `json:"deploymentName"`
	Environment    string `json:"environment"`
	DeployerEName  string `json:"deployerEName"`
	Version        string `json:"version"`
	ReleaseTag     string `json:"releaseTag"`
	CommitSHA      string `json:"commitSha"`
	PublicKey      string `json:"publicKey"`
}

type FinalizeDeploymentRequest struct {
	SignerEName           string `json:"signerEName"`
	Signature             string `json:"signature"`
	KeyBindingCertificate string `json:"keyBindingCertificate"`
}

type BootstrapPlatformRequest struct {
	RepositoryID  int64  `json:"repositoryId"`
	FullName      string `json:"fullName"`
	DefaultBranch string `json:"defaultBranch"`
	PublicKey     string `json:"publicKey"`
}

func (p *Processor) PrepareDeployment(ctx context.Context, input PrepareDeploymentRequest) (*DeploymentJob, error) {
	if input.ID == "" || input.RepositoryID <= 0 || input.PlatformEName == "" || input.DeploymentName == "" ||
		input.Environment == "" || input.DeployerEName == "" || input.Version == "" || input.ReleaseTag == "" ||
		input.CommitSHA == "" || input.PublicKey == "" {
		return nil, errors.New("complete deployment details are required")
	}
	if existing, err := p.store.GetDeployment(input.ID); err != nil || existing != nil {
		return existing, err
	}
	platformID, err := uuid.Parse(strings.TrimPrefix(input.PlatformEName, "@"))
	if err != nil {
		return nil, errors.New("platform eName must contain a UUID")
	}
	identity, err := p.w3ds.prepareIdentity(ctx)
	if err != nil {
		return nil, err
	}
	versionEName := "@" + uuid.NewSHA1(platformID, []byte("software-version:"+input.Version)).String()
	_, _, bundle, err := w3ds.BuildDeploymentAttestation(
		identity.EName, input.DeploymentName, input.Environment, input.DeployerEName,
		input.PlatformEName, versionEName, input.Version, input.ReleaseTag, input.CommitSHA, input.PublicKey,
	)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	job := &DeploymentJob{
		ID: input.ID, RepositoryID: input.RepositoryID, PlatformEName: input.PlatformEName,
		DeploymentEName: identity.EName, VersionEName: versionEName,
		DeploymentName: input.DeploymentName, Environment: input.Environment, DeployerEName: input.DeployerEName,
		Version: input.Version, ReleaseTag: input.ReleaseTag, CommitSHA: strings.ToLower(input.CommitSHA),
		PublicKey: input.PublicKey, RegistryEntropy: identity.RegistryEntropy, Namespace: identity.Namespace,
		BundlePayload: bundle, Status: DeploymentAwaitingSignature, CreatedAt: now,
	}
	if err := p.store.SaveDeployment(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (p *Processor) FinalizeDeployment(input FinalizeDeploymentRequest, job *DeploymentJob) error {
	if job == nil {
		return errors.New("deployment is not awaiting a signature")
	}
	if (job.Status == DeploymentPublishing || job.Status == DeploymentCompleted || job.Status == DeploymentFailed) &&
		job.DeployerEName == input.SignerEName && job.WalletSignature == input.Signature {
		return nil
	}
	if job.Status != DeploymentAwaitingSignature {
		return errors.New("deployment is not awaiting a signature")
	}
	if input.SignerEName != job.DeployerEName || input.Signature == "" || input.KeyBindingCertificate == "" {
		return errors.New("deployment signature does not match its deployer")
	}
	job.WalletSignature = input.Signature
	job.KeyBindingCertificate = input.KeyBindingCertificate
	job.Status = DeploymentPublishing
	job.Attempts = 0
	job.LastError = ""
	job.NextAttempt = time.Now().UTC()
	return p.store.SaveDeployment(job)
}

func (p *Processor) ReconcileDeployment(ctx context.Context, job *DeploymentJob) error {
	if job == nil || job.WalletSignature == "" {
		return errors.New("signed deployment job is required")
	}
	job.Status = DeploymentPublishing
	job.LastError = ""
	if err := p.store.SaveDeployment(job); err != nil {
		return err
	}
	if err := p.w3ds.publishDeployment(ctx, job); err != nil {
		return err
	}
	job.Status = DeploymentCompleted
	job.Attempts = 0
	job.LastError = ""
	job.RegistryEntropy = ""
	job.NextAttempt = time.Time{}
	return p.store.SaveDeployment(job)
}

func NewProcessor(config Config, store *Store, client *http.Client) *Processor {
	return &Processor{
		config:  config,
		store:   store,
		forgejo: newForgejoClient(config, client),
		w3ds:    newW3DSClient(config, client),
	}
}

// BootstrapPlatformIdentity activates a new platform with the key selected for its first deployment.
func (p *Processor) BootstrapPlatformIdentity(ctx context.Context, input BootstrapPlatformRequest) (*Job, error) {
	if input.RepositoryID <= 0 || strings.TrimSpace(input.FullName) == "" || strings.TrimSpace(input.DefaultBranch) == "" ||
		!strings.HasPrefix(input.PublicKey, "z") || len(input.PublicKey) > 8192 {
		return nil, errors.New("complete platform identity details are required")
	}
	job, err := p.store.Get(input.RepositoryID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		if err := p.store.Schedule(input.RepositoryID, input.FullName, input.DefaultBranch, "", false); err != nil {
			return nil, err
		}
		job, err = p.store.Get(input.RepositoryID)
		if err != nil {
			return nil, err
		}
	}
	if job == nil {
		return nil, errors.New("platform publication job was not created")
	}
	if job.ProvisioningKey != "" && job.ProvisioningKey != input.PublicKey {
		return nil, errors.New("platform identity is already being activated with another key")
	}
	if job.EName != "" && job.Status == StatusPublished {
		return job, nil
	}
	job.FullName = input.FullName
	job.DefaultBranch = input.DefaultBranch
	job.ProvisioningKey = input.PublicKey
	job.Status = StatusIdentityPending
	job.LastError = ""
	job.Attempts = 0
	job.NextAttempt = time.Now().UTC()
	if err := p.store.Save(job); err != nil {
		return nil, err
	}
	if err := p.Reconcile(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
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

	manifestChanged := false
	manifestMessage := "chore: sync platform metadata"
	provisioningKey := manifest.PublicKey
	if provisioningKey == "" {
		provisioningKey = job.ProvisioningKey
	}
	if job.EName == "" && provisioningKey == "" {
		job.Manifest = manifest
		job.PlatformName = manifest.PlatformName
		job.Status = StatusAwaitingDeploy
		job.Attempts = 0
		job.LastError = ""
		job.NextAttempt = time.Time{}
		return p.store.Save(job)
	}
	if job.EName == "" {
		ename, err := p.w3ds.provision(ctx, provisioningKey)
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
	if manifest.PublicKey == "" {
		manifest.PublicKey = provisioningKey
		manifestChanged = true
	}
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
			job.Decisions = nil
			job.DecisionCheckedAt = time.Time{}
			if manifest.InSubmission {
				manifest.InSubmission = false
				manifest.SubmissionVersion = ""
				manifest.SubmissionProof = nil
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
				manifest.SubmissionProof = nil
				manifestChanged = true
			} else if manifest.InSubmission && manifest.SubmissionVersion != "" && manifest.SubmissionVersion != release.Version {
				manifest.InSubmission = false
				manifest.SubmissionVersion = ""
				manifest.SubmissionProof = nil
				manifestChanged = true
			}
			if releaseChanged {
				job.Decision = nil
				job.Decisions = nil
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
	job.ProvisioningKey = ""
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
	decisions, err := p.w3ds.accreditations(ctx, job.EName, job.ReleaseVersion)
	if err != nil {
		return err
	}
	job.Decisions = decisions
	job.Decision = nil
	if len(decisions) > 0 {
		job.Decision = &job.Decisions[len(job.Decisions)-1]
	}
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
	w.reconcileDeployments(ctx)
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
			w.reconcileDeployments(ctx)
		case <-accreditationTicker.C:
			w.refreshAccreditations(ctx)
		}
	}
}

func (w *Worker) reconcileDeployments(ctx context.Context) {
	jobs, err := w.store.ReadyDeployments(time.Now().UTC(), 50)
	if err != nil {
		slog.Error("load deployment jobs", "error", err)
		return
	}
	for _, job := range jobs {
		if err := w.processor.ReconcileDeployment(ctx, job); err != nil {
			job.Attempts++
			job.Status = DeploymentFailed
			job.LastError = err.Error()
			delay := time.Duration(math.Min(math.Pow(2, float64(job.Attempts)), 300)) * time.Second
			job.NextAttempt = time.Now().UTC().Add(delay)
			if saveErr := w.store.SaveDeployment(job); saveErr != nil {
				slog.Error("save failed deployment", "deployment_id", job.ID, "error", saveErr)
			}
			slog.Warn("deployment publication will retry", "deployment_id", job.ID, "attempt", job.Attempts, "error", err)
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
