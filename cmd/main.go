package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"

	"github.com/ccvass/swarmex/swarmex-remediation"
)

func main() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer cli.Close()

	rem := remediation.New(cli, logger)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		})
		logger.Info("health endpoint", "addr", ":8080")
		http.ListenAndServe(":8080", nil)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("swarmex-remediation starting")

	go rem.Cleanup(ctx, 1*time.Minute)

	msgCh, errCh := cli.Events(ctx, events.ListOptions{})
	logger.Info("event stream connected")
	for {
		select {
		case event := <-msgCh:
			if event.Type == events.ContainerEventType {
				logger.Debug("container event", "action", event.Action, "service", event.Actor.Attributes["com.docker.swarm.service.name"])
			}
			rem.HandleEvent(ctx, event)
		case err := <-errCh:
			if ctx.Err() != nil {
				logger.Info("shutdown complete")
				return
			}
			logger.Error("event stream error", "error", err)
			return
		case <-ctx.Done():
			logger.Info("shutdown complete")
			return
		}
	}
}
