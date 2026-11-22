<p align="center">
  <img src="art/taaro.png" alt="Taaro emblem" width="96" />
</p>

# Taaro — Project Specification and Design Document

> **Status:** Architecture and Product Specification  
> **Scope:** Cross-tool intermediate artifact exchange, runtime-enforced awareness, revision-anchored PR handoffs, and evaluation methodology for coding-agent workflows.

Taaro is a coordination and handoff layer for solo developers and engineering teams working with coding agents. It enables an agent to begin useful work from an intermediate output—such as a failing reproducer, golden fixture, or schema—produced by another agent that is still coding, while guaranteeing that acting workers receive relevant ongoing changes and design proposals before taking protected actions.

---

## Contents

1. [Overview and Motivation](#1-overview-and-motivation)
2. [Core System Model and Capabilities](#2-core-system-model-and-capabilities)
3. [Required Awareness of Active Work and Proposals](#3-required-awareness-of-active-work-and-proposals)
4. [Revision-Anchored Handoff Cards and Lifecycle Utility](#4-revision-anchored-handoff-cards-and-lifecycle-utility)
5. [Cross-Team Overlap Detection and Rework Prevention](#5-cross-team-overlap-detection-and-rework-prevention)
6. [Epistemology, Provenance, and Runtime Integration](#6-epistemology-provenance-and-runtime-integration)
7. [Trust, Security, and Operational Reliability](#7-trust-security-and-operational-reliability)
8. [Ecosystem Positioning and Adoption Strategy](#8-ecosystem-positioning-and-adoption-strategy)
9. [Implementation Roadmap](#9-implementation-roadmap)
10. [Validation and Experimental Design](#10-validation-and-experimental-design)

---

## 1. Overview and Motivation

### 1.1 Problem Statement

Developers increasingly run coding agents across multiple sessions, models, worktrees, and tools. Today, coordination between those agents falls back on manual copy-pasting, heavyweight draft PRs, or starting over from scratch:

- **For a solo developer**, resuming yesterday's bug fix or switching models often means paying a fresh session to rediscover how to reproduce the failure, reconstruct which checks already ran, and re-read open review threads.
- **For a team**, coupled changes—such as a server API and its client, or a compiler diagnostic and its editor renderer—either execute serially while one side waits for a finished branch, or execute in parallel blindly until merge conflicts or contract breaks surface late.

Taaro addresses this across two cooperating surfaces:

1. **A GitHub App** that makes visible work discoverable, publishes revision-anchored handoff cards on pull requests, and provides an optional branch-protection awareness check.
2. **Supported Agent Runtime Adapters** that deliver relevant ongoing changes, provisional artifacts, and proposals directly into the acting worker's context before protected actions, and attest delivery.

The larger opportunity is to let workers exchange usable intermediate results across different tools, revisit only the work affected by a subsequent change, and validate related patches together early.

### 1.2 The Incremental-Commit Trap

A common instinct when supervising coding agents is to instruct them to *"make small incremental commits with detailed messages."* While that sounds like a request for clean Git history, an agent can reasonably interpret it as a strict execution schedule: after each edit, stop, inspect the diff, compose a narrative, run a broad check, commit, and reconstruct where it left off. When multiple workers wait on each checkpoint, those pauses land directly on the project's critical path.

Elapsed project duration alone cannot reveal how much time went to writing narratives, waiting on tests, integration rework, ambiguous scope, or unavailable reviewers. Removing that narrative-checkpoint instruction costs nothing. Teams should keep commit boundaries that genuinely aid review, bisecting, and rollback, and explain design decisions *once*—in an issue or review thread where the explanation has an actual reader. **Taaro earns its place only if it saves time after that baseline workflow cleanup.**

### 1.3 Who Benefits Most (and Where Taaro Adds Little)

| Profile | Expected Value |
| :--- | :--- |
| **Solo developers switching sessions, models, or days on unfinished work** | High — immediate reuse of verified reproducers, fixtures, and revision-pinned review state. |
| **Repositories where producer and consumer changes routinely meet** (compilers, SDKs, APIs, shared libraries) | High — parallel overlap on provisional artifacts, early contract checks, and overlap visibility before rework accumulates. |
| **Solo developer finishing a self-contained task in one sitting and pushing directly to `main`** | Low — little state to recover or share across boundaries. |
| **Team blocked for two days waiting on one overbooked maintainer** | Conditional — faster handoff summaries help only if maintainer reading time is a meaningful share of the wait. |

### 1.4 Guiding Product Principles

- **Shorten real waits or remove real work:** Every mechanism must justify itself against a disciplined baseline (clean prompts, parallel worktrees, native build caches, early draft PRs).
- **Quiet on straightforward fixes:** A routine, unrelated fix with no overlap or open question must pass automatically with zero extra prompts, templates, or manual acknowledgments.
- **Provenance over synthesis:** Every fact retains its source, exact commit revision, status, and observation time. Taaro never invents rationale or blurs platform facts with heuristic suggestions.
- **Work with existing tools:** Planning, model selection, subagent spawning, build caching, merge queues, and spend controls remain with the tools and teams that already own them.

---

## 2. Core System Model and Capabilities

### 2.1 Architecture Overview

Taaro acts as an opt-in coordination plane connecting local or cloud agent runtimes, repository hosting (GitHub), and existing CI or build infrastructure without taking ownership of the editor, model, or task planner.

```text
┌─────────────────────────────────────────────────────────────────────────────────┐
│                       AGENT RUNTIMES (Heterogeneous Tools)                      │
│                                                                                 │
│  ┌─────────────────────┐   ┌──────────────────────┐   ┌──────────────────────┐  │
│  │ Producer Worker     │   │ Consumer Worker      │   │ Fresh Solo Session   │  │
│  │ (Runtime Adapter A) │   │ (Runtime Adapter B)  │   │ (CLI / Brief / Tool) │  │
│  └──────────┬──────────┘   └──────────┬───────────┘   └──────────┬───────────┘  │
└─────────────┼─────────────────────────┼──────────────────────────┼──────────────┘
              │ publish provisional     ▲ subscribe, receive       ▲ read-only query
              │ artifact & scope        │ fixture & enforce gate   │ or copy brief
              ▼                         ▼                          │
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            TAARO COORDINATION LAYER                             │
│                                                                                 │
│  ┌─────────────────────┐   ┌──────────────────────┐   ┌──────────────────────┐  │
│  │ Versioned Artifact  │◄─►│ Awareness & Proposal │──►│ Handoff Assembler    │  │
│  │ Store (Fixtures)    │   │ Index & Receipts     │   │ (SHA-Pinned Cards)   │  │
│  └─────────────────────┘   └──────────┬───────────┘   └──────────┬───────────┘  │
└───────────────────────────────────────┼──────────────────────────┼──────────────┘
              ▲                         ▲                          ▲
              │ indexed revisions       │ PRs, SHAs, required      │ webhooks, neutral
              │ & proposal status       │ check & combined CI      │ check & verdicts
              │                         ▼                          ▼
┌─────────────┴─────────────────────────┴──────────────────────────┴──────────────┐
│                       REPOSITORY & BUILD INFRASTRUCTURE                         │
│                                                                                 │
│  ┌─────────────────────┐   ┌──────────────────────┐   ┌──────────────────────┐  │
│  │ Proposal Sources    │   │ GitHub               │   │ Existing CI & Build  │  │
│  │ (PRs, Issues, RFCs) │   │ (PRs, Checks, MQ)    │   │ (Runners & Caches)   │  │
│  └─────────────────────┘   └──────────────────────┘   └──────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Walkthrough: Concurrent Compiler and Editor Change

The most valuable output one coding agent can give another is often a *half-finished* piece of work: a failing fixture hours before the fix is ready, or an interface before its implementation exists. Required awareness protects that handoff if the underlying work changes later.

Consider an illustrative compiler-and-editor project (referred to throughout this document as **`Neura`**). A developer asks their usual coding tool to add a secondary source span to compiler diagnostics and render it in the editor. The tool assigns one worker to the compiler and another to the editor. The editor worker needs an example of what the compiler will emit.

1. **Provisional publication:** The compiler worker writes an example diagnostic and its expected output (a golden fixture). A configured adapter detects those files and publishes an immutable, task-scoped version marked `provisional`. Uncommitted artifacts are shared only through an opted-in adapter within a repository-approved sharing scope.
2. **Immediate cross-tool consumption:** The editor worker receives the fixture in its own context and runs its decoder against it immediately—even if it runs in a different client. The compiler worker continues implementing; neither worker owes Taaro a progress report or an extra commit.
3. **Targeted revision propagation:** Later, the compiler worker changes how an absent secondary span is encoded. Because Taaro recorded which worker consumed the earlier fixture version, it delivers the changed field to the editor worker before its next affected action.
4. **Early combined validation:** Once candidate patches exist on both sides, Taaro can request the team's existing CI to run the relevant compatibility test against that exact revision pair in a disposable environment. Both workers receive the same failing example and can compare emitted output against consumer expectations. Standard merge and release checks still evaluate what ultimately ships.

#### Critical-Path Timing Comparison (Illustrative)

Suppose producing the fixture takes 6 minutes, the rest of the compiler change takes 12 minutes, the editor change takes 8 minutes, and the final compatibility check takes 4 minutes:

```text
Sequential (~30 min illustrative)
  Compiler  [Fixture 6m][== Compiler Impl 12m =]
  Editor                                        [= Editor 8m ==]
  CI Check                                                      [Chk 4m]
            ├───────────┴───────────────────────┴───────────────┴───────┤
            0m          6m                      18m             26m    30m

Overlapped with Taaro (~22 min illustrative)
  Compiler  [Fixture 6m][== Compiler Impl 12m =]
                        ▼ provisional fixture
  Editor                [= Editor 8m ==]
  CI Check                                      [Chk 4m]
            ├───────────┴───────────────┴───────┴───────┤
            0m          6m              14m     18m    22m
```

**Honest baseline caveat:** These timings are illustrative. Publishing an early draft PR could achieve the same overlap. Taaro has to beat that workflow *after* accounting for the time spent publishing the fixture, revising it, and rerunning affected tests. If interface churn consumes those eight minutes, waiting for a stable interface would have been cheaper. **A narrow, executable example is a better first bet than a forecast of an entire unfinished branch.**

### 2.3 Four Core Capabilities

| Capability | What Changes During the Session | When It Pays Off (and Constraints) |
| :--- | :--- | :--- |
| **Start with a runnable example** | A solo agent opening yesterday's bug receives the exact reproducer, command, and source revision, and reruns it on today's checkout before relying on it. | Switching models or resuming the next day without paying a fresh session to rediscover how to trigger the failure. |
| **Use work while it is in progress** | A worker requests a concrete output (such as a fixture or schema) and subscribes to its availability. Its existing planner schedules another ready task or suspends until delivery. | Downstream work starts earlier; waiting agents avoid repeated polling model calls asking whether anything changed. |
| **Join an eligible check already running** | Two workers requesting the same reproducible job follow a single run. Taaro reuses the build system's own identity for inputs, toolchain, flags, and environment wherever possible. | One execution serves multiple consumers. A different tree, flaky test, or unknown environment requires a fresh run; saving compute shortens delivery only when that check was on the critical path. |
| **Try dependent changes together early** | A declared producer and consumer request one targeted test against their combined candidate revisions. Exact revisions are recorded and results returned to both. | Contract breaks surface while both workers are still active to fix them. Speculative jobs must be capped, superseded runs canceled, and arbitrary all-pairs testing avoided. |

### 2.4 Interoperating with Build Caches and Merge Queues

Build systems such as **Bazel** already reuse action outputs for known inputs via remote caching, and **GitHub merge queues** already validate combinations ready to land. Taaro does not reimplement either system; it connects an agent's declared need to those systems *earlier*, while the result can still change the work plan.

Taaro maintains a strict distinction between evidence types:

- **Agent diagnosis:** Treated as a *lead with a source*, never as ground truth.
- **Reproducible test verdict:** Governed by strict input, revision, and environment reuse conditions.

### 2.5 Project-Scale Evolution

At larger project scale, agents can express dependencies directly in terms of the *results* they need. For a `Neura` release, a packaging agent might declare a need for a Windows install-and-uninstall verification result for a specific build revision. A connected Windows runner can claim that request under the team's existing permissions and budget while another agent fixes the Linux package; if a runner is already verifying that exact build, the packaging agent simply follows that run. Maintainers can immediately see which missing result is delaying release.

Supporting this requires capturing a small, verifiable provenance graph at existing tool boundaries:

1. **Who requested** an output
2. **Which revision produced** it
3. **Who consumed** it
4. **What changed** afterward

Complex planning, model selection, subagent spawning, and per-team spend remain owned by existing tools and humans. Broader cross-boundary scheduling is warranted *only* if measured blocking across tool boundaries justifies it. A single-file task with no downstream consumer and no expensive check incurs almost none of this machinery.

---

## 3. Required Awareness of Active Work and Proposals

### 3.1 The Core Requirement

> **Product Requirement:** A worker may take a protected action *only after* its runtime has delivered the current, known changes and proposals relevant to that action into that worker's context.

A trusted runtime can attest **delivery**—specifically, which evidence versions reached which worker and context generation prior to an action. Whether the model *understood* the evidence or chose the best design is a separate question evaluated from the resulting plan, patch, or test results.

### 3.2 What Counts as Relevant

Proposals need a home before code exists. Teams designate existing **design PRs**, **labeled issues**, or **versioned RFC files** as configured proposal sources, retaining their native identifiers, owners, and review states.

**Initial (v1) Matching Rules — High-Precision, Explicit Signals:**

- An explicit shared issue link
- A declared component or file path
- A named interface and its registered consumers (an API caller can be affected even when it never edits the API definition)
- Active draft PRs and scoped in-progress work reported by connected agents

**Non-Blocking Signals:** Similar wording or neighboring directories may suggest an optional lookup, but are too weak to block work on their own.

**Delivered Payload Structure:** Every delivered item includes:

1. The concrete substance that could change the worker's decision
2. Source link
3. Version / revision identifier
4. Status (e.g., `draft`, `accepted`, `withdrawn`, `superseded`)
5. Reason it matched the worker's scope

**Proposal Authority Lifecycle:**

- **`draft`:** Visible while alternatives are still open. After receiving it, the worker may still proceed with the current design.
- **`accepted`:** Carries the authority defined by the repository; a separately configured compatibility test may enforce concrete contract requirements.
- **`withdrawn` / `superseded`:** Ceases to apply; Taaro delivers the status change to any worker that previously received the proposal.
- **Authority control:** Only designated maintainers or configured authoritative sources may change a proposal's status.

### 3.3 Enforcement Lifecycle

Awareness checks operate at four points in a worker's lifecycle:

```text
            ┌───────────────────────────────────────────────────────┐
            │  Worker Action Attempt                                │
            │  (Task Start / Scope Entry / Guarded Edit / Publish)  │
            └───────────────────────────┬───────────────────────────┘
                                        ▼
            ┌───────────────────────────────────────────────────────┐
            │  Lookup complete & index available?                   │
            └─────────────┬───────────────────────────┬─────────────┘
                 No       │                           │ Yes
        (error / no auth) ▼                           ▼
    ┌───────────────────────────┐      ┌──────────────────────────────────┐
    │ HOLD ACTION (Fail Closed) │      │ Applicable proposals or active   │
    │ Await verification        │      │ work matched this scope?         │
    └───────────────────────────┘      └────────┬────────────────┬────────┘
                                          No    │                │ Yes
                                                ▼                ▼
                                ┌────────────────────┐  ┌─────────────────────────┐
                                │ CLEAR AUTOMATICALLY│  │ Current versions in     │
                                │ Proceed silently   │  │ this context gen with   │
                                └────────────────────┘  │ a valid receipt?        │
                                                        └───┬─────────────────┬───┘
                                                       Yes  │                 │ No (missing,
                                                            ▼                 │ stale, or reset)
                                          ┌──────────────────────┐            ▼
                                          │ CLEAR — Proceed      │  ┌───────────────────────────┐
                                          └──────────────────────┘  │ HOLD PROPOSED ACTION      │
                                                                    │ 1. Inject delta as        │
                                                                    │    quoted project data    │
                                                                    │ 2. Runtime issues receipt │
                                                                    │ 3. Give worker fresh turn │
                                                                    │    to reconsider          │
                                                                    └───────────────────────────┘
```

1. **At task start or delegation:** Resolve the issue or intended scope if known. For connected workers, register active scope and query overlapping work in **one ordered (atomic) operation** so two simultaneous starts cannot both assume they are alone. Deliver relevant items to the *actual* worker preparing the plan, including implicit subagents. If scope is not yet known, permit discovery and resolve incrementally as paths or interfaces appear.
2. **When work enters a new scope:** For a guarded read, return relevant context alongside the artifact. Before a guarded write or a submitted plan relying on a newly encountered interface, verify the actual target against the worker's current context generation. If evidence is missing or stale, **hold the proposed action**, return the update to the worker, and give it a **fresh turn to reconsider**. Replaying an edit chosen before delivery would defeat the rule.
3. **When relevant work changes:** Track both versions of applicable items and membership of the matching set. A newly filed proposal can make a worker's context stale even on an unchanged Git commit. Deliver *only what changed* before the worker's next affected action. Restore relevant material after a context reset, compaction that dropped the evidence, or delegation to a fresh worker. Unrelated proposal revisions must never interrupt the worker.
4. **At publication:** Re-evaluate the actual diff against all applicable proposals. Bind the verdict to the candidate revision, scope, evidence versions, and trusted runtime delivery records. Newly discovered overlap requires revisiting the affected change. A complete evaluation with no matches clears automatically. **Fail-closed guarantee:** missing access, an unavailable index, or an unverified delivery leaves a protected action pending—a blank result from an incomplete lookup can never clear it.

### 3.4 Illustrative Enforcement Scenario

A `Neura` worker is about to modify the diagnostic renderer. Another team has opened a draft proposal to move source-span selection into the lexer, naming the `diagnostic-span` contract.

- Before the renderer edit executes, Taaro delivers the proposed ownership shift and the linked lexer PR to the renderer worker. The worker can adopt the other team's fixture, keep a renderer fallback for the current contract, or revise its plan.
- If the proposal transitions to `accepted` while the worker is running tests, the runtime delivers that decision before the worker's next affected edit.
- Meanwhile, a separate worker fixing a spelling error in CLI help text proceeds from start to finish with zero prompts or interruptions.

### 3.5 Trust Boundaries, Branch Protection, and Known Limits

#### Runtime Guarantees and Blind Spots

- **Trusted receipts, no self-certification:** Inside a supported runtime, Taaro enforces delivery only for actions that the runtime mediates. The trusted component issues a receipt *after* successful context insertion, scoped to `(worker_id, context_generation)`. A model cannot self-certify by emitting text such as *"I read it."*
- **Remaining blind spots:** Unpublished work in disconnected tools, proposals outside configured sources, and undeclared dependencies cannot be seen by Taaro.
- **Private policy sources:** When an applicable policy lives in a private source the worker is not authorized to read, Taaro reports that the operation requires authorized review without exposing the private source's contents.

#### Branch Protection via the GitHub App

On a protected branch, maintainers may require a dedicated **Taaro Awareness Check** published by the GitHub App and restrict branch-protection bypasses:

- **Exact SHA validation:** Validate the current candidate head SHA—including the combined merge-group SHA when a GitHub merge queue is enabled.
- **Strict conclusion semantics:** Emit `success` *only* for a complete, current evaluation backed by required evidence. Because GitHub treats `neutral` and `skipped` check conclusions as passing for required status checks, neither may ever be used when evidence is missing or incomplete.
- **Unsupported editors:** A contribution authored in an uninstrumented editor can be held at merge for review by a supported worker. That review establishes what the reviewer received from that point onward; the original author's local context remains unknown. Where agent authorship cannot be reliably determined, apply the rule by scope to all changes.

#### Freshness and the Final Landing Race

Freshness is measured at validation time. Taaro records the indexed snapshot identifier and timestamp, refreshes from configured sources, and invalidates affected checks when new evidence arrives. An external proposal can still change during the window between a green GitHub check and the merge click; eliminating that final race would require a system that orders proposal admission and branch landing together. The first release explicitly reports the validated snapshot and measures event propagation delay.

---

## 4. Revision-Anchored Handoff Cards and Lifecycle Utility

### 4.1 Entity Model

Taaro anchors every fact to the natural GitHub object that owns it:

- **Commit SHA:** Identifies an immutable snapshot; attached to every revision-specific fact (diffs, test runs, review targets).
- **Pull Request:** Holds the evolving discussion and serves as the primary home of a handoff whenever a PR exists.
- **Issue:** Expresses a larger goal spanning one or many PRs (while a single PR may advance multiple issues). Explicit goal links can be traversed in either direction to explain purpose without falsely implying that a broader milestone is complete.
- **Direct pushes / unlinked commits:** A direct push may have a commit-only view if the GitHub App has access; unlinked changes stand on their own without requiring an issue.

### 4.2 Anatomy of a Handoff Card

Suppose a `Neura` contributor uses an agent to improve diagnostics for unterminated strings, and a fresh agent inherits the branch the next morning. A PR handoff card assembles only the actionable, revision-pinned facts from existing diffs, threads, and CI records:

> ### PR #247 · Point unterminated-string errors at the opening quote
>
> - **Goal** *(from issue #231)* — When a string spans several lines, the diagnostic should point to the opening quote.
> - **Change at `91bd80a`** — The lexer diff adds `opening_span = token.span`. The new multiline input is in the diagnostics fixture.
> - **Checks** — The Linux diagnostics job passed at `6f150de`. The check for the current head, `91bd80a`, is queued.
> - **Open question** *(from review of `6f150de`)* — *"Which span does the renderer show when the file ends immediately after a backslash?"* → Open review thread · Open fixture
>
> *Opened by Maya · Copy handoff · View diff since review*

Why this structure saves time:

- The incoming agent can immediately inspect the trailing-backslash fixture and answer the reviewer before modifying the lexer again.
- The earlier green check carries a precise revision boundary (`6f150de` vs. `91bd80a`) rather than misleadingly claiming the latest head passed.
- A new reviewer can jump directly from the unresolved question to the relevant diff since their last review.
- **Disciplined agent attribution:** An agent-contribution field appears only when backed by evidence, stated at the exact scope of that evidence—for example, *"Claude Code reported an Explore session linked to this PR."* A stronger claim such as *"Explore fixed the lexer"* requires separate evidence tying that session to the surviving patch.

### 4.3 The Quiet Small-Fix Rule

If a developer fixes a single misspelled option in CLI help text and pushes normally—with a clear diff and no handoff state, open questions, or related active work—**Taaro stays quiet.** The author owes it no issue, PR template, test, coverage target, prompt log, agent declaration, or prose summary.

Actionability, not diff size, governs interruption: a one-line change to a public API can still merit surfacing to registered consumers. The test for every notice is simple: *what concrete action could the reader take because of this information?*

### 4.4 GitHub Display Surface

Using the documented **GitHub Checks API**, the GitHub App publishes a Check Run against a specific commit SHA containing a Markdown summary and a details URL linking to Taaro's richer view.

- **No DOM hacks or noisy Git history:** Taaro does not inject custom panels into GitHub commit pages and keeps Git commit history ordinary.
- **Optional display check vs. required enforcement check:** The handoff card is published as a completed, `neutral`, optional check *only* when there is something actionable to surface, updating that same check for the head SHA as evidence evolves. On branches that enforce awareness, the required **Awareness Check** is maintained as a completely separate Check Run.
- **Fallback and retention:** For quiet or unsupported PRs, the handoff remains accessible from the Taaro app directly. Because GitHub check retention limits how long Check Runs persist, Check Runs are treated as a live workflow surface rather than a permanent archive.

### 4.5 Lifecycle Utility Matrix

| Lifecycle Stage | Decision Taaro Shortens | What Makes It Useful |
| :--- | :--- | :--- |
| **Pick a task** | Start fresh, reuse an open change, or take an uncovered slice of an issue. | Given an issue, return explicitly linked active PRs and how they differ. A draft PR is enough to make work visible. |
| **Plan and implement** | Choose the next investigation or split work cleanly along a real interface. | Deliver relevant proposals, active work, and accepted constraints at planning time and as scope expands; fetch deeper detail on demand. |
| **Resume or hand off** | Continue from the last valid result after a context reset, tool switch, or owner change. | Pin every result to a revision and place unresolved decisions first. *(Highest-frequency solo use case to validate.)* |
| **Review and validate** | Determine what behavior changed since the last review and which evidence applies. | Link reviewed revision, incremental diff, and exact CI run. Surface a missing result only when it affects an open decision. |
| **Integrate** | Decide whether to reuse a patch, divide ownership, or sequence compatible landings. | Highlight the exact shared requirement or interface while both changes remain cheap to revise. |
| **Release** | Identify which release or environment contains a merged change. | Join a recorded release or deployment status to its exact revision (supplied by the deployment system when external). |
| **Maintain** | During a regression, locate the recent changes and design rationale worth inspecting first. | Trace a deployed revision back to candidate PRs, tests, and recorded rationale; confirm root cause via debugging or reproduction. |

### 4.6 Selective Retrieval and Delivery Channels

For a single fresh agent session, the payoff is **selective retrieval**. Assume the new session should spend at most one screen reading handoff state: give it the issue's acceptance condition, the last reviewed revision, what changed afterward, the live review thread, and any pending job—each with a source link. Additional details are pulled on demand. Dumping entire chat transcripts bloats context and risks reintroducing abandoned plans. A warm session that already holds current evidence receives no repeated message.

Taaro supports three delivery modes depending on workflow maturity:

1. **Copyable brief:** Formatted for ordinary chat windows with zero client integration.
2. **Read-only CLI query:** For on-demand retrieval by any agent or developer (e.g., `taaro context --pr 247`).
3. **Runtime push (enforced work):** The runtime supplies relevant items directly to each worker as it enters scope, even across automatic subagent delegation. A lead agent can share a single lookup across its swarm, while each acting worker still receives the evidence in its own current context—requiring no reporting prompts from workers.

---

## 5. Cross-Team Overlap Detection and Rework Prevention

### 5.1 Early Detection Scenario

Two developers ask separate coding tools to fix duplicate diagnostics emitted after a compiler import failure:

- **Maya's draft PR `#318`** suppresses the second diagnostic in the renderer.
- **Jules's draft PR `#322`** prevents the parser from emitting the duplicate in the first place.

Both patches touch different files and would merge cleanly, yet both explicitly link **issue `#309`**. If Jules sees Maya's failing fixture while selecting a fix strategy, the team can agree which layer owns the invariant, reuse one reproducer, and ship a single implementation. Discovering the overlap at merge time recovers almost none of the wasted effort.

### 5.2 Defensible Comparisons and Non-Disruptive UX

Taaro surfaces only claims it can factually defend:

> *"PRs #318 and #322 both target issue #309. One changes the renderer and one changes the parser. Compare the diffs."*

- Authors remain fully in control: they can mark the changes complementary, combine them, or dismiss the comparison.
- Acknowledging a pair silences repeat alerts across subsequent routine pushes.
- No branch is locked, no task is reassigned, and nobody waits for permission to keep working.
- A shared issue proves a shared goal—it is weak evidence that either patch should be abandoned. Because GitHub already exposes linked PRs on an issue, the real product test is whether delivering a concrete diff comparison *at the right moment* actually changes decisions.

### 5.3 Semantic and Contract Conflicts Beyond File Paths

Some costly conflicts share neither an issue nor a file path. Suppose an API team changes a token-expiry field from seconds to milliseconds while a client team adds a cache assuming seconds. Both branches can pass CI independently and merge cleanly.

A later phase of Taaro can address this by comparing a **versioned contract** against its **declared consumers**, flagging the unit change, and suggesting an existing compatibility test on the combined pair. Doing so requires a trustworthy contract definition, an explicit consumer mapping, and either an executable test or human judgment. File-path overlap alone cannot solve this—and noisy file-level collisions (such as two unrelated PRs updating a shared lockfile) must be suppressed.

### 5.4 The Discovery Timing Ceiling and Ephemeral Intent Hints

Time of discovery sets an upper bound on rework savings:

- A GitHub-only app learns about overlapping work only when a visible signal lands (a linked issue, branch push, or draft PR). If two agents complete duplicate patches in private local worktrees before either pushes, no retrospective card can recover that effort.
- For teams that frequently hit pre-push collisions, an **optional local client** can broadcast a short-lived statement such as *"working on `#309` from revision `X`."*
- Intent hints carry a timestamp and short TTL expiry. They are strictly advisory hints—if a laptop goes offline, a stale hint never reserves or locks a task.
- Mixed adoption degrades gracefully: a connected team still sees published GitHub activity from unconnected teams, while two unconnected, unpublished sessions naturally remain invisible to each other.

### 5.5 Pilot Restraint

Prioritize comparisons that leave someone a concrete action and enough time to take it. The initial pilot matches only **explicit issue links** and **declared proposal scopes** within a single repository. Measure how often those signals occur before investing in semantic text matching—two PRs with nearly identical phrasing are often an implementation and its documentation. Shadow-testing candidate matchers against maintainer judgments offline is far cheaper than training an organization to ignore noisy alerts.

---

## 6. Epistemology, Provenance, and Runtime Integration

### 6.1 What Taaro Can Know vs. Remaining Limits

Taaro's credibility depends on never overstating what GitHub events, Git metadata, or runtime adapters actually prove. These distinctions must survive every presentation of a fact, including the structured payload delivered to agents.

| Question | Available to a GitHub App (with relevant access) | Extra Evidence Needed or Remaining Limit |
| :--- | :--- | :--- |
| **Who was behind this change?** | PR opener, recorded reviewers, and Git `author` / `committer` fields (each answering a different question). | Authenticated clients or explicit session links can report participants. Git author fields are configurable; shared credentials or bot accounts cannot identify the human operator or attribute lines to specific agents. |
| **What goal does it advance?** | Linked issues, PR descriptions, review decisions, and milestones. | External tracker connectors can supply private tickets for authorized readers. Inferred goals must be offered privately as editable/removable suggestions. |
| **What did the swarm do?** | Provider-specific metadata may link a session to a commit; standard Git pushes contain no universal worker tree. | Local runtimes may report worker roles and parentage for participating sessions. Missing events leave gaps, and a completed run alone does not prove which edits survived review. |
| **What work is underway?** | Visible branches, pushes, draft PRs, and linked issues in accessible repos. | Unpushed local work requires opt-in client signals. A branch pushed yesterday may already be abandoned. |
| **Which proposals affect this work?** | Design PRs, issues, and versioned proposal files with revision, review state, and declared scope. | Requires a configured source and evaluable scope rule. Unshared discussions or undeclared design dependencies remain invisible. |
| **What was tested and shipped?** | Check results naming a job and revision; GitHub deployment statuses reporting an environment and outcome. | External CI/CD must report back or connect directly. A green job never proves behavior it did not test; production health and incident causes require separate operational evidence. |

### 6.2 Claim-Level Provenance Rules

Provenance is tracked at the level of each individual claim:

1. **Observed platform event:** e.g., *"Maya opened #247"* — attributed to the exact GitHub event and timestamp.
2. **Attributed human rationale:** e.g., *"We chose the lexer so every renderer gets the same span"* — linked directly to Maya's review comment.
3. **Taaro suggestion:** e.g., *"These PRs may duplicate each other"* — explicitly labeled as a suggestion with its match reason, dismissal control, and expiry.

If no rationale was recorded anywhere, Taaro omits it (or displays *"No rationale recorded"* when explicitly queried). A maintainer who knows the reason can record it once on the original discussion rather than inside a proprietary Taaro field.

### 6.3 Connecting an Agent Runtime

- **Advisory handoffs:** Any tool can consume copyable briefs or call a read-only context CLI without deeper integration.
- **Enforced awareness:** Requires an adapter sitting directly on the mediation path that grants the agent access to protected operations. A voluntary tool call, read-only CLI, or system-prompt instruction leaves compliance up to the model and cannot establish enforcement.
- **Prompt-injection and privilege boundary:** Delivered source excerpts and proposal text must arrive strictly as **quoted project data**. They must never expand the agent's permissions or override repository policy.
- **Honest capability labeling:** Modern coding agents increasingly expose lifecycle hooks (for example, Claude Code documents hooks for tool use and subagent lifecycle events). Even so, a compliant integration must demonstrate delivery to the *exact acting worker's context* and coverage across *every* in-scope write route—including shell writes, subprocesses, and delegated tools. Clients exposing observation alone must be explicitly labeled as observation-only.
- **Minimal-data transport:** Filter locally inside the adapter, authenticate the runtime, and exchange opaque work and worker identifiers. Enforced awareness does not require sending raw user prompts or full session transcripts to Taaro.

---

## 7. Trust, Security, and Operational Reliability

### 7.1 Least-Privilege GitHub App Permissions

Installation operates **repository by repository** so teams can pilot on a single repo without organization-wide exposure:

| Scope | Permission | Purpose |
| :--- | :--- | :--- |
| **PR Pilot Baseline** | `Metadata: Read` | Repository identity and webhook routing |
| **PR Pilot Baseline** | `Pull Requests: Read` | PR state, diffs, review threads, head SHAs |
| **PR Pilot Baseline** | `Issues: Read` | Linked goals, labels, proposal issues |
| **PR Pilot Baseline** | `Checks: Read & Write` | Read CI verdicts; publish neutral handoff checks and (when enabled) required awareness checks |
| **Optional Expansion** | `Contents: Read` | Added only when needed for versioned RFC/proposal files, push history, or direct-commit views |

- **No default LLM processing of private code:** Baseline handoff cards are assembled deterministically from GitHub records and structured templates, with model processing of private source code **off by default**.
- **Transparent exposure disclosure:** Even without cloning a repository or invoking an LLM, authorizing a GitHub App to read PRs exposes sensitive descriptions, comments, and diffs; documentation and onboarding must state this plainly.

### 7.2 Git History and Fork Edge Cases

Real repositories rewrite history and accept external forks. Taaro handles these explicitly:

- **Stable navigation vs. exact snapshots:** Navigate by stable PR identity, but anchor every check, diff, and excerpt to its exact commit SHA. Refresh the handoff on head or review-state changes while keeping historical checks attached to the SHAs they actually tested.
- **Force-pushes:** Invalidate incremental comparisons tied to the overwritten prior head.
- **Squash merges:** Multiple commits collapse into one; record the merge association reported by GitHub and explicitly mark unverifiable commit mappings as unavailable rather than guessing.
- **Synthetic merge revisions:** CI runs executed against synthetic merge commits (e.g., PR merge preview refs or merge groups) must be labeled as such.
- **Fork pull requests:** GitHub can return an empty PR list for check runs triggered on fork pushes; resolve associations through the base PR and degrade cleanly to partial information if repository access ends.

### 7.3 Access Control and Visibility Boundaries

- **No cross-visibility leakage:** Never copy a private upstream title, file path, proposal excerpt, or session detail onto a more public PR card or check summary.
- **Live authorization on details pages:** Verify the reader's current GitHub repository permissions whenever they open the richer Taaro details view, not merely at ingestion time.

### 7.4 Webhook Processing, Degraded Operation, and Data Lifecycle

- **Loop prevention and idempotency:** Ignore Taaro's own check webhook events when deciding whether to refresh. Verify webhook signatures, acknowledge deliveries quickly, deduplicate by GitHub delivery ID, and reconcile current PR state after missed or out-of-order events.
- **Observation timestamps:** Publish every fact alongside the time it was observed.
- **Degraded operation during outages:**
  - *Handoff cards:* Fall back cleanly to direct GitHub links.
  - *Protected operations requiring fresh evidence:* Wait for verification (fail closed).
  - *Unprotected work:* Continues unaffected.
  - *(Already-green required GitHub checks remain subject to the last-validation freshness limit noted in [Section 3.5](#35-trust-boundaries-branch-protection-and-known-limits).)*
- **Minimal retention and clean removal:** Keep stored state small, expire cached excerpts automatically, and purge affected repository data promptly upon app uninstallation or revocation of access.

### 7.5 Anti-Surveillance Metrics

Taaro reports **repository-level outcome measures** only (e.g., handoff reuse, wait reduction, overlap outcomes). It deliberately excludes per-person agent counts, prompt volumes, or lines-authored leaderboards, which would incentivize the very churn and busywork the tool aims to eliminate.

---

## 8. Ecosystem Positioning and Adoption Strategy

### 8.1 Relationship to Existing Tools

| Existing System | What It Already Solves Well | Where Taaro Complements It |
| :--- | :--- | :--- |
| **GitHub concurrent agent sessions & Copilot commit tracing** | Linking commits to agent session logs and recording human initiators as co-authors. | Links to those records under their existing access controls and focuses on the *open decisions crossing* multiple sessions and tools. |
| **Entire CLI** | Capturing searchable commit-tied checkpoints and resuming sessions. | Links to checkpoint history while adding cross-worker provisional exchange, proposal awareness, and PR-level handoffs. |
| **GitHub Merge Queues** | Validating whether a queued PR combination passes required checks right before landing. | Surfaces shared fixtures, duplicate implementations, and interface changes *earlier*, while plans are still cheap to adapt. *(Note: even a perfect early alert has no value if both teams would still take the exact same next step.)* |
| **Deployment Dashboards** | Reporting which revision is live in production (and mapping components to artifacts in monorepos). | Joins deployment status into maintenance/regression tracing and links out to existing dashboards before building new release UI. |

### 8.2 Target Adopters and Solo-First Onboarding

1. **Start where handoff pain is already felt:** Target a compiler, SDK, or API repository with explicit producer/consumer boundaries—or a solo maintainer running multiple agent sessions—where someone recently pasted a fixture across terminals, repeated debugging after a reset, or waited on a sibling branch. That gives a real workaround and a fair comparison baseline.
2. **Make the first win possible alone:** Connect one repository and one supported tool, then run an ordinary task. If an accessible prior run holds a relevant reproducer, surface it, rerun it on the current revision, and display its provenance and outcome. For a greenfield repository, provide a clearly labeled two-worker example runnable both with and without Taaro.
   - **Proposed onboarding target:** First useful result within **5 minutes** of installation.
   - **Measurement discipline:** Track *real-repository activation* strictly separately from demo completion.
3. **Let useful work pull in the second user:** Place the shared fixture or combined compatibility failure directly where the teammate already reviews code, accessible via normal repository permissions. Keep quiet when nothing is actionable. Track whether the recipient voluntarily uses Taaro on a real task the following week.

### 8.3 Open Protocol for Tool Builders

Taaro offers an open, progressive integration path—published alongside a **protocol specification**, a **reference adapter**, and **conformance tests**:

1. **Level 1 — Read & Follow:** Request scoped context and subscribe to an existing result or check.
2. **Level 2 — Exchange & Subscribe:** Publish versioned provisional artifacts, subscribe to revisions, and join eligible running checks.
3. **Level 3 — Enforced Awareness:** Claim enforced-awareness compliance *only* after passing conformance tests verifying worker-scoped context delivery and full protected-action mediation.

IDEs and agent vendors gain interoperability with work happening outside their own sessions while keeping full ownership of their editor, models, task scheduler, and customer relationship.

### 8.4 Business Model Alignment

- Keep local and single-repository adoption inexpensive and low-friction.
- Test willingness to pay around genuine operational value: managed private coordination, shared test runners, compliance/retention controls, and organization-level administration—priced against real hosting and compute costs.
- **Avoid per-worker pricing:** Charging a fee for every spawned worker gives teams a financial incentive to suppress the exact parallel concurrency Taaro is built to make productive. Durable adoption is measured by teams choosing Taaro voluntarily on their next coupled change.

---

## 9. Implementation Roadmap

### Phase 1 — One Useful Exchange (MVP)

Deliver the narrowest end-to-end loop that proves real utility:

- **Scope:** One repository, one supported agent runtime, two independent worktrees, and one existing compatibility test.
- **Core flow:**
  1. Producer worker emits a versioned provisional fixture.
  2. Adapter publishes it automatically and delivers it to the consumer worker.
  3. Consumer runtime enforces delivery of any relevant proposal revision before the consumer's next affected action.
  4. PR handoff card surfaces the exact fixture revision and resulting compatibility check.
- **Zero prompt tax:** The developer issues the exact same task prompt they would have used without Taaro.
- **Solo path:** A fresh session opened the next day on the same repository reuses that exact fixture and reruns it against the current checkout.

### Phase 2 — Portability and Enforcement Proof (Validated Separately)

- **Cross-client portability:** Move the consumer worker to a second agent client and repeat the exchange without altering the task prompt or either client's planner.
- **Branch-protected enforcement trial:** Deploy the GitHub App required Awareness Check on an opt-in branch requested by maintainers.
- **Adversarial & edge-case exercises:** Explicitly verify stale proposals, missing delivery receipts, uninstrumented contributions, and quiet unrelated typo fixes.
- **Bounded combined CI:** Trigger targeted combined-revision tests through the team's own CI pipeline with strict speculative caps and cancellation of superseded runs.

### Phase 3 — Evidence-Driven Expansion

Expand into broader build-cache sharing, cross-OS/platform runner routing (e.g., Windows/Linux release runners), and versioned contract-to-consumer matching **only** after Phase 1 and Phase 2 telemetry shows which delays actually dominate real delivery times.

---

## 10. Validation and Experimental Design

Taaro's claims must be evaluated empirically rather than assumed.

### 10.1 Counterfactual Baseline Protocol

To avoid flattering comparisons against artificially weak baselines:

- Recruit solo developers who routinely restart agent sessions and teams whose PRs regularly overlap.
- Select distinct but comparable historical tasks from each participating repository, explicitly including small, straightforward fixes.
- **Equalize the workflow first:** Remove incremental-documentation commit instructions in *both* control and treatment arms.
- Give both arms the same capable agents, comparable starting context, parallel worktrees, native build caches, CI pipelines, budgets, and any existing session-resume tools the team already uses.
- Permit the baseline arm to use its best existing coordination practices—including early draft PRs and manually shared fixtures.
- Alternate or randomize task assignments within each repository so no participant sees the same task twice.

### 10.2 Evaluation Matrix

| Experiment | Primary Observations | Decision Rule |
| :--- | :--- | :--- |
| **Delivery speed under concurrency** | Run comparable dependent tasks with 1, 2, and 4 workers. Measure end-to-end elapsed time to the same accepted, jointly tested change, plus downstream wait time, churn rework after artifact revisions, human involvement, and compute cost. | Expand only where Taaro beats the team's best existing coordination. Inspect the slowest small fixes alongside the median. Lower compute cost at identical wall-clock time supports a cost claim, not a speed claim. |
| **Adoption** | Time to first confirmed reuse on a real task, first second-client consumption, 4-week repeat usage, and the specific setup step causing abandonment. | Separate demo completion from voluntary return on real work. Prioritize integrations users actually come back to. |
| **Enforced awareness** | Adversarial scenarios: pre-work draft proposal, mid-task revision on an unchanged PR head, delegation to a fresh worker, scope expansion into a declared consumer, simultaneous starts, unchecked write route, stale receipt, and unavailable index. | Every controlled action must either follow delivery of current applicable evidence (as of validation) or be held. Unrelated typos pass with zero human acknowledgments. Log bypasses and added latency separately. |
| **Fresh-session or reviewer handoff** | Elapsed time from receiving the PR to taking a correct next action. Before assignment, a maintainer records acceptable answers and the evidence each requires. Include failures and timeouts in results. | Target at least **20% lower median time** with comparable correctness and no meaningful increase in rework. Evaluate solo and team cohorts separately. |
| **Overlapping work** | Across comparable periods with comparison enabled vs. disabled, count eligible PR pairs, timely alerts, changed decisions, and every interruption; interview both authors on what was reused or avoided; sample unflagged pairs for costly misses. | Expand only if confirmed time saved exceeds time spent reading, dismissing, and administering alerts. Treat sparse event counts as inconclusive rather than proof. |
| **Small fixes and overall delivery** | Author time, unsolicited notices per small fix, new required manual steps, model cost, CI runs, and ready-for-review-to-merge latency (plus lead time to production and follow-up fixes where deployment evidence exists). | Keep required manual steps at **zero** for unrelated fixes. Investigate any slowdown or extra CI runs on small fixes prior to rollout. Claim a shipping gain only if end-to-end time improves without quality loss. |

### 10.3 Measurement Discipline

- **Treat thresholds as investment gates:** The 20% handoff improvement figure is a proposed investment threshold, not a guaranteed outcome. Report sample sizes, spreads, and uncertainty.
- **Test simpler alternatives first:** Benchmark a plain, deterministic list of well-chosen links against any LLM-synthesized summary before paying model latency and cost for prose synthesis.
- **Separate time dimensions:** Track reviewer minutes, agent runtime, model cost, and calendar delay independently. Saving ten minutes across each of three parallel workers can lower compute spend while barely shifting the actual ship date.

### 10.4 Decisive Pilot Criteria

Ship the **fixture exchange first**, with **required awareness** and the **revision-anchored PR handoff card** attached directly to that workflow:

- **Decisive technical observation:** A downstream worker starts useful work before the producer finishes, adapts cleanly to a mid-task proposal revision, and reaches the verified combined result faster than the team's existing coordination workflow.
- **Strongest adoption signal:** Developers voluntarily reach for Taaro on the next project where two pieces of work depend on each other.
