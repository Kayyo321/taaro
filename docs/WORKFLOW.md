# Using and developing Taaro

This guide covers registering a local Taaro GitHub App, running the Go server and CLI locally, forwarding webhooks, and debugging or testing changes against the contract in [PROJECT.md](PROJECT.md).

## Using Taaro in your project

From the Taaro **source checkout**, run `make build` with the Go toolchain listed in `go.mod`, then run `export PATH="$PWD/bin:$PATH"` in that shell. Run the following project commands in your application workspace. Git and the CLI are the only local requirements there.

**Existing project.** From anywhere in its Git worktree, run `taaro init --repo acme/widgets` with the exact owner and name GitHub reports. Review and commit the new `.taaro.json` to share the default with other clones and worktrees. Repeating the command with the same repository is safe. To change the association, edit the file explicitly. An explicit `taaro context --repo acme/widgets --pr 1` also works from outside a checkout.

**New project.** From the intended parent directory, run `taaro new --repo acme/widgets widgets`, then `cd widgets`. The destination must be new. Choose the application language and add its first source and test. Commit these files with `.taaro.json`, create the matching repository on GitHub, add its URL as a Git remote, and push your initial branch. Open a feature PR after the App is installed.

**Connect either project.** An operator runs the App setup, server, and webhook forwarding commands below from the Taaro source checkout. Select the target repository in the App installation settings. PR read permission exposes descriptions, comments, and diffs. Keep service credentials in the operator’s checkout. The project file contains only the GitHub repository name.

