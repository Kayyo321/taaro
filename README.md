<p align="center">
  <img src="art/taaro-logo.png" alt="Taaro" width="200" />
</p>

# Taaro

Taaro is a proposed coordination layer for coding agents that work on the same repository. Its unit of exchange is something another worker can use directly, such as a failing input, an expected diagnostic, or a schema pinned to a revision.

Consider a compiler change that needs a corresponding editor update. The compiler worker can share a diagnostic fixture as soon as the output format takes shape. The editor worker can use it to exercise the renderer while the compiler work continues. If the format changes, the runtime delivers the revision to the editor worker before its next affected edit. A reviewer can then see which versions were tested together.

## System boundaries

| Component | Responsibility |
| --- | --- |
| Runtime adapter | Publishes opted-in artifacts, delivers relevant evidence to the acting worker, and mediates protected actions. |
| Coordination service | Tracks artifact versions, consumers, proposal scope, and delivery receipts. |
| GitHub App | Presents handoffs at an exact commit, surfaces related pull requests, and supports an optional required awareness check. |
| Repository infrastructure | Retains authority over proposal decisions, CI jobs, branch protections, and release checks. |

A routine fix with no relevant overlap or open decision should require zero extra prompts or acknowledgments. The design targets repositories with coupled changes and developers who need to resume unfinished work across sessions.

## Documentation

This repository contains the product and architecture specification alongside the initial Go implementation (`cmd/taaro-server` and `cmd/taaro`). Runtime adapters and later-phase capabilities remain specified in [docs/PROJECT.md](docs/PROJECT.md).

- [Development workflow](docs/WORKFLOW.md) covers Go setup, one-click GitHub App registration, local webhook forwarding, testing, and debugging.
- [System design](docs/PROJECT.md#2-core-system-model-and-capabilities) describes artifact exchange and the conditions for reusing a check.
- [Awareness requirements](docs/PROJECT.md#3-required-awareness-of-active-work-and-proposals) define what the runtime must verify before an action proceeds.
- [Handoff cards](docs/PROJECT.md#4-revision-anchored-handoff-cards-and-lifecycle-utility) and [overlap detection](docs/PROJECT.md#5-cross-team-overlap-detection-and-rework-prevention) describe what authors and reviewers see.
- [Trust and operations](docs/PROJECT.md#7-trust-security-and-operational-reliability) covers permissions, event processing, and data retention.
- [Implementation roadmap](docs/PROJECT.md#9-implementation-roadmap) and [pilot evaluation](docs/PROJECT.md#10-validation-and-experimental-design) define the delivery sequence and how to judge its value.
- [Contributor instructions](AGENTS.md) explain how to change and verify both code and specification.
