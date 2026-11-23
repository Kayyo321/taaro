# Taaro Adapter Protocol v1

This document specifies the HTTP JSON wire protocol implemented by `taaro-server`, the reference Go adapter (`internal/adapter`), and the `taaro` CLI in Taaro 1.0.0. It implements the progressive integration levels defined in [PROJECT.md](PROJECT.md#8-ecosystem-positioning-and-adoption-strategy).

## Authentication and visibility

- When `TAARO_API_TOKEN` is configured on `taaro-server`, repository handoff pages and every `/api/v1/*` endpoint require `Authorization: Bearer <token>`. Unauthenticated requests fail with `401 Unauthorized`.
- Without a token, only direct loopback clients using a loopback host may access repository data. A configured token grants access to the entire instance. Deploy one instance per trusted group.
- Proposal and awareness endpoints accept an optional `audience` parameter (`authorized` or `public`). Public readers and GitHub Check Run summaries never receive private proposal titles, scope paths, or excerpts.
- `GET /healthz` reports `{"status":"ok","version":"1.0.0","protocol_version":"1","app_id":...}`.

## Progressive integration levels

### Level 1 — Read and follow

Read observed repository state, revision-anchored handoffs, proposals, and overlap suggestions.

| Method & Path | Purpose |
| --- | --- |
| `GET /api/v1/context?repo=owner/repo&pr=N` | Fetch the latest observed `PullRequestRecord` pinned to `head_sha`. |
| `GET /api/v1/handoff?repo=owner/repo&pr=N` | Fetch the assembled `HandoffCard` (reviews, external checks, fixtures, validations, proposals, overlaps, copyable brief, and Markdown summary). |
| `POST /api/v1/proposals` | Register or revise a design PR, labeled issue, or versioned RFC proposal (`draft`, `accepted`, `withdrawn`, `superseded`). Substantive changes increment `version`. |
| `GET /api/v1/proposals?repo=owner/repo` | List indexed proposals for a repository, respecting audience visibility. |
| `GET /api/v1/overlaps?repo=owner/repo[&pr=N]` | List detected PR overlap suggestions attributed to Taaro (`authoritative: false`), suppressing incidental lockfile-only collisions and acknowledged pairs. |
| `POST /api/v1/overlaps/ack` | Record an author disposition (`complementary`, `combined`, or `dismissed`) to suppress repeated alerts on routine pushes. |

### Level 2 — Exchange and subscribe

Publish immutable artifact versions, subscribe consumers, record combined validations, and match reusable checks.

| Method & Path | Purpose |
| --- | --- |
| `POST /api/v1/artifacts` | Publish a new immutable version (`v1`, `v2`, …) with SHA-256 checksum, `producer_rev`, and optional `command`. Returns the artifact and all `stale_consumers` whose consumed version is now behind. |
| `GET /api/v1/artifacts?repo=owner/repo[&key=K][&version=N]` | List latest artifacts for a repository, or fetch a specific immutable version (or latest when `version` is omitted). |
| `POST /api/v1/artifacts/subscribe` | Record a worker's subscription and consumed version for `(worker_id, context_generation, consumer_rev)`. |
| `GET /api/v1/artifacts/consumers?repo=owner/repo&key=K` | List consumers of an artifact key and whether each is stale relative to the latest version. |
| `POST /api/v1/checks` | Record a validation or combined compatibility run identifying `producer_rev`, `consumer_rev`, `inputs_hash`, `toolchain`, `flags`, and `environment`. Marks older queued/in-progress speculative runs for the same `(repo, job_name, pr_number)` on superseded revision pairs as cancelled in SQLite. External job cancellation requires a CI integration. |
| `GET /api/v1/checks?repo=owner/repo[&pr=N]` | List recorded validation checks. |
| `POST /api/v1/checks/match` | Return `reusable: true` only when a non-flaky, non-cancelled check matches `producer_rev`, `consumer_rev`, `inputs_hash`, `toolchain`, `flags`, and `environment` exactly. |

### Level 3 — Enforced awareness

Mediate protected runtime operations and attest delivery to the acting worker.

| Method & Path | Purpose |
| --- | --- |
| `POST /api/v1/scopes` | Atomically register `(worker_id, context_generation)` active scope and query overlapping scopes, proposals, and open PRs in a single serialized transaction so concurrent starts cannot both see an empty scope. |
| `POST /api/v1/awareness/evaluate` | Evaluate a protected operation (`read`, `write`, `plan`, or `publish`) for `(worker_id, context_generation)`. |
| `POST /api/v1/awareness/receipt` | Record a verified delivery receipt scoped strictly to `(worker_id, context_generation)` and an exact `evidence_set_hash`. |
| `POST /api/v1/awareness/verify-pr` | Evaluate a candidate PR head SHA or synthetic `merge_group` SHA for the required **Taaro Awareness Check** (`success`, `action_required`, or `in_progress` — never `neutral` or `skipped` for incomplete evaluations). With awareness publication enabled, pass `"publish": true` for an open, indexed PR head to post the Check Run. GitHub merge-group webhook requests stay pending when their combined diff is unknown. |

## Mediation contract

1. **Silent routine clearance:** When a complete lookup finds zero applicable proposals, active scopes, overlapping PRs, or stale subscribed fixtures, `/api/v1/awareness/evaluate` returns `decision: "allow"`, `silent: true`.
2. **Quoted project data:** Every returned `EvidenceItem` carries `quoted_data: true`, source link, version, lifecycle state, match reason, and provenance (`authority`). Injected evidence carries no authority to change permissions or policy.
3. **Hold and fresh turn:** On `write`, `plan`, or `publish` with missing or stale evidence, `/api/v1/awareness/evaluate` returns `decision: "hold"`, `discard_pending_edit: true`, `require_fresh_turn: true`. The runtime must discard the pending edit selected under earlier context, insert the quoted data into the acting worker's context, post a receipt to `/api/v1/awareness/receipt` after insertion succeeds, and give the worker a fresh turn.
4. **Receipt isolation:** Receipts are bound strictly to `(repo, worker_id, context_generation)` and `evidence_set_hash` (covering matching-set membership, item versions, and lifecycle states). A parent worker's receipt never covers a delegated child worker, and context compaction/reset increments `context_generation`.
5. **Fail closed:** Unavailable lookups or unreachable servers return `pending` / hold. Private sources without authorized audience return `decision: "review_required"` with redacted opaque keys.
6. **CLI exit codes:** `taaro awareness check` exits `0` for `allow` / `allow_with_context`, `2` for usage errors, and `3` for `hold` / `review_required` / `pending`.

## Conformance verification

The reference session models worker context in memory. A production runtime must deliver the quoted data into the actual worker context, discard held edits and route every claimed operation through the adapter. The shared token identifies a trusted client of the service and the client supplies the worker identity.

The [Codex CLI pilot](CODEX_PILOT.md) implements delivery into a real worker turn for one selected file. A planner and a delegated editor receive separate worker identities and context generations.

Run the Level 1–3 conformance suite against the reference adapter (`internal/adapter`):

```bash
make conformance
```