**Verify the connection.** Run `taaro health`, then open or update a PR and run `taaro context --pr NUMBER` inside the configured project. Compare the reported head with the PR on GitHub. A 404 means the server has no observation for that repository and number yet. Installation alone does not backfill existing PRs. With installation credentials, PR and proposal issue deliveries refresh current state from GitHub. Accepted deliveries survive a server restart and processing failures retry from SQLite. Deliveries that never reached the server require GitHub redelivery. If the server is on a different address, pass `--server URL` or export `TAARO_SERVER_URL` in the CLI’s shell.

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/taaro-server` | HTTP server binary hosting the GitHub App webhook endpoint, Manifest setup wizard (`-setup`), version flag (`-version`), handoff pages, and Coordination API v1. |
| `cmd/taaro` | Standalone CLI binary for project setup (`init`, `new`), version, queries (`health`, `context`), immutable artifacts (`artifact`), proposals (`proposal`), scopes (`scope`), awareness mediation (`awareness`), overlaps (`overlap`), and checks (`check`). |
| `internal/version` | Semantic version (`1.0.0`) and Adapter Protocol version (`1`). |
| `internal/config` | Loads `.env` (without overriding exported variables) and parses runtime configuration (including optional `TAARO_API_TOKEN` and `TAARO_ENABLE_AWARENESS_CHECK`). |
| `internal/store` | `Store` interface and embedded pure-Go SQLite implementation (`modernc.org/sqlite`) for webhook deduplication, PR revisions, reviews, external check runs, immutable artifacts, subscriptions, validation checks, proposals, atomic worker scopes, delivery receipts, awareness evaluations, overlap acknowledgments, and repository purge. |
| `internal/coordination` | Deterministic matching engine (issue-link extraction, path/interface matching, lockfile suppression), awareness mediation, check-reuse identity & speculative cancellation, overlap detection, and revision-anchored handoff assembly. |
| `internal/githubapp` | GitHub App Manifest setup wizard and installation-authenticated GitHub client (`ghinstallation/v2` + `go-github/v88`) publishing neutral `Taaro Handoff` checks and required `Taaro Awareness Check` runs. |
| `internal/webhook` | `POST /webhooks/github` handler: HMAC-SHA256 verification, optional payload capture, self-`check_run` filtering, SQLite deduplication, `202 Accepted` acknowledgment, and async ingestion of `pull_request`, `pull_request_review`, `issues` (proposal labels), external `check_run`, `merge_group`, and `installation`/`installation_repositories` purge events. |
| `internal/server` | Stdlib `net/http` router wiring `/healthz`, `/webhooks/github`, `/handoffs/{owner}/{repo}/pull/{number}`, and the `/api/v1/*` Coordination API (with optional Bearer auth). |
| `internal/adapter` | Reference Go runtime adapter implementing Adapter Protocol v1 across Levels 1–3 plus the conformance test suite (`make conformance`). |
| `Makefile` & `.air.toml` | Local dev tasks, build targets with version ldflags, conformance target, and live-reload configuration for `cmd/taaro-server`. |

## Prerequisites

- **Go** (`go version` matching `go.mod`; the race-test target also needs CGO and a C compiler)
- **Make** (`make help` lists all targets)
- **Node/npx** (used by `make smee` to run `smee-client`)
- **Cloudflared** (optional; used by `make tunnel` when testing clickable Check Run `details_url` links)
- **Air** (optional; install once for live-reloading `taaro-server`):

```bash
make install-tools
```

## 1. Create and install a local GitHub App

An operator can register one private development App and install it on selected repositories. The manifest requests permissions from [Trust, Security, and Operational Reliability](PROJECT.md#7-trust-security-and-operational-reliability).

1. For a public sandbox, open `https://smee.io/new` in a browser to create a persistent webhook proxy channel and copy the URL (for example, `https://smee.io/AbCdEf123`).
   For private projects, use an approved webhook ingress that forwards only `POST /webhooks/github`. Skip `make smee` when delivering directly.
2. Start the setup wizard:
   ```bash
   make setup
   ```
3. Open `http://127.0.0.1:8080`, enter a unique App name (such as `taaro-dev-<yourhandle>`), paste your Smee URL, optionally check `Contents: read`, and submit **Register GitHub App**.
4. Confirm creation on GitHub. GitHub redirects back to `http://127.0.0.1:8080/setup/callback` with the conversion code and registration state.
5. `taaro-server` exchanges the code via GitHub's manifest conversion API and writes two gitignored files with `0600` permissions:
   - `.env` (`TAARO_APP_ID`, `TAARO_APP_SLUG`, `TAARO_WEBHOOK_SECRET`, `TAARO_CLIENT_ID`, `TAARO_CLIENT_SECRET`, `TAARO_SMEE_URL`, `TAARO_PRIVATE_KEY_PATH`)
   - `.secrets/taaro-dev.pem` (the App's RSA private key)
6. Click the **Install the App on your selected repository** link shown on the completion page and select your target repository (use a public sandbox for tunnel testing).
7. Stop the setup server with `Ctrl+C`.

> **Permission updates:** If you later add permissions in GitHub App settings, existing installations do not receive them automatically. Accept the pending permission prompt on the installation's settings page before testing the new API calls.

### Registered manifest permissions and events

| Permission | Access | Purpose |
| --- | --- | --- |
| `metadata` | `read` | Identify repositories and route events. |
| `pull_requests` | `read` | Read PR state, diffs, threads, and head SHAs. |
| `issues` | `read` | Resolve linked goals, labels, and proposal issues. |
| `checks` | `write` | Publish the optional neutral `Taaro Handoff` check. |
| `merge_queues` | `read` | Receive merge-group check requests. |
| `contents` | `read` *(optional)* | Read versioned RFC files when enabled. |

The default App subscribes to `pull_request`, `pull_request_review`, `issues`, `check_run`, `check_suite`, and `merge_group` (GitHub sends `installation` and `installation_repositories` automatically). The server indexes PR revisions, reviews, proposal-labeled issues, and external check runs, filters its own check events, and purges stored repository data when an installation or repository is removed. `merge_group` events are also handled once enabled in GitHub App settings for the branch-protection trial (`TAARO_ENABLE_AWARENESS_CHECK=1`). [GitHub webhook events](https://docs.github.com/en/webhooks/webhook-events-and-payloads#merge_group)

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
2. `taaro-server` verifies `X-Hub-Signature-256`, deduplicates `X-GitHub-Delivery` in `.data/taaro.db`, responds `202 Accepted`, records the PR observation timestamp, and, for actionable handoffs with installation credentials, publishes a completed `neutral` Check Run named **Taaro Handoff** pinned to the PR's `head_sha`.
3. Query observed PR state, exchange fixtures, or run awareness checks from the CLI (see [Adapter Protocol v1](PROTOCOL.md) for the full HTTP API):
   ```bash
   taaro health
   taaro context --repo <owner>/<repo> --pr <number> --full
   taaro artifact publish --key compiler/diag --payload '{"span":1}' --command 'test -s "$TAARO_FIXTURE_PATH"'
   taaro artifact rerun --key compiler/diag --command 'test -s "$TAARO_FIXTURE_PATH"' --record-check --pr <number>
   ```

To run the producer’s command, inspect the immutable version with `taaro artifact get --key compiler/diag --version 1 --json`, then explicitly select it with `taaro artifact rerun --key compiler/diag --version 1 --use-recorded-command`. A recorded check identifies both Git revisions. Commit the project configuration and keep the consumer worktree clean when recording the result. A local-only rerun can use a dirty checkout.

### Testing clickable `details_url` links

`smee.io` only proxies webhook POSTs. A public HTTPS endpoint lets a Check Run link to the local handoff page. A link for a superseded head returns HTTP 410 once the latest observation moves to another SHA.

Set `TAARO_API_TOKEN` in `.env` and export it in CLI or adapter shells before using a public address. The same bearer token protects HTML handoff pages and the API. A plain GitHub details link opens the protected URL and requires an authenticated client or access-controlled proxy to supply the token. A direct loopback deployment may omit the token. Use tunnels only with dedicated sandbox data. Keep each instance within one repository access group.

1. Start a Cloudflare quick tunnel:
   ```bash
   make tunnel
   ```
2. Copy the printed `https://<random>.trycloudflare.com` URL into `TAARO_PUBLIC_URL` in `.env` and restart `taaro-server`.
   - `taaro-server` attaches `details_url` to published Check Runs whenever `TAARO_PUBLIC_URL` begins with `https://`.
   - Keep `TAARO_SMEE_URL` for webhooks, or temporarily point the GitHub App's Webhook URL to `https://<random>.trycloudflare.com/webhooks/github`.

## 3. Debugging and testing

### Offline test suite and webhook fixture harness

Run the full test suite with the race detector, or run the Level 1–3 Adapter Protocol conformance suite directly:

```bash
make test
make conformance
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
- Click **Redeliver** after a code change. GitHub reuses the original delivery GUID. Taaro deduplicates the GUID and retains the verified payload in SQLite until processing succeeds. A single server worker retries failures with backoff and resumes due work on startup. Inspect the server logs and the `pending_webhooks` table for repeated failures. GitHub-side redelivery covers requests the server never accepted. [GitHub webhook redelivery](https://docs.github.com/en/webhooks/using-webhooks/automatically-redelivering-failed-deliveries-for-a-github-app-webhook)

### Inspecting or resetting local SQLite state

Local state lives in `.data/taaro.db` (WAL mode, gitignored).

- Inspect tables:
  ```bash
  sqlite3 .data/taaro.db "SELECT delivery_id, event, action, received_at FROM webhook_deliveries ORDER BY received_at DESC LIMIT 10;"
  sqlite3 .data/taaro.db "SELECT repo_full_name, pr_number, head_sha, check_run_id, observed_at FROM pull_requests;"
  ```
- Stop the server before resetting local state completely:
  ```bash
  rm -f .data/taaro.db .data/taaro.db-wal .data/taaro.db-shm
  ```

## 4. Common pitfalls

- **Self-triggering `check_run` loops:** Publishing a `Taaro Handoff` Check Run emits a `check_run` webhook back to your App. Keep `TAARO_APP_ID` set accurately so `internal/webhook` filters events where `check_run.app.id == TAARO_APP_ID`.
- **Signature mismatch (`401 Unauthorized`):** Verify `TAARO_WEBHOOK_SECRET` in `.env` matches the GitHub App settings, and never parse/re-serialize JSON before calling `webhook.VerifySignature`.
- **Permission error (`403 Resource not accessible by integration`):** Confirm `checks: write` is enabled on the App and that the repository installation accepted any recent permission update.
- **Secrets hygiene:** `.env`, `.secrets/`, `*.pem`, and `.data/` are ignored in `.gitignore`. Never commit private keys, webhook secrets, or raw dumps from private repositories.
