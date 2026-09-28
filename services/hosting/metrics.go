// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"time"

	"forgejo.org/models/db"
	hosting_model "forgejo.org/models/hosting"
	"forgejo.org/modules/log"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector exposes managed hosting gauges on GitW3's /metrics.
type Collector struct {
	deployments   *prometheus.Desc
	queueAge      *prometheus.Desc
	targets       *prometheus.Desc
	poolAvailable *prometheus.Desc
}

// NewCollector returns the hosting metrics collector.
func NewCollector() *Collector {
	return &Collector{
		deployments:   prometheus.NewDesc("gitw3_hosting_deployments", "Managed hosting deployments by status.", []string{"status"}, nil),
		queueAge:      prometheus.NewDesc("gitw3_hosting_build_queue_oldest_seconds", "Age of the oldest build waiting for a runner.", nil, nil),
		targets:       prometheus.NewDesc("gitw3_hosting_targets", "Managed hosting targets.", nil, nil),
		poolAvailable: prometheus.NewDesc("gitw3_hosting_domain_pool_available", "Ready pool domains.", nil, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.deployments
	ch <- c.queueAge
	ch <- c.targets
	ch <- c.poolAvailable
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := db.DefaultContext
	counts, err := hosting_model.CountDeploymentsByStatus(ctx)
	if err != nil {
		log.Error("Hosting metrics: %v", err)
		return
	}
	for status, count := range counts {
		ch <- prometheus.MustNewConstMetric(c.deployments, prometheus.GaugeValue, float64(count), string(status))
	}
	oldest := 0.0
	if jobs, err := hosting_model.ListRunningBuildJobs(ctx); err == nil {
		for _, job := range jobs {
			if job.Status == hosting_model.BuildQueued {
				oldest = max(oldest, time.Since(job.StartedUnix.AsTime()).Seconds())
			}
		}
	}
	ch <- prometheus.MustNewConstMetric(c.queueAge, prometheus.GaugeValue, oldest)
	if targets, err := hosting_model.ListAllTargets(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.targets, prometheus.GaugeValue, float64(len(targets)))
	}
	if available, err := hosting_model.CountAvailablePoolDomains(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.poolAvailable, prometheus.GaugeValue, float64(available))
	}
}
