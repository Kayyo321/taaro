package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kayyo321/taaro/internal/config"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

func main() {
	setupMode := flag.Bool("setup", false, "run the GitHub App Manifest registration wizard")
	envFile := flag.String("env", ".env", "path to optional .env file")
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if *setupMode {
		runSetupWizard(cfg, *envFile, logger)
		return
	}

	if err := cfg.ValidateServer(); err != nil {
		logger.Error("invalid server configuration", "err", err)
		os.Exit(1)
	}

	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		logger.Error("failed opening sqlite store", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	var publisher githubapp.HandoffPublisher
	if cfg.HasGitHubAppAuth() {
		if _, err := os.Stat(cfg.PrivateKeyPath); err != nil {
			logger.Warn("private key file not found; Check Run publishing disabled", "path", cfg.PrivateKeyPath, "err", err)
		} else {
			publisher = githubapp.NewClient(cfg.AppID, cfg.PrivateKeyPath)
		}
	} else {
		logger.Warn("TAARO_APP_ID or TAARO_PRIVATE_KEY_PATH unset; Check Run publishing disabled")
	}

	wh := &webhook.Handler{
		AppID:         cfg.AppID,
		WebhookSecret: cfg.WebhookSecret,
		PublicURL:     cfg.PublicURL,
		DumpDir:       cfg.DumpWebhooks,
		Store:         st,
		Publisher:     publisher,
		Logger:        logger,
	}

	srv := &server.Server{
		AppID:          cfg.AppID,
		Store:          st,
		WebhookHandler: wh,
		Logger:         logger,
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("starting taaro-server",
			"addr", cfg.ListenAddr,
			"app_id", cfg.AppID,
			"db", cfg.DBPath,
			"dump_webhooks", cfg.DumpWebhooks,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down taaro-server")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
	wh.Wait()
	logger.Info("taaro-server stopped cleanly")
}

func runSetupWizard(cfg config.Config, envPath string, logger *slog.Logger) {
	setupSrv := &githubapp.SetupServer{
		BaseURL:        cfg.PublicURL,
		EnvPath:        envPath,
		PrivateKeyPath: cfg.PrivateKeyPath,
		DefaultSmeeURL: cfg.SmeeURL,
		Logger:         logger,
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           setupSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("GitHub App Manifest setup wizard listening", "url", cfg.PublicURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("setup server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
