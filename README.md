<p align="center">
  <img src="art/taaro-logo.png" alt="Taaro" width="200" />
</p>

<p align="center">
  <strong>Intermediate artifact exchange, enforced context awareness, and revision-anchored handoffs for coding agents.</strong>
</p>

<p align="center">
  <a href="PROJECT.md">Specification</a> ·
  <a href="PROJECT.md#9-implementation-roadmap">Roadmap</a> ·
  <a href="PROJECT.md#10-validation-and-experimental-design">Validation</a>
</p>

> **Status:** Design and architecture specification. See [PROJECT.md](PROJECT.md) for the complete technical specification.

## Overview

Taaro enables coding agents across different tools and sessions to consume provisional artifacts from in-progress work, enforce awareness of active changes and design proposals before protected actions, and produce deterministic pull request handoffs.

It operates through two components:

- **GitHub App:** Surfaces revision-anchored handoff cards via the Checks API, detects overlapping work across pull requests, and provides an optional branch-protection awareness check.
- **Agent Runtime Adapters:** Exchange task-scoped provisional artifacts, inject relevant active changes and proposals into worker context prior to protected actions, and issue verifiable delivery receipts.

## Capabilities

| Capability | Description |
| :--- | :--- |
| **Intermediate Result Exchange** | Publishes versioned, task-scoped provisional artifacts (reproducers, golden fixtures, schemas) so downstream workers and resumed sessions start from runnable state without waiting for upstream merges. |
| **Required Awareness** | Delivers active overlapping changes and applicable proposals (design PRs, labeled issues, RFCs) into worker context before protected actions. Enforcement is fail-closed and backed by runtime-issued `(worker, context_generation)` delivery receipts rather than model self-reporting. |
| **Revision-Anchored Handoffs** | Publishes actionable, commit-SHA-pinned handoff cards as optional `neutral` GitHub Check Runs, paired with a copyable brief and read-only CLI (`taaro context --pr <number>`). |
| **Cross-Team Overlap Detection** | Flags independent PRs targeting the same issue or proposal scope early—including non-overlapping file changes—with dismissible, factual comparisons. |
| **Combined Validation** | Supports bounded compatibility checks on paired producer/consumer revisions and deduplicates eligible in-flight checks keyed by build inputs, toolchain, and environment. |

## Architecture & Security

- **Runtime-enforced trust boundary:** Delivery verification and action gating occur in the trusted runtime adapter; external proposals and diffs are injected as quoted data.
- **Deterministic provenance:** Platform events, human rationale, and tool suggestions are strictly separated and anchored to exact commit SHAs.
- **Least-privilege access:** Scoped per-repository via GitHub App installation, with LLM processing of private source disabled by default and no raw prompts or transcripts required for coordination.
- **Non-intrusive integration:** Operates alongside existing editors, planners, build caches, and merge queues without mandatory commit templates or workflow noise on routine changes.
- **Repository-level telemetry:** Metrics measure handoff reuse, wait reduction, and delivery outcomes rather than per-developer activity.

## Roadmap

1. **Phase 1 — Core Exchange (MVP):** Single-repository provisional fixture handoff, runtime-enforced proposal awareness, PR handoff cards, and cross-session reproducer reuse.
2. **Phase 2 — Portability & Enforcement:** Multi-client adapter interoperability, opt-in branch-protected Awareness Check, adversarial enforcement testing, and bounded combined CI runs.
3. **Phase 3 — Measured Expansion:** Cache sharing, cross-platform runner routing, and contract-to-consumer dependency matching driven by empirical telemetry.

## Specification

| Section | Scope |
| :--- | :--- |
| [1. Overview and Motivation](PROJECT.md#1-overview-and-motivation) | Problem definition, target users, and product scope |
| [2. Core System Model and Capabilities](PROJECT.md#2-core-system-model-and-capabilities) | System architecture, capabilities, and infrastructure boundaries |
| [3. Required Awareness](PROJECT.md#3-required-awareness-of-active-work-and-proposals) | Scope matching, proposal lifecycle, runtime receipts, and branch protection |
| [4. Handoff Cards and Lifecycle Utility](PROJECT.md#4-revision-anchored-handoff-cards-and-lifecycle-utility) | Data model, Checks API integration, and lifecycle coverage |
| [5. Cross-Team Overlap Detection](PROJECT.md#5-cross-team-overlap-detection-and-rework-prevention) | Overlap matching, contract conflicts, and intent signals |
| [6. Provenance and Runtime Integration](PROJECT.md#6-epistemology-provenance-and-runtime-integration) | Epistemic boundaries, claim provenance, and adapter security |
| [7. Trust, Security, and Reliability](PROJECT.md#7-trust-security-and-operational-reliability) | GitHub permissions, Git history edge cases, webhooks, and privacy |
| [8. Ecosystem and Adoption](PROJECT.md#8-ecosystem-positioning-and-adoption-strategy) | Ecosystem positioning, onboarding, and open adapter protocol |
| [9. Implementation Roadmap](PROJECT.md#9-implementation-roadmap) | Phased delivery milestones |
| [10. Validation and Experimental Design](PROJECT.md#10-validation-and-experimental-design) | Baseline methodology, experiments, and pilot success criteria |
