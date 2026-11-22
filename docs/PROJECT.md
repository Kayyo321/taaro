<p align="center">
  <img src="art/taaro.png" alt="Taaro emblem" width="96" />
</p>

# Taaro product and architecture specification

This document defines the proposed behavior of Taaro for repository maintainers, runtime integrators, and pilot teams. Sections 1–8 describe the system contract. Sections 9–10 define delivery phases and the evidence required to expand the pilot.

## 1. Overview and Motivation

A compiler and its editor extension can be developed in parallel once both workers have an example of the diagnostic they must support. A developer returning to a bug can resume investigation once they have its input, command, and last observed result. In both cases, the useful handoff may exist before a branch is ready for review.

Taaro lets a producer publish that intermediate result, records which worker consumed which version, and delivers relevant changes before the consumer acts again. A pull request (PR) gives reviewers a stable place to find the evidence as revisions change.

The intended users are developers moving across sessions and teams maintaining coupled components such as an SDK and its clients. A self-contained change finished in one session offers little opportunity for reuse. For a team waiting on reviewer availability, the expected benefit depends on how much of the delay comes from reconstructing context.

Keep ordinary commit boundaries for review, bisecting, and rollback. Record rationale once in the discussion that owns the decision. The evaluation baseline gives teams these practices as well as parallel worktrees, early draft PRs, and existing build caches.

## 2. Core System Model and Capabilities

| Owner | Responsibilities |
| --- | --- |
| Producer adapter | Filter locally and publish an immutable, task-scoped artifact version. Sharing uncommitted material requires adapter opt-in and repository-approved scope. |
| Coordination service | Index visible work and configured proposals. Record requests, producing revisions, consumers, and subsequent changes. Notify subscribers when a requested result becomes available or changes. |
| Consumer adapter | Deliver the relevant version into the acting worker’s context. Mediate protected actions and issue delivery receipts. |
| GitHub App | Associate evidence with repository objects and publish revision-specific checks. |
| Existing planner and CI | Schedule or suspend workers, apply budgets, execute jobs, and decide what can land. |

### Artifact exchange

In the illustrative Neura workflow, a compiler worker publishes a golden fixture for a secondary diagnostic span. An editor worker subscribes to that output and tests its decoder against the provisional fixture. When the compiler changes the encoding for an absent span, the service can identify the affected consumer from the recorded version. A resumed session retrieves the fixture, its source revision, and the original command, then reruns it on its own checkout.

Time saved depends on the dependency. With six minutes to produce the fixture, twelve further minutes of compiler work, eight minutes of editor work, and four minutes of joint validation, serial execution takes 30 minutes. Starting the editor at fixture publication gives a critical path of 22 minutes. These are illustrative timings. A draft PR sharing the same fixture could yield the same overlap. Publication overhead and rework from fixture revisions count against any measured gain.

### Check identity and combined validation

Workers may follow an eligible in-flight job when the build system identifies the same inputs, tree, toolchain, flags, and environment. A different tree, unknown environment, or flaky test requires a fresh run. Attribute each verdict to its job and revision. An agent diagnosis is a lead for investigation.

For declared dependencies, request a targeted compatibility test against the exact producer and consumer revision pair in a disposable environment. Return the result to both workers, cap speculative jobs, and cancel superseded runs. Existing caches supply reusable outputs and the merge queue validates landing candidates. A later runner integration could route a request for a Windows install-and-uninstall result against a specific build through the team’s existing permissions and budget.

## 3. Required Awareness of Active Work and Proposals

A supported runtime may authorize a protected action only after a complete lookup and delivery of all current, known evidence relevant to the actual action. Delivery establishes what reached a particular worker at validation time. Assess understanding and compatibility from the resulting plan, patch, or test.

### Sources, scope, and authority

Repositories designate design PRs, labeled issues, or versioned RFC files as proposal sources. Preserve their identifiers, owners, and review states. Match accessible draft PRs, connected workers’ active scopes, and proposals through explicit issue links, declared components or paths, or named interfaces and registered consumers. Similar language and adjacent directories can support optional lookups only.

Deliver the substance relevant to the decision, source link, version, lifecycle state, and match reason. A draft makes an alternative visible while work proceeds. An accepted proposal carries repository-defined authority. Maintainers may configure a separate compatibility test for its contract. Withdrawal or supersession ends applicability and triggers an update to prior recipients. Only designated maintainers or authoritative sources may change proposal state.

