// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"forgejo.org/services/platformsync"
)

func main() {
	config, err := platformsync.ConfigFromEnv()
	if err != nil {
		slog.Error("invalid platform manifest sync configuration", "error", err)
		os.Exit(1)
	}
	store, err := platformsync.OpenStore(config.StatePath)
	if err != nil {
		slog.Error("open platform manifest sync state", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	httpClient := &http.Client{Timeout: config.RequestTimeout}
	processor := platformsync.NewProcessor(config, store, httpClient)
	worker := platformsync.NewWorker(store, processor, config.ReconcilePeriod)
	server := platformsync.NewServer(config, store)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go worker.Run(ctx)

	httpServer := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("shut down platform manifest sync", "error", err)
		}
	}()

	slog.Info("platform manifest sync listening", "address", config.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve platform manifest sync", "error", err)
		os.Exit(1)
	}
}
