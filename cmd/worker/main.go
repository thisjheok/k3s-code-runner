package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mini-code-runner/internal/config"
	"mini-code-runner/internal/k8s"
	"mini-code-runner/internal/runs"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting mini code runner worker", "namespace", cfg.Namespace, "queue", cfg.RedisQueueName)

	clientset, err := k8s.NewClientset(cfg.KubeconfigPath)
	if err != nil {
		logger.Error("failed to create kubernetes client", "err", err)
		os.Exit(1)
	}

	store := runs.NewRedisStore(cfg.RedisAddr, cfg.RedisQueueName, cfg.RedisRecentRunsKey)
	if err := store.Ping(context.Background()); err != nil {
		logger.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	service := runs.NewService(clientset, store, cfg, logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return
		default:
		}

		runID, err := store.DequeueRunID(ctx, 5*time.Second)
		if errors.Is(err, context.Canceled) {
			logger.Info("worker shutting down")
			return
		}
		if err != nil {
			logger.Error("failed to dequeue run", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if runID == "" {
			continue
		}

		record, err := store.GetRun(ctx, runID)
		if errors.Is(err, runs.ErrNotFound) {
			logger.Warn("queued run record no longer exists", "run_id", runID)
			continue
		}
		if err != nil {
			logger.Error("failed to load run record", "run_id", runID, "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		logger.Info("submitting run as kubernetes job", "run_id", runID, "language", record.Language)
		if err := service.SubmitRun(ctx, record); err != nil {
			logger.Error("failed to submit run", "run_id", runID, "err", err)
			continue
		}
		logger.Info("run submitted", "run_id", runID)
	}
}