### Action lifecycle

1. **Task start or delegation.** Register known active scope and query overlaps in one ordered, atomic operation so concurrent starts cannot both see an empty scope. Deliver matches to the worker preparing the plan, including a delegated worker. Allow discovery when scope is unknown and resolve it as targets emerge.
2. **Scope entry.** Return context alongside a guarded read. Before a guarded write or plan submission, evaluate the actual target. If evidence is missing or stale, hold the action, insert the relevant update as quoted project data, and issue a receipt after insertion succeeds. Give the worker a fresh turn to reconsider the action. Discard the pending edit selected under the earlier context.
3. **Evidence or context change.** Track matching-set membership as well as item versions. A new proposal can invalidate a receipt on an unchanged head. Deliver the delta before the next affected action. Restore relevant evidence after a reset, compaction that drops it, or delegation. Unrelated revisions require no interruption.
4. **Publication.** Re-evaluate the actual diff. Bind the verdict to the candidate revision, scope, evidence versions, and trusted delivery records. Revisit changes affected by newly discovered overlap.

Receipts are scoped to `(worker_id, context_generation)`. A complete lookup with no matches clears automatically. Missing access, an unavailable index, or unverified delivery keeps the action pending. When an applicable source is private, request authorized review without revealing its contents. Coverage extends only to mediated operations and indexed sources. Disconnected local work and undeclared dependencies can escape detection.

### Branch protection and freshness

Maintainers may require a dedicated **Taaro Awareness Check** and restrict bypasses. Evaluate the current candidate head SHA, including the combined merge-group SHA when a queue is in use. Issue `success` only after a complete, current evaluation with the required evidence. GitHub can accept `neutral` and `skipped` for required checks, so both conclusions are invalid for incomplete evaluations.

A supported worker can review a contribution from an uninstrumented editor before merge. The receipt establishes delivery to that reviewer from that point forward. Apply protection by change scope where authorship is uncertain.

Refresh configured sources, record the indexed snapshot and validation time, invalidate affected checks on new evidence, and measure propagation delay. A proposal may change between validation and merge. Closing that interval would require proposal admission and branch landing to share an ordering mechanism.

## 4. Revision-Anchored Handoff Cards and Lifecycle Utility

Navigate by PR identity. Pin revision-specific facts to a commit SHA. Issues identify broader goals and can link to multiple PRs in either direction. Report completion at the scope the evidence supports. An accessible direct push can have a commit-only view, and an unlinked change can stand alone.

A handoff should fit on one screen. Put the next decision, acceptance condition, reviewed revision, incremental diff, unresolved thread, and relevant job within reach. Link each item to its source and fetch more detail on demand. Suppress repeated delivery to a session that already has the evidence.

### Example review handoff

| Evidence for illustrative PR #247 | What the reviewer can do |
| --- | --- |
| Issue #231 asks that a multiline unterminated string point to its opening quote. | Inspect the multiline fixture and acceptance condition. |
| The lexer at `91bd80a` records the opening span. The diagnostics job passed at `6f150de`. The job for `91bd80a` is queued. | Follow the current job and inspect the diff since the reviewed revision. |
| An unresolved thread on `6f150de` asks which span appears after a trailing backslash at end of file. | Run that input and answer the original thread. |

### Presentation and retrieval

For an actionable handoff, the App publishes a completed, optional `neutral` Check Run with a Markdown summary and details URL. Update the same card for that head as evidence evolves. The required Awareness Check is a separate Check Run. Keep details available in the App for quiet or unsupported PRs and account for check retention when presenting historical records.

Offer a copyable brief, a proposed read-only query such as `taaro context --pr 247`, and runtime delivery for mediated work. A parent can share a lookup across workers. Each acting worker still requires delivery in its own current context.

During task selection, show explicitly linked work. During implementation, deliver relevant changes at scope entry. At integration, show the common requirement that owners can divide or test. After release, link the deployed revision to its PRs, checks, and recorded decisions. Debugging or reproduction is still necessary to establish a regression’s cause.

A spelling fix with no open decision should generate no notice or manual step. A one-line change to a public interface can warrant delivery to a registered consumer. Base interruption on the action a recipient can take.

## 5. Cross-Team Overlap Detection and Rework Prevention

