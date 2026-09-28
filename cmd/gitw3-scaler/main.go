// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// gitw3-scaler adds and removes Docker Swarm workers on DigitalOcean for
// GitW3 managed hosting. It runs as a swarm service pinned to a manager and
// reaches Docker only through a restricted socket proxy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forgejo.org/modules/scaler"
)

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func required(key string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value, nil
	}
	return "", fmt.Errorf("%s is required", key)
}

func envSecret(key string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(key + "_FILE")); path != "" {
		data, err := os.ReadFile(path)
		return strings.TrimSpace(string(data)), err
	}
	return required(key)
}

func envInt(key string, fallback int) (int, error) {
	return strconv.Atoi(env(key, strconv.Itoa(fallback)))
}

func envFloat(key string, fallback float64) (float64, error) {
	return strconv.ParseFloat(env(key, strconv.FormatFloat(fallback, 'f', -1, 64)), 64)
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	return time.ParseDuration(env(key, fallback.String()))
}

type settings struct {
	config     scaler.Config
	doToken    string
	dockerHost string
	statePath  string
	listenAddr string
	tick       time.Duration
}

func load() (*settings, error) {
	var errs []error
	check := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	s := &settings{
		dockerHost: env("DOCKER_HOST", "tcp://scaler-proxy:2375"),
		statePath:  env("STATE_PATH", "/data/scaler.db"),
		listenAddr: env("LISTEN_ADDR", ":9180"),
	}
	var err error
	s.doToken, err = envSecret("DO_TOKEN")
	check(err)
	policy := scaler.Policy{}
	policy.MinWorkers, err = envInt("MIN_WORKERS", 1)
	check(err)
	policy.MaxWorkers, err = envInt("MAX_WORKERS", 5)
	check(err)
	policy.CPUHigh, err = envFloat("CPU_HIGH", 0.75)
	check(err)
	policy.CPULow, err = envFloat("CPU_LOW", 0.30)
	check(err)
	policy.CooldownUp, err = envDuration("COOLDOWN_UP", 5*time.Minute)
	check(err)
	policy.CooldownDown, err = envDuration("COOLDOWN_DOWN", 15*time.Minute)
	check(err)
	policy.LowSustain, err = envDuration("LOW_SUSTAIN", 20*time.Minute)
	check(err)
	s.tick, err = envDuration("TICK", 30*time.Second)
	check(err)
	joinTimeout, err := envDuration("JOIN_TIMEOUT", 10*time.Minute)
	check(err)
	rotation, err := envDuration("TOKEN_ROTATION", 7*24*time.Hour)
	check(err)
	manager, err := required("MANAGER_PRIVATE_IP")
	check(err)
	vpc, err := required("VPC_UUID")
	check(err)
	if policy.MinWorkers < 0 || policy.MaxWorkers < policy.MinWorkers {
		errs = append(errs, errors.New("need 0 <= MIN_WORKERS <= MAX_WORKERS"))
	}
	if policy.CPULow >= policy.CPUHigh {
		errs = append(errs, errors.New("CPU_LOW must be below CPU_HIGH"))
	}
	var sshKeys []string
	for _, key := range strings.Split(env("SSH_KEYS", ""), ",") {
		if key = strings.TrimSpace(key); key != "" {
			sshKeys = append(sshKeys, key)
		}
	}
	s.config = scaler.Config{
		Policy: policy,
		Droplet: scaler.DropletSpec{
			Region: env("DO_REGION", "ams3"), Size: env("DO_SIZE", "s-2vcpu-4gb"),
			Image: env("DO_IMAGE", "ubuntu-24-04-x64"), VPCUUID: vpc, SSHKeys: sshKeys,
		},
		WorkerTag: env("WORKER_TAG", "gitw3-worker"), ManagerAddr: manager,
		NamePrefix: env("NAME_PREFIX", "gitw3"), JoinTimeout: joinTimeout, TokenRotation: rotation,
	}
	return s, errors.Join(errs...)
}

// healthcheck is the container HEALTHCHECK: it asks the running scaler
// whether its loop is still ticking.
func healthcheck() int {
	addr := env("LISTEN_ADDR", ":9180")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	s, err := load()
	if err != nil {
		slog.Error("invalid scaler configuration", "error", err)
		os.Exit(1)
	}
	store, err := scaler.OpenNodeStore(s.statePath)
	if err != nil {
		slog.Error("open scaler state", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	swarm, err := scaler.NewDockerSwarm(s.dockerHost, 20*time.Second)
	if err != nil {
		slog.Error("docker client", "error", err)
		os.Exit(1)
	}
	loop := scaler.NewLoop(s.config, store, swarm, scaler.NewDigitalOcean(s.doToken, 30*time.Second))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go loop.Run(ctx, s.tick)

	server := &http.Server{Addr: s.listenAddr, Handler: loop.Handler(s.tick), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("gitw3 scaler running", "workers", fmt.Sprintf("%d-%d", s.config.Policy.MinWorkers, s.config.Policy.MaxWorkers), "listen", s.listenAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve metrics", "error", err)
		os.Exit(1)
	}
}
