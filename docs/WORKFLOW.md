# Taaro Go & GitHub App Development Workflow

This guide covers registering a local Taaro GitHub App, running the Go server and CLI locally, forwarding webhooks, and debugging or testing changes against the contract in [PROJECT.md](PROJECT.md).

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/taaro-server` | HTTP server binary hosting the GitHub App webhook endpoint, Manifest setup wizard (`-setup`), handoff pages, and coordination API. |
| `cmd/taaro` | Standalone CLI binary (`taaro health`, `taaro context`). |
| `internal/config` | Loads `.env` (without overriding exported variables) and parses runtime configuration. |
| `internal/store` | `Store` interface and embedded pure-Go SQLite implementation (`modernc.org/sqlite`) for delivery deduplication and observed PR records. |
| `internal/githubapp` | GitHub App Manifest setup wizard and installation-authenticated GitHub client (`ghinstallation/v2` + `go-github/v88`). |
| `internal/webhook` | `POST /webhooks/github` handler: HMAC-SHA256 verification, optional payload capture, self-`check_run` filtering, SQLite deduplication, `202 Accepted` acknowledgment, and async Check Run publishing. |
| `internal/server` | Stdlib `net/http` router wiring `/healthz`, `/webhooks/github`, `/api/v1/context`, and `/handoffs/{owner}/{repo}/pull/{number}`. |
| `Makefile` & `.air.toml` | Local dev tasks and live-reload configuration for `cmd/taaro-server`. |

## Prerequisites

- **Go** (`go version` matching `go.mod`, no CGO required)
- **Make** (`make help` lists all targets)
- **Node/npx** (used by `make smee` to run `smee-client`)
- **Cloudflared** (optional; used by `make tunnel` when testing clickable Check Run `details_url` links or OAuth callbacks)
- **Air** (optional; install once for live-reloading `taaro-server`):

```bash
make install-tools
```

## 1. Create and install a local GitHub App

Taaro includes a built-in **GitHub App Manifest** wizard so every developer registers a private dev App with the exact permissions and webhook events specified in [Trust, Security, and Operational Reliability](PROJECT.md#7-trust-security-and-operational-reliability).

1. Open `https://smee.io/new` in a browser to create a persistent webhook proxy channel and copy the URL (for example, `https://smee.io/AbCdEf123`).
2. Start the setup wizard:
   ```bash
   make setup
   ```
