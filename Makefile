# Read only forwarding settings as data. The server loads its own credentials.
dotenv = $(shell awk -F= '$$1 == "$(1)" {sub(/^[^=]*=/, ""); print; exit}' .env 2>/dev/null)
TAARO_SMEE_URL ?= $(call dotenv,TAARO_SMEE_URL)
TAARO_LISTEN_ADDR ?= $(call dotenv,TAARO_LISTEN_ADDR)
export TAARO_SMEE_URL

BIN_DIR := bin
SERVER_BIN := $(BIN_DIR)/taaro-server
CLI_BIN := $(BIN_DIR)/taaro
LISTEN_ADDR ?= $(or $(TAARO_LISTEN_ADDR),127.0.0.1:8080)
TARGET_URL ?= http://$(LISTEN_ADDR)/webhooks/github

.PHONY: help install-tools setup dev server smee tunnel build test tidy clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

install-tools: ## Install air for live-reloading Go server builds
	go install github.com/air-verse/air@latest

setup: ## Launch one-click GitHub App Manifest registration wizard
	go run ./cmd/taaro-server -setup

dev: ## Run taaro-server with live-reload via air
	@command -v air >/dev/null 2>&1 || { echo "air is not installed; run 'make install-tools' or use 'make server'"; exit 1; }
	air -c .air.toml

server: ## Run taaro-server directly without air
	go run ./cmd/taaro-server

smee: ## Forward webhooks from TAARO_SMEE_URL to the local server
	@test -n "$$TAARO_SMEE_URL" || { echo "Set TAARO_SMEE_URL in .env (create one at https://smee.io/new)"; exit 1; }
	npx --yes smee-client --url "$$TAARO_SMEE_URL" --target "$(TARGET_URL)"

tunnel: ## Expose local server via cloudflared (for clickable details_url in a public sandbox)
	cloudflared tunnel --url "http://$(LISTEN_ADDR)"

build: ## Build server and CLI binaries into ./bin
	@mkdir -p $(BIN_DIR)
	go build -o $(SERVER_BIN) ./cmd/taaro-server
	go build -o $(CLI_BIN) ./cmd/taaro

test: ## Run all Go tests with race detector
	go test -race ./...

tidy: ## Clean up go.mod and go.sum
	go mod tidy

clean: ## Remove build artifacts (keeps .env, .secrets, and .data)
	rm -rf $(BIN_DIR) tmp build-errors.log
