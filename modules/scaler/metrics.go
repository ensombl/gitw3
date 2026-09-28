// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scaler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Handler serves /metrics (Prometheus text format) and /healthz.
func (l *Loop) Handler(tickEvery time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		m := l.Metrics()
		var out strings.Builder
		gauge := func(name, help string, value float64, labels ...string) {
			fmt.Fprintf(&out, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
			if len(labels) > 0 {
				fmt.Fprintf(&out, "%s{%s} %g\n", name, strings.Join(labels, ","), value)
				return
			}
			fmt.Fprintf(&out, "%s %g\n", name, value)
		}
		gauge("gitw3_scaler_workers", "Schedulable swarm workers.", float64(m.Workers))
		gauge("gitw3_scaler_pending_tasks", "Tasks the swarm could not place.", float64(m.PendingTasks))
		gauge("gitw3_scaler_utilization", "Reserved CPU/memory share of the workers.", m.Utilization)
		atMax := 0.0
		if m.AtMax {
			atMax = 1
		}
		gauge("gitw3_scaler_at_max", "1 when more capacity is needed but MAX_WORKERS is reached.", atMax)
		gauge("gitw3_scaler_last_tick_timestamp_seconds", "Unix time of the last reconcile.", float64(m.LastTick.Unix()))
		healthy := 1.0
		if m.LastError != "" {
			healthy = 0
		}
		gauge("gitw3_scaler_last_tick_ok", "1 when the last reconcile succeeded.", healthy)
		states := make([]string, 0, len(m.States))
		for state := range m.States {
			states = append(states, string(state))
		}
		sort.Strings(states)
		fmt.Fprint(&out, "# HELP gitw3_scaler_nodes Tracked worker droplets by state.\n# TYPE gitw3_scaler_nodes gauge\n")
		for _, state := range states {
			fmt.Fprintf(&out, "gitw3_scaler_nodes{state=%q} %d\n", state, m.States[NodeState(state)])
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(out.String()))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		m := l.Metrics()
		if m.LastTick.IsZero() || time.Since(m.LastTick) > 3*tickEvery {
			http.Error(w, "scaler loop is not ticking", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
