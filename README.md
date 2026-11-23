<p align="center">
  <img src="art/taaro-logo.png" alt="Taaro" width="200" />
</p>

# Taaro

Taaro (`v1.0.0`) is a coordination layer for coding agents working in the same GitHub repository. You can add it to a project with established tests and CI, or start a new Git project with Taaro configured from its first commit. Taaro ships a standalone CLI (`taaro`), a local coordination server and GitHub App (`taaro-server`), a versioned Adapter Protocol v1 with a Go reference adapter and conformance suite (`internal/adapter`), immutable fixture exchange, check-identity reuse, cross-PR overlap detection, revision-anchored handoff cards, and a reference implementation of Level 3 mediation for operations routed through the adapter.

## Start using Taaro

Build the tools with `make build` in the Taaro source checkout and add its `bin` directory to your PATH. Use Git and the standalone `taaro` binary in your own project. Replace `acme/widgets` with the repository’s GitHub name.

| Starting point | Command | Result |
| --- | --- | --- |
| Existing Git worktree | `taaro init --repo acme/widgets` | Adds a committable `.taaro.json` at the worktree root. Your project keeps its build, CI, and agent instructions. |
| New project | `taaro new --repo acme/widgets widgets` | Creates the local `widgets` directory, initializes Git, and writes the same configuration. Add your application and tests, commit, and push to the named GitHub repository. |

Install the App on the selected GitHub repository and start the server and webhook forwarder. After opening or updating a PR, run `taaro context --pr 1` (or `--full` for the complete revision-anchored handoff card) from either project, substituting its actual PR number. The output identifies the head SHA and observation time. The two setup commands work offline. GitHub repository creation and App installation are separate steps. [Project and service setup](docs/WORKFLOW.md#using-taaro-in-your-project)

## What agents can share

Consider a compiler change that needs a corresponding editor update. The compiler worker publishes an immutable diagnostic fixture (`taaro artifact publish`) as soon as the output format takes shape. The editor worker subscribes to that key (`taaro artifact subscribe`) and runs it against its own checkout with a locally chosen command (`taaro artifact rerun --key KEY --command COMMAND --record-check`) while compiler work continues. If the format changes, publishing a new immutable version flags stale consumers so the runtime delivers the revision as quoted project data before the editor worker’s next guarded edit (`taaro awareness check` / `internal/adapter`). Reviewers see both tested revisions, external CI runs, reviews, applicable proposals, and unacknowledged overlaps on the PR’s handoff card.

## System boundaries

| Component | Responsibility |
| --- | --- |
| Runtime adapter (`internal/adapter`, `taaro`) | Publishes opted-in immutable artifacts, delivers relevant evidence as quoted data to the acting worker, records delivery receipts per `(worker_id, context_generation)`, and mediates protected actions. |
| Coordination service (`taaro-server`, `internal/coordination`, `internal/store`) | Tracks immutable artifact versions, consumers, combined check identity, proposal lifecycle state, atomic active scopes, delivery receipts, and PR overlaps in SQLite. |
| GitHub App (`internal/githubapp`, `internal/webhook`) | Presents revision-anchored neutral `Taaro Handoff` cards at an exact commit, indexes reviews/proposal issues/external checks, purges repository data on uninstall, and can publish an experimental `Taaro Awareness Check`. A merge-group candidate with unknown scope stays pending. |
| Repository infrastructure | Retains authority over proposal decisions, CI jobs, branch protections, and release checks. |

The server and its shared token assume one trusted group with access to every configured repository. The reference adapter exercises the protocol in Go. A coding runtime must route its actual file, shell, subprocess, and delegated operations through mediation and insert evidence into the acting worker’s context before issuing a receipt. The Codex CLI pilot guards one selected text file, including staged shell edits and a separately identified child worker. [Run the Codex pilot](docs/CODEX_PILOT.md)

A routine fix with a complete lookup and no relevant overlap, open decision, or stale fixture clears silently with zero extra prompts or acknowledgments. The design targets repositories with coupled changes and developers who need to resume unfinished work across sessions.

## Documentation

This repository contains the Taaro 1.0.0 Go implementation (`cmd/taaro-server`, `cmd/taaro`, `internal/`), Adapter Protocol v1 specification, and product architecture specification.

- [Production pilot sequence](docs/WORKFLOW.md#from-source-checkout-to-a-live-pilot) covers publishing the candidate, deploying the server, installing the App, and exercising a real PR.
- [Development workflow](docs/WORKFLOW.md) covers Go setup, one-click GitHub App registration, local webhook forwarding, CLI workflows, testing, and debugging.
- [Adapter Protocol v1](docs/PROTOCOL.md) specifies the HTTP API across integration levels 1–3, receipt isolation, quoted-data rules, and conformance testing (`make conformance`).
- [System design](docs/PROJECT.md#2-core-system-model-and-capabilities) describes artifact exchange and the conditions for reusing a check.
- [Awareness requirements](docs/PROJECT.md#3-required-awareness-of-active-work-and-proposals) define what the runtime must verify before an action proceeds.
- [Handoff cards](docs/PROJECT.md#4-revision-anchored-handoff-cards-and-lifecycle-utility) and [overlap detection](docs/PROJECT.md#5-cross-team-overlap-detection-and-rework-prevention) describe what authors and reviewers see.
- [Trust and operations](docs/PROJECT.md#7-trust-security-and-operational-reliability) covers permissions, event processing, and data retention.
- [Implementation roadmap](docs/PROJECT.md#9-implementation-roadmap) and [pilot evaluation](docs/PROJECT.md#10-validation-and-experimental-design) define the delivery sequence and how to judge its value.
- [Contributor instructions](AGENTS.md) explain how to change and verify both code and specification.
