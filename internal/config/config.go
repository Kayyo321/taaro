package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds runtime settings for taaro-server.
type Config struct {
	ListenAddr     string
	PublicURL      string
	AppID          int64
	AppSlug        string
	WebhookSecret  string
	PrivateKeyPath string
	ClientID       string
	ClientSecret   string
	SmeeURL        string
	DBPath         string
	DumpWebhooks   string
	LogLevel       slog.Level
}

// Load reads an optional .env file (without overriding already-exported env vars)
// and parses Taaro's runtime configuration.
func Load(envFile string) (Config, error) {
	if envFile != "" {
		if err := loadDotEnv(envFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("load %s: %w", envFile, err)
		}
	}

	cfg := Config{
		ListenAddr:     envOrDefault("TAARO_LISTEN_ADDR", "127.0.0.1:8080"),
		PublicURL:      strings.TrimRight(envOrDefault("TAARO_PUBLIC_URL", "http://127.0.0.1:8080"), "/"),
		AppSlug:        strings.TrimSpace(os.Getenv("TAARO_APP_SLUG")),
		WebhookSecret:  os.Getenv("TAARO_WEBHOOK_SECRET"),
		PrivateKeyPath: envOrDefault("TAARO_PRIVATE_KEY_PATH", filepath.Join(".secrets", "taaro-dev.pem")),
		ClientID:       strings.TrimSpace(os.Getenv("TAARO_CLIENT_ID")),
		ClientSecret:   os.Getenv("TAARO_CLIENT_SECRET"),
		SmeeURL:        strings.TrimSpace(os.Getenv("TAARO_SMEE_URL")),
		DBPath:         envOrDefault("TAARO_DB_PATH", filepath.Join(".data", "taaro.db")),
		DumpWebhooks:   resolveDumpDir(os.Getenv("TAARO_DUMP_WEBHOOKS")),
		LogLevel:       parseLogLevel(os.Getenv("TAARO_LOG_LEVEL")),
	}

	if rawID := strings.TrimSpace(os.Getenv("TAARO_APP_ID")); rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse TAARO_APP_ID %q: %w", rawID, err)
		}
		cfg.AppID = id
	}

	return cfg, nil
}

// ValidateServer checks that minimum webhook verification settings exist when running the server.
func (c Config) ValidateServer() error {
	if strings.TrimSpace(c.WebhookSecret) == "" {
		return errors.New("TAARO_WEBHOOK_SECRET is required (run `make setup` or set it in .env)")
	}
	return nil
}

// HasGitHubAppAuth reports whether App ID and a private key path are configured.
func (c Config) HasGitHubAppAuth() bool {
	return c.AppID > 0 && strings.TrimSpace(c.PrivateKeyPath) != ""
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func resolveDumpDir(raw string) string {
	v := strings.TrimSpace(raw)
	switch strings.ToLower(v) {
	case "", "0", "false", "no":
		return ""
	case "1", "true", "yes":
		return filepath.Join(".data", "webhooks")
	default:
		return v
	}
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return scanner.Err()
}
