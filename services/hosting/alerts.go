// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	hosting_model "forgejo.org/models/hosting"
	packages_model "forgejo.org/models/packages"
	system_model "forgejo.org/models/system"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
)

// alertInterval is both how often alerts are checked and how often the
// same alert may repeat.
const alertInterval = 5 * time.Minute

var alertMemory = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// raise creates an admin notice unless the same alert fired recently.
func raise(ctx context.Context, key, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	alertMemory.Lock()
	last, seen := alertMemory.last[key]
	if seen && time.Since(last) < time.Hour {
		alertMemory.Unlock()
		return
	}
	alertMemory.last[key] = time.Now()
	alertMemory.Unlock()
	log.Warn("Hosting alert: %s", message)
	if err := system_model.CreateNotice(ctx, system_model.NoticeTask, "Managed hosting: "+message); err != nil {
		log.Error("Create hosting notice: %v", err)
	}
}

// CheckAlerts raises admin notices for the conditions operators must act
// on: a stuck build queue, failed deploys, the swarm at max size, a full
// registry, and an unreachable manager.
func CheckAlerts(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	jobs, err := hosting_model.ListRunningBuildJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Status == hosting_model.BuildQueued && time.Since(job.StartedUnix.AsTime()) > setting.Hosting.BuildQueueAlert {
			raise(ctx, "build-queue", "build job %d has waited %s for a runner; check the build node", job.ID, time.Since(job.StartedUnix.AsTime()).Round(time.Minute))
			break
		}
	}

	failed, err := hosting_model.ListDeploymentsFailedSince(ctx, timeutil.TimeStamp(time.Now().Add(-alertInterval).Unix()))
	if err != nil {
		return err
	}
	for _, deployment := range failed {
		raise(ctx, fmt.Sprintf("failed-%d", deployment.ID), "deployment %d (%s) ended as %s: %s", deployment.ID, deployment.TagName, deployment.Status, truncate(deployment.Error, 300))
	}

	if setting.Hosting.ScalerMetricsURL != "" {
		metrics, err := scrapeScaler(ctx)
		switch {
		case err != nil:
			raise(ctx, "scaler-down", "the autoscaler metrics endpoint is unreachable: %v", err)
		case metrics["gitw3_scaler_at_max"] == 1:
			raise(ctx, "scaler-max", "the swarm needs more capacity but the autoscaler reached MAX_WORKERS (%v workers, %v pending tasks)",
				metrics["gitw3_scaler_workers"], metrics["gitw3_scaler_pending_tasks"])
		case metrics["gitw3_scaler_last_tick_ok"] == 0:
			raise(ctx, "scaler-error", "the autoscaler's last reconcile failed; check its logs")
		}
	}

	if setting.Hosting.RegistryAlertBytes > 0 {
		if owner, err := user_model.GetUserByName(ctx, setting.Hosting.RegistryOwner); err == nil {
			size, err := packages_model.CalculateFileSize(ctx, &packages_model.PackageFileSearchOptions{OwnerID: owner.ID})
			if err == nil && size > setting.Hosting.RegistryAlertBytes {
				raise(ctx, "registry-size", "the deployments registry uses %d MiB (alert threshold %d MiB)", size>>20, setting.Hosting.RegistryAlertBytes>>20)
			}
		}
	}

	if err := probe(ctx, setting.Hosting.DokployURL); err != nil {
		raise(ctx, "manager-dokploy", "Dokploy on the manager is unreachable: %v; running apps keep serving but deploys and scaling stop", err)
	}
	if setting.Hosting.SwarmProxyURL != "" {
		if err := probe(ctx, setting.Hosting.SwarmProxyURL+"/_ping"); err != nil {
			raise(ctx, "manager-swarm", "the swarm manager's Docker API is unreachable: %v", err)
		}
	}
	return nil
}

func probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

// scrapeScaler reads the unlabelled gauges of the scaler's /metrics.
func scrapeScaler(ctx context.Context) (map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, setting.Hosting.ScalerMetricsURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return parseMetrics(io.LimitReader(response.Body, 1<<20)), nil
}

func parseMetrics(reader io.Reader) map[string]float64 {
	metrics := map[string]float64{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "{") {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		var number float64
		if _, err := fmt.Sscanf(value, "%g", &number); err == nil {
			metrics[name] = number
		}
	}
	return metrics
}
