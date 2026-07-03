---
name: post-work-review
description: "Use from Codex CLI to run a bounded isolated reviewer gate on current git work before commit or PR. Use when the user says review して仕上げて, post-review, finalize, コミット前確認, 二重チェック, or post-work-review."
metadata:
  short-description: Bounded isolated reviewer gate
---

# post-work-review

Run the installed bash driver and delegate review work to fresh isolated
subagents. The main agent must not review its own code.

## Hard rules

- Default backend is `bounded-isolated-reviewer`.
- Do not call `codex review` in the default path.
- Do not use local LLMs.
- Do not start this gate from a Codex session whose runtime override disables or
  weakens agent sandbox settings, such as `--yolo`, `danger-full-access`, or an
  equivalent no-sandbox mode. Stop non-clean before `prepare` if the
  `sandbox_mode = "read-only"` TOML setting cannot be enforced for subagents.
- The driver and reviewer/verifier subagents must not run tests, linters,
  formatters, typecheck, tsc, or project-specific checks.
- Do not accept same-agent review, hooks-only success, or manual self-review as
  clean.
- Do not create per-file or per-packet reviewer fanout.
- Use exactly one broad reviewer call, then at most two verifier calls.
- After fixes, never start a new broad review. Use verifiers only.
- Stop immediately if any driver command prints a non-empty `stop_reason=`.

Hard caps:

- `broad_review_max=1`
- `verify_review_max=2`
- `max_total_reviewer_calls=3`
- `max_fix_rounds=2`
- `max_findings_per_round=20`

## Resolve driver

```bash
codex_dir="${CODEX_DIR:-$HOME/.codex}"
driver="$codex_dir/tools/post-work-review.sh"
if [ ! -x "$driver" ] && [ -n "${CODEX_HOME:-}" ]; then
  driver="$CODEX_HOME/tools/post-work-review.sh"
fi
if [ ! -x "$driver" ]; then
  echo "post-work-review driver not installed: $driver"
  echo "Run make install-integrations from the fanout repo, then retry."
  exit 1
fi
```

The driver writes review bookkeeping under the worktree git metadata directory.
If the sandbox blocks `.git/post-work-review` or `.git/post-work-review-passed`
writes, rerun only the blocked driver subcommand with a scoped escalation. Do
not escalate subagent review work and do not use escalation to call
`codex review`.

If the user request or fanout briefing includes `POST_WORK_REVIEW_BASE=<base>`,
pass that same environment variable explicitly to every driver command in this
procedure. Do not assume a shell-like `$post-work-review` prefix reached the
driver.

## Project validation before the gate

Before `prepare`, resolve the project's lint and test commands (AGENTS.md,
CLAUDE.md, Makefile) and run them yourself. Fix failures caused by the change
under review; report pre-existing or environment-caused failures (such as a
missing toolchain) in one line and continue instead of editing out-of-scope
code. When gating a committed branch (the tree was clean before validation),
commit the fixes this validation produces before running `prepare` — a tree
left dirty makes the driver narrow its scope to `uncommitted|HEAD` and `mark`
later rejects with `non_branch_review_scope`. When reviewing uncommitted work
(the tree was already dirty), leave the fixes uncommitted; the
`uncommitted|HEAD` bundle includes them together with the work under review.
Skip with a one-line note when the project has none. This validation runs
outside the isolated reviewer/verifier calls and never replaces them.

## Procedure

1. Prepare one broad review bundle:

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" prepare
   ```

   Omit `POST_WORK_REVIEW_BASE=...` only when no base override was requested.

   Read only the key=value output. Use `review_bundle=` as the sole broad
   review input. Do not split by file.

2. Spawn exactly one fresh isolated `post-work-reviewer` subagent. Give it only
   `review_bundle=` and require JSON only. Store the exact JSON in a temporary
   file outside the repository, then record it:

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" record broad <review-json-file>
   ```

   If `record` rejects the result, stop. Do not repair reviewer JSON yourself.
   The JSON must report `reviewer_sandbox_mode: "read-only"`.

3. Summarize:

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" summarize
   ```

   If `stop_reason=` is non-empty, stop non-clean and do not mark.

4. If `clean=true`, run `mark` only when `marker_eligible=true`:

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" mark
   ```

   If branch scope returns `clean=true` but `marker_eligible=false` because
   the fix is still in a dirty working tree, do not mark the old gate. Commit
   the fix first, then restart this procedure from step 1 for the new committed
   review target. Treat that as a new gate for a new HEAD, not as another broad
   reviewer call inside the previous gate.

5. If findings remain and no stop reason is set, fix only actionable findings
   from the stored JSON/results and generated `findings.tsv`. Then prepare a
   verifier bundle. If you changed files, run the normal focused validation for
   those edits before invoking the verifier; that validation is outside the
   isolated reviewer/verifier calls and never replaces the verifier JSON.

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" prepare-verify
   ```

   Give `verify_bundle=` to one fresh isolated `post-work-verifier` subagent.
   It is not a broad reviewer. It may check only prior findings and obvious
   regressions introduced by the fix. It must not hunt for unrelated issues.

6. Record the verifier result:

   ```bash
   POST_WORK_REVIEW_BASE="<base>" bash "$driver" record verify <review-json-file>
   ```

   Then run `summarize` again. If still not clean and no stop reason is set,
   perform one more fix round and one more verifier call. Never exceed two
   verifier calls.

7. Stop non-clean if any cap is exhausted, `truncated=true`, a repeated finding
   fingerprint appears after a fix round, or the driver reports any
   `stop_reason=`.

## Finish report

Report:

- reviewed scope
- broad and verifier call counts
- actionable fixes made
- final `clean=`
- final `stop_reason=`
- whether `.git/post-work-review-passed` was written