3. Open `http://127.0.0.1:8080`, enter a unique App name (such as `taaro-dev-<yourhandle>`), paste your Smee URL, optionally check `Contents: read`, and submit **Register GitHub App**.
4. Confirm creation on GitHub. GitHub redirects back to `http://127.0.0.1:8080/setup/callback?code=...`.
5. `taaro-server` exchanges the code via GitHub's manifest conversion API and writes two gitignored files with `0600` permissions:
   - `.env` (`TAARO_APP_ID`, `TAARO_APP_SLUG`, `TAARO_WEBHOOK_SECRET`, `TAARO_CLIENT_ID`, `TAARO_CLIENT_SECRET`, `TAARO_SMEE_URL`, `TAARO_PRIVATE_KEY_PATH`)
   - `.secrets/taaro-dev.pem` (the App's RSA private key)
6. Click the **Install the App on a test repository** link shown on the completion page and select a dedicated sandbox repository.
7. Stop the setup server with `Ctrl+C`.

> **Permission updates:** If you later add permissions in GitHub App settings, existing installations do not receive them automatically. Accept the pending permission prompt on the installation's settings page before testing the new API calls.

### Registered manifest permissions and events

| Permission | Access | Purpose |
| --- | --- | --- |
| `metadata` | `read` | Identify repositories and route events. |
| `pull_requests` | `read` | Read PR state, diffs, threads, and head SHAs. |
| `issues` | `read` | Resolve linked goals, labels, and proposal issues. |
| `checks` | `write` | Read check verdicts and publish `Taaro Handoff` / awareness Check Runs. |
| `contents` | `read` *(optional)* | Read versioned RFC files when enabled. |

Subscribed events: `pull_request`, `pull_request_review`, `issues`, `check_run`, `check_suite`, `merge_group` (GitHub sends `installation` and `installation_repositories` automatically).

## 2. Daily local development loop

Run the server and webhook forwarder in two terminals:

**Terminal 1 — Server (live reload via Air):**

```bash
make dev
# Or without Air:
make server
```

**Terminal 2 — Webhook forwarder:**

```bash
make smee
```

`make smee` forwards deliveries from `TAARO_SMEE_URL` to `http://127.0.0.1:8080/webhooks/github`. Because your Smee channel URL does not change across restarts, you do not need to edit GitHub App settings between sessions.

### Triggering and inspecting a handoff end-to-end

1. Open or push a commit to a pull request in your sandbox repository.
2. `taaro-server` verifies `X-Hub-Signature-256`, deduplicates `X-GitHub-Delivery` in `.data/taaro.db`, responds `202 Accepted`, records the PR observation timestamp, and publishes a completed `neutral` Check Run named **Taaro Handoff** pinned to the PR's `head_sha`.
3. Query the observed PR state from the CLI:
   ```bash
   go run ./cmd/taaro health
   go run ./cmd/taaro context --repo <owner>/<repo> --pr <number>
   ```

### Testing clickable `details_url` links or OAuth callbacks

`smee.io` only proxies webhook POSTs. When you need public HTTPS ingress so GitHub Check Run `details_url` links open your local `/handoffs/...` page (or for OAuth callbacks):

1. Start a Cloudflare quick tunnel:
   ```bash
   make tunnel
   ```
2. Copy the printed `https://<random>.trycloudflare.com` URL into `TAARO_PUBLIC_URL` in `.env` and restart `taaro-server`.
   - `taaro-server` attaches `details_url` to published Check Runs whenever `TAARO_PUBLIC_URL` begins with `https://`.
   - Keep `TAARO_SMEE_URL` for webhooks, or temporarily point the GitHub App's Webhook URL to `https://<random>.trycloudflare.com/webhooks/github`.

## 3. Debugging and testing

### Offline test suite and webhook fixture harness

Run the full test suite with the race detector:

```bash
make test
```

`internal/webhook/handler_test.go` signs JSON payloads from `internal/webhook/testdata/` with HMAC-SHA256 and verifies:

- Valid signature handling, `202 Accepted` responses, and async Check Run publishing.
- Invalid or missing `X-Hub-Signature-256` rejection (`401 Unauthorized`).
- Atomic `X-GitHub-Delivery` deduplication in SQLite (`200 OK` duplicate response without republishing).
- Updating the existing Check Run when a second event arrives for the same `head_sha`, while preserving separate Check Runs across different SHAs.
- Self-originated `check_run` loop suppression (`check_run.app.id == TAARO_APP_ID`).
- Local payload dumping when `TAARO_DUMP_WEBHOOKS` is enabled.

### Capturing live webhooks into regression fixtures

1. Set `TAARO_DUMP_WEBHOOKS=1` in `.env` (or pass a custom directory path).
2. Trigger the event in your sandbox repository. Verified payloads are written to `.data/webhooks/<timestamp>_<event>_<delivery_id>.json` (gitignored).
3. Extract the `payload` object, scrub any sensitive fields, save it under `internal/webhook/testdata/`, and add a test case in `internal/webhook/handler_test.go`.

### Replaying live events from GitHub

In **GitHub → Settings → Developer settings → GitHub Apps → *Your App* → Advanced → Recent Deliveries**:

- Inspect exact request headers (`X-GitHub-Event`, `X-GitHub-Delivery`, `X-Hub-Signature-256`), raw payload JSON, and `taaro-server`'s response status and body.
- Click **Redeliver** after a code change. GitHub issues a fresh `X-GitHub-Delivery` UUID on redelivery, so `taaro-server` processes it cleanly through deduplication.

### Inspecting or resetting local SQLite state

Local state lives in `.data/taaro.db` (WAL mode, gitignored).

- Inspect tables:
  ```bash
  sqlite3 .data/taaro.db "SELECT delivery_id, event, action, received_at FROM webhook_deliveries ORDER BY received_at DESC LIMIT 10;"
  sqlite3 .data/taaro.db "SELECT repo_full_name, pr_number, head_sha, check_run_id, observed_at FROM pull_requests;"
  ```
- Reset local state completely:
  ```bash
  rm -f .data/taaro.db .data/taaro.db-wal .data/taaro.db-shm
  ```

## 4. Common pitfalls

- **Self-triggering `check_run` loops:** Publishing a `Taaro Handoff` Check Run emits a `check_run` webhook back to your App. Keep `TAARO_APP_ID` set accurately so `internal/webhook` filters events where `check_run.app.id == TAARO_APP_ID`.
- **Signature mismatch (`401 Unauthorized`):** Verify `TAARO_WEBHOOK_SECRET` in `.env` matches the GitHub App settings, and never parse/re-serialize JSON before calling `webhook.VerifySignature`.
- **Permission error (`403 Resource not accessible by integration`):** Confirm `checks: write` is enabled on the App and that the repository installation accepted any recent permission update.
- **Secrets hygiene:** `.env`, `.secrets/`, `*.pem`, and `.data/` are ignored in `.gitignore`. Never commit private keys, webhook secrets, or raw dumps from private repositories.