The pilot compares PRs within one repository using explicit issue links and declared proposal scope. For example, draft PR #318 suppresses a duplicate diagnostic in a renderer and draft PR #322 changes the parser that emits it. Both link issue #309. Present the common issue, the different layers, and both diffs while the authors can still reuse the fixture or agree on ownership.

Authors may mark a pair complementary, combine the work, or dismiss the comparison. Acknowledgment suppresses repeated alerts on routine pushes. A match grants no lock or reassignment rights. Suppress incidental collisions such as unrelated edits to a lockfile. Evaluate candidate matchers offline against maintainer judgments before enabling alerts.

GitHub-only discovery starts with a visible event. An optional local client can advertise an issue and base revision before push. Timestamp the advisory hint and give it a short expiry. Mixed deployments retain visibility into published activity even when local work remains private.

Later contract matching requires a versioned definition, declared consumers, and a test or reviewer capable of judging compatibility. For example, changing an expiry field from seconds to milliseconds can break a client cache even when both branches pass their own tests and edit different files.

## 6. Epistemology, Provenance, and Runtime Integration

| Claim | Evidence and limits |
| --- | --- |
| Participation | Attribute PR opening and review to platform events. Git author and committer fields are configurable. A runtime can report a linked session, role, or parent worker. Attributing a surviving edit requires evidence tying that patch to the session. |
| Goal or rationale | Use an explicit issue, PR description, or attributed review decision. Link external tickets only for authorized readers. Offer inferred goals privately as editable, removable suggestions. Omit absent rationale or report its absence when asked. |
| Active work or proposal | Include the observed branch, PR, opt-in scope signal, or configured proposal revision and observation time. Older activity alone cannot establish continued work. |
| Test, deployment, or incident | Identify the job, revision, environment, and reported outcome as applicable. External systems must supply their records. Production health and root cause require additional evidence. |

Keep provenance at claim level in both cards and adapter payloads. Platform observations carry source and time. Human rationale links to its author’s statement. A Taaro suggestion carries its match reason, dismissal control, and expiry.

Authenticate adapters, filter locally, and transport opaque work and worker identifiers. Raw prompts and full transcripts are unnecessary. Inject excerpts and proposals as quoted project data with no authority to change permissions or policy. An enforced integration must mediate every protected route it claims, including shell writes, subprocesses, and delegated tools. Label observation-only integrations accordingly. Voluntary queries and prompt instructions cannot attest delivery.

## 7. Trust, Security, and Operational Reliability

### Installation and visibility

Install per repository. The baseline handoff is assembled deterministically from accessible records. Private-source processing by a language model is disabled by default. Onboarding must disclose that PR access exposes descriptions, comments, and diffs even without a clone or model call.

| GitHub App permission | Use |
| --- | --- |
| Metadata, read | Identify the repository and route events. |
| Pull requests, read | Retrieve PR state, diffs, threads, and head SHAs. |
| Issues, read | Resolve linked goals, labels, and proposal issues. |
| Checks, read and write | Read verdicts and publish handoff or awareness checks. |
| Contents, read (optional) | Read versioned RFCs, push history, or direct-commit views when enabled. |

Restrict each output to the visibility of its destination. Private titles, paths, excerpts, and session details must stay within their authorized audience. Recheck the reader’s current repository access whenever they open the details page. Expire cached excerpts and promptly purge affected repository data on removal or revocation.

### Event processing and revision history

Verify webhook signatures, acknowledge promptly, deduplicate by delivery ID, and exclude the App’s own check events from refresh triggers. Reconcile against current PR state after missed or out-of-order events. Record observation time on every fact.

Refresh on head and review changes. After a force-push, invalidate comparisons based on replaced history. After a squash merge, record GitHub’s reported merge association and mark unverifiable commit mappings unavailable. Label synthetic merge and merge-group revisions explicitly. For fork checks with an empty PR association, resolve through the base PR and show partial information where access ends.

During outages, handoffs can fall back to GitHub links. Protected operations awaiting fresh evidence stay pending. Unprotected work continues. Previously green checks retain the freshness limit described in section 3.

Report repository outcomes such as fixture reuse, waits, and decisions changed by an overlap notice. Exclude individual prompt volumes, worker counts, and lines-authored rankings.

## 8. Ecosystem Positioning and Adoption Strategy

Start with a repository where someone already passes fixtures between tools, reconstructs a reproducer after a reset, or waits for a related branch. Connect one repository and one supported runtime. Surface an accessible reproducer, rerun it on the current checkout, and show the result and provenance. For a new repository, provide a labeled two-worker demonstration runnable with and without Taaro. The proposed onboarding target is first useful reuse within five minutes of installation. Measure real-repository activation separately from demo completion.

