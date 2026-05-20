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

	"mini-code-runner/internal/api"
	"mini-code-runner/internal/config"
	"mini-code-runner/internal/k8s"
	"mini-code-runner/internal/runs"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting mini code runner api", "port", cfg.Port, "namespace", cfg.Namespace)

	logger.Info("creating kubernetes client")
	clientset, err := k8s.NewClientset(cfg.KubeconfigPath)
	if err != nil {
		logger.Error("failed to create kubernetes client", "err", err)
		os.Exit(1)
	}
	logger.Info("kubernetes client ready")

	logger.Info("connecting to redis", "addr", cfg.RedisAddr, "queue", cfg.RedisQueueName)
	store := runs.NewRedisStore(cfg.RedisAddr, cfg.RedisQueueName, cfg.RedisRecentRunsKey)
	if err := store.Ping(context.Background()); err != nil {
		logger.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}
	defer store.Close()
	logger.Info("redis ready")

	service := runs.NewService(clientset, store, cfg, logger)
	handler := api.NewHandler(service, logger)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("api server listening", "addr", server.Addr, "namespace", cfg.Namespace)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "err", err)
		os.Exit(1)
	}
}
