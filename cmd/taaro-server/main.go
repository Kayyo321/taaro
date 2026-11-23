package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kayyo321/taaro/internal/config"
	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/version"
	"github.com/Kayyo321/taaro/internal/webhook"
)

func main() {
	setupMode := flag.Bool("setup", false, "run the GitHub App Manifest registration wizard")
	showVersion := flag.Bool("version", false, "print taaro-server version and exit")
	envFile := flag.String("env", ".env", "path to optional .env file")
	flag.Parse()

	if *showVersion {
		fmt.Printf("taaro-server %s (protocol v%s)\n", version.Version, version.ProtocolVersion)
		return
	}

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if *setupMode {
		if !cfg.LoopbackOnly() {
			logger.Error("configure a loopback listen address and public URL for the local setup wizard")
			os.Exit(1)
		}
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

	var (
		reconciler         webhook.PullRequestReconciler
		publisher          githubapp.HandoffPublisher
		awarenessPublisher githubapp.AwarenessPublisher
	)
	if cfg.HasGitHubAppAuth() {
		if _, err := os.Stat(cfg.PrivateKeyPath); err != nil {
			logger.Warn("private key file not found; Check Run publishing disabled", "path", cfg.PrivateKeyPath, "err", err)
		} else {
			ghClient := githubapp.NewClient(cfg.AppID, cfg.PrivateKeyPath)
			publisher = ghClient
			reconciler = ghClient
			if cfg.EnableAwarenessCheck {
				awarenessPublisher = ghClient
			}
		}
	} else {
		logger.Warn("TAARO_APP_ID or TAARO_PRIVATE_KEY_PATH unset; Check Run publishing disabled")
	}

	if cfg.EnableAwarenessCheck && awarenessPublisher == nil {
		logger.Error("awareness check requires configured GitHub App credentials")
		os.Exit(1)
	}

	engine := &coordination.Engine{Store: st}

	wh := &webhook.Handler{
		Reconciler:           reconciler,
		AppID:                cfg.AppID,
		WebhookSecret:        cfg.WebhookSecret,
		PublicURL:            cfg.PublicURL,
		DumpDir:              cfg.DumpWebhooks,
		Store:                st,
		Engine:               engine,
		Publisher:            publisher,
		AwarenessPublisher:   awarenessPublisher,
		EnableAwarenessCheck: cfg.EnableAwarenessCheck,
		Logger:               logger,
	}

	srv := &server.Server{
		AppID:              cfg.AppID,
		PublicURL:          cfg.PublicURL,
		APIToken:           cfg.APIToken,
		Store:              st,
		Engine:             engine,
		WebhookHandler:     wh,
		AwarenessPublisher: awarenessPublisher,
		Logger:             logger,
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		logger.Error("cannot listen", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("starting taaro-server",
			"version", version.Version,
			"protocol", version.ProtocolVersion,
			"addr", cfg.ListenAddr,
			"app_id", cfg.AppID,
			"db", cfg.DBPath,
			"api_auth", cfg.APIToken != "",
			"awareness_check", cfg.EnableAwarenessCheck,
			"dump_webhooks", cfg.DumpWebhooks,
		)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			stop()
		}
	}()

	wh.Drain()
	retryTicker := time.NewTicker(5 * time.Second)
serviceLoop:
	for {
		select {
		case <-retryTicker.C:
			wh.Drain()
		case <-ctx.Done():
			break serviceLoop
		}
	}
	retryTicker.Stop()
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

	setupHandler := setupSrv.Handler()
	httpServer := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !config.DirectLoopbackRequest(r) {
				http.Error(w, "setup requires direct loopback access", http.StatusForbidden)
				return
			}
			setupHandler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		logger.Error("cannot listen", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("GitHub App Manifest setup wizard listening", "version", version.Version, "url", cfg.PublicURL)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("setup server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
