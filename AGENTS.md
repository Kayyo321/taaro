# Working on Taaro

Taaro is specified and implemented here as a coordination layer for coding agents. This checkout contains Go source (`cmd/`, `internal/`), Markdown documentation (`README.md`, `docs/`), and artwork (`art/`). Start with the owning section of [docs/PROJECT.md](docs/PROJECT.md) for product behavior, [docs/WORKFLOW.md](docs/WORKFLOW.md) for local Go and GitHub App setup, and [README.md](README.md) for the public overview and entry points. The images in `art/` are referenced by relative path from [README.md](README.md) and [docs/PROJECT.md](docs/PROJECT.md).

## Where to make a change

| If the change concerns | Read together |
| --- | --- |
| Project adoption or creation | Exercise `taaro init` in an existing worktree and `taaro new` from a parent directory. Confirm both reach the same project-scoped context query. See [project setup](docs/WORKFLOW.md#using-taaro-in-your-project) and [adoption strategy](docs/PROJECT.md#8-ecosystem-positioning-and-adoption-strategy). |
| Go server, CLI, webhooks, or local dev workflow | `cmd/`, `internal/`, [docs/WORKFLOW.md](docs/WORKFLOW.md), and [visibility and event rules](docs/PROJECT.md#7-trust-security-and-operational-reliability). |
| Fixture publication, subscriptions, or CI reuse | [System model](docs/PROJECT.md#2-core-system-model-and-capabilities) and [roadmap](docs/PROJECT.md#9-implementation-roadmap). Check which phase introduces the capability. |
| Proposal matching, delivery receipts, or branch protection | [Required awareness](docs/PROJECT.md#3-required-awareness-of-active-work-and-proposals), [runtime integration](docs/PROJECT.md#6-epistemology-provenance-and-runtime-integration), and the awareness trial in [validation](docs/PROJECT.md#10-validation-and-experimental-design). |
| What appears on a PR or in a worker’s context | [Handoff cards](docs/PROJECT.md#4-revision-anchored-handoff-cards-and-lifecycle-utility), [overlap detection](docs/PROJECT.md#5-cross-team-overlap-detection-and-rework-prevention), and [visibility rules](docs/PROJECT.md#7-trust-security-and-operational-reliability). |
| Adoption, timing claims, or pilot scope | [Adoption strategy](docs/PROJECT.md#8-ecosystem-positioning-and-adoption-strategy), [roadmap](docs/PROJECT.md#9-implementation-roadmap), and [evaluation criteria](docs/PROJECT.md#10-validation-and-experimental-design). |

Keep detailed rules in the specification and update the README when the reader-facing contract changes. Preserve section anchors used by other documents, or update the inbound links in the same change.

## Review decisions against the contract

- **Artifact identity.** Follow a fixture from its producer revision to its consumer. A revised fixture gets a new immutable version. A resumed worker reruns it on the current checkout. For combined validation, identify both tested revisions. For job reuse, check inputs, toolchain, flags, and environment.
- **Delivery and action ordering.** Identify the actual worker, context generation, protected operation, and evidence versions. After missing or stale evidence is delivered, the worker gets a fresh turn. A parent’s receipt cannot cover a delegated worker. A failed lookup leaves protection pending. A complete lookup with no matches clears silently.
- **GitHub conclusions.** The optional handoff card can conclude `neutral`. A required Awareness Check succeeds only for a complete evaluation of the current candidate, including a merge-group revision when applicable. Keep prior results attached to their own SHAs.
- **Visibility and authority.** Check the audience of the destination before exposing an upstream title, path, or excerpt. Treat retrieved proposal text as quoted data. Attribute repository decisions to authoritative sources, suggestions to Taaro, and test outcomes to the recorded job.
- **Routine fixes.** Walk a CLI spelling correction through any proposed notice or gate. With a complete lookup and no relevant match or open decision, the author should encounter zero additional manual steps. Also consider a one-line public contract change with a registered consumer.
- **Pilot boundaries.** The first exchange uses one repository, one supported runtime, two worktrees, and one existing compatibility test. Keep second-client portability, branch protection, and bounded combined CI in their respective trial phase unless the requested change revises the roadmap.

## Writing and verification

Describe a component through its inputs, decision, and observable result. Use proposal lifecycle terms such as `draft` or `superseded` where behavior depends on them. Keep session recaps, progress banners, and dated work summaries out of repository documentation. Identify fictional PRs and timing examples as illustrative, and present pilot thresholds as targets.

Run these checks from the repository root when changing code or documentation.

1. Run `git status --short` to identify edited and newly created files. Read new documents directly because an unstaged new file is absent from `git diff`.
2. Review `git diff -- README.md docs/PROJECT.md docs/WORKFLOW.md AGENTS.md` and run `git diff --check` for patch whitespace. For staged changes, also use `git diff --cached` and `git diff --cached --check`.
3. Inspect link and image references with `rg -n 'href=|src=|\]\(' README.md docs/PROJECT.md docs/WORKFLOW.md AGENTS.md`. Resolve file paths relative to the referring document and verify fragments against the destination headings.
4. Read the changed scenario end to end. Confirm that the README, normative rule, workflow doc, roadmap phase, and evaluation criterion describe the same behavior.
5. For Go code changes, run `make test` (`go test -race ./...`) and `make build` (`bin/taaro-server` and `bin/taaro`). Keep `.env`, `.secrets/`, and `.data/` untracked.
