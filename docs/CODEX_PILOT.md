# Codex CLI awareness pilot

The pilot controller runs a Codex CLI turn for one existing UTF-8 text file. It asks Taaro for current evidence before supplying the file to Codex, validates the response against the operator-selected path, and checks the proposed write before changing the worktree. The operator chooses the target and task.

## Run the controller

Use Python 3.10 or later and an authenticated Codex CLI on Linux. The pilot was exercised with Codex CLI 0.170.0. Start or connect to a [Taaro server](WORKFLOW.md). The controller accepts verified HTTPS and direct loopback HTTP. Set `TAARO_API_TOKEN` in the environment if the server requires it.

From the Taaro checkout, select an existing file in your pilot worktree:

```sh
python3 integrations/codex/taaro-codex.py \
  --worktree /path/to/pilot-worktree \
  --repo owner/repo \
  --path path/to/file.txt \
  --task 'Correct the typo on the first line.' \
  --server http://127.0.0.1:8080
```

The CLI starts an ephemeral, read-only Codex turn in a private scratch directory. It sends the selected file, operator task, and applicable quoted evidence to the configured Codex model. Native shell execution, native agent delegation, hooks, apps, plugins, and web search are disabled for this invocation. The controller leaves changes in the worktree for review. It supports a single selected file up to 64 KiB.

- **Default text edit.** Codex returns a full replacement for the selected path. The controller verifies the receipt and applies the replacement only after a successful write evaluation.
- **Delegation.** Add `--delegate` to have a planner propose a bounded task for a child. Taaro evaluates the plan before launch. The controller starts the child with a separate worker ID and context, registers its parent, and delivers evidence to the child before issuing its own receipt. The child can act on the same selected file.
- **Staged shell edit.** On Linux with Bubblewrap and unprivileged namespaces available, add `--edit-mode shell`. Model-generated POSIX shell and subprocesses run in an isolated stage without network, host credentials, or the original worktree. Edit the staged file at `/work/target` in place. Redirection is supported. Utility modes that replace the file by renaming over it cannot replace the staged bind mount. The controller rechecks the write after the shell exits and copies back only the selected text file on success. Combine this flag with `--delegate` to run the shell stage for the child.

## Observe a held write

1. Select a trial file with no applicable evidence and start a controller run.
2. After it prints `Starting generation 1 with 0 quoted evidence item(s)`, add an applicable proposal that declares that file in its scope. Leave the worktree unchanged.
3. The write evaluator should hold the first response. The controller discards it, starts a distinct ephemeral Codex thread, and includes current evidence in that thread.
4. The controller records a receipt after the fresh thread returns successfully. Verify that the final worktree file reflects the updated evidence and that the log records a different thread and context generation.

For example, start with a trial file `settings.py` containing `timeout_default = 5`. Ask the controller to add a `timeout_unit` string, using milliseconds without a proposal and following the proposal when one applies. From the Taaro checkout, publish the proposal after the first generation starts:

```sh
bin/taaro proposal upsert --repo owner/repo \
  --key rfc/pilot-timeout --title 'Timeout units' --state accepted \
  --paths settings.py --summary 'The timeout unit string must be seconds.'
```

Repeat with `--delegate --edit-mode shell` using a separate pilot invocation. To exercise a child subprocess, ask that the child use a nested `/bin/sh -c` with `printf` and redirection to append to `/work/target`. Verify that the planner and editor log distinct worker IDs and receipts, and that only the selected file changed.

Run the offline controller, transport, and Linux shell stage tests from the Taaro checkout:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s integrations/codex -p 'test_*.py'
```

A server lookup failure, inaccessible source, unsuccessful Codex turn, or invalid shell stage stops the operation. The controller supports Git worktrees owned by the operator. Other editors, local processes, Git publication, and arbitrary multi-file commands live outside its mediation. Use the [fixture exchange workflow](PROTOCOL.md) for producer and consumer artifact checks.
