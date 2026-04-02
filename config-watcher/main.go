package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"config-watcher/internal/config"
	"config-watcher/internal/watcher"
)

func main() {
	configPath := flag.String("config", "/etc/config-watcher/config.yaml", "Path to watcher configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Log.Level),
	}))

	manager := watcher.NewManager(cfg, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := manager.Start(ctx); err != nil {
		logger.Error("failed to start watchers", "error", err)
		os.Exit(1)
	}

	<-ctx.Done()
	logger.Info("shutdown signal received")
}

func parseLogLevel(raw string) slog.Level {
	switch raw {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