Link existing GitHub agent-session records, commit checkpoints such as Entire CLI records, merge queues, and deployment dashboards under their own access controls. Keep editor choice, models, task scheduling, and spending decisions with their existing owners.

### Adapter protocol

Publish the protocol, a reference adapter, and conformance tests. The proposed integration levels are progressive.

1. **Read and follow.** Request scoped context and subscribe to a result or check.
2. **Exchange and subscribe.** Publish provisional artifact versions, follow revisions, and join eligible in-flight checks.
3. **Enforced awareness.** Demonstrate delivery to the acting worker and complete mediation of protected actions through conformance tests.

Keep single-repository adoption inexpensive. Test demand for managed private coordination, shared runners, retention controls, and organization administration against their hosting and compute costs. Per-worker fees would penalize parallel work. Measure whether recipients voluntarily use the service for another real dependency.

## 9. Implementation Roadmap

| Phase | Deliverable | Acceptance evidence |
| --- | --- | --- |
| 1. Single-repository exchange | One supported runtime, two worktrees, a provisional fixture, a relevant proposal, one existing compatibility test, and a PR handoff. | With the ordinary task prompt, the consumer starts before the producer finishes, receives a proposal revision before its next affected action, and sees the fixture revision and check on the card. A fresh solo session reruns the same fixture on its current checkout. |
| 2. Portability and enforcement | Repeat the exchange with a second client. Trial the required Awareness Check on a maintainer-selected branch. Add bounded combined CI with cancellation. | Demonstrate cross-client delivery without planner or prompt changes. Exercise stale evidence, missing receipts, uninstrumented contributions, and unrelated fixes independently of the portability trial. |
| 3. Measured expansion | Consider cache sharing, cross-platform runner routing, and contract-to-consumer matching. | Select work using measured sources of delay from the first two phases. |

## 10. Validation and Experimental Design

Recruit developers who restart sessions and teams with recurring coupled changes. Use distinct, comparable tasks within each repository, including straightforward fixes. Alternate or randomize assignments so participants never repeat a task. Give both groups comparable starting context, agents, worktrees, budgets, native caches, CI, and session-resume tools. Remove mandatory narrative checkpoints in both arms. Allow early draft PRs and manually shared fixtures in the control group.

| Trial | Measurement and decision |
| --- | --- |
| Concurrent delivery | At 1, 2, and 4 workers, time the same accepted, jointly tested outcome. Record downstream wait, revision rework, human effort, and compute. Compare medians and slow small fixes with the best existing workflow. Distinguish cost savings from elapsed-time gains. |
| Awareness | Exercise a pre-existing draft, a new revision on an unchanged head, delegation, scope expansion, simultaneous starts, an unchecked write route, stale receipts, and an unavailable index. Every controlled action requires current delivery or a hold. Record bypasses and latency separately. An unrelated typo requires zero acknowledgments. |
| Handoff | Before assignment, have a maintainer define correct next actions and supporting evidence. Include timeouts and failures. Target at least 20% lower median time to a correct action with comparable correctness and no meaningful increase in rework. Analyze solo and team cohorts separately. |
| Overlap | Compare periods with and without notices. Count eligible pairs, timely alerts, changed decisions, and interruptions. Interview both authors and sample unflagged pairs for misses. Expand only when confirmed savings exceed reading, dismissal, and administration costs. Sparse observations are inconclusive. |
| Adoption | Record time to first real reuse, first second-client consumption, setup abandonment, voluntary use the following week, and repeat use at four weeks. Track demo completions separately. |
| Routine changes and release | Record author time, notices, manual steps, model cost, CI runs, and review-ready-to-merge latency. Where deployment evidence exists, also measure production lead time and follow-up fixes. Investigate slowdowns or extra CI on small fixes. Require zero additional manual steps for unrelated changes. |

Report sample sizes, spread, and uncertainty. Treat thresholds as investment criteria. Compare deterministic source links with any proposed model-generated synthesis before incurring its cost. Report reviewer minutes, agent runtime, model cost, and calendar delay independently.

The pilot succeeds technically when a consumer can start early, adapt to a revised proposal, and reach a verified joint result faster than the team’s existing workflow. Voluntary reuse on the next coupled change is the adoption criterion.
