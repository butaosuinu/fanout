---
name: post-work-review
description: Run the bounded isolated reviewer gate on current Git work before a commit or PR. Use for final review, post-review, "review して仕上げて", "コミット前確認", "二重チェック", or explicit post-work-review requests.
---

# Post-work review

Run the installed driver and delegate review to fresh native subagents. The
main agent validates and fixes the work; it must not review its own code.

## Invariants

- Use the `bounded-isolated-reviewer` backend. Never use local LLMs or
  `codex review`.
- Require enforceable `read-only` sandboxing for reviewer/verifier subagents.
  Stop non-clean before `prepare` if the current Codex runtime weakens or
  disables agent sandbox settings, including `--yolo`, `danger-full-access`,
  or an equivalent override. Never escalate subagent review work.
- Use exactly one fresh `post-work-reviewer` call for the complete bundle, then
  at most two fresh `post-work-verifier` calls after fixes. Never split by
  file, start another broad review after fixes, or accept same-agent,
  hooks-only, or manual self-review as clean.
- Use those configured roles and models as installed. If either is unavailable
  or fails to start, stop non-clean; never substitute another role or model.
- Keep reviewer calls read-only: they must not run tests, linters, formatters,
  typechecks, project checks, local LLMs, or `codex review`.
- Enforce `broad_review_max=1`, `verify_review_max=2`,
  `max_total_reviewer_calls=3`, `max_fix_rounds=2`, and
  `max_findings_per_round=20`.
- Stop non-clean immediately when any driver command emits a non-empty
  `stop_reason=`.

## Resolve the driver

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

The driver stores state under the worktree git metadata directory. If the
sandbox blocks `.git/post-work-review` or `.git/post-work-review-passed`, rerun
only that driver subcommand with scoped escalation.

If the request or fanout briefing supplies `POST_WORK_REVIEW_BASE=<base>`, pass
that environment variable explicitly to every driver command below. Do not
assume a shell-like `$post-work-review` prefix reached the driver.

## Validate before the gate

Resolve the project lint and test commands from `AGENTS.md`, `CLAUDE.md`, and
the build files. Run them as the main agent before `prepare`; reviewer agents
must not run them. Fix failures caused by the reviewed change. Report
pre-existing or environment failures in one line and do not expand scope.

If a committed branch was clean before validation, commit validation fixes
before `prepare`; otherwise the driver selects `uncommitted|HEAD` and `mark`
rejects `non_branch_review_scope`. If the work was already uncommitted, leave
the fixes uncommitted so the same bundle includes all reviewed work. Note and
skip projects with no validation commands.

## Run the gate

1. Run `bash "$driver" prepare`. Read only its key/value output and pass the
   reported `review_bundle=` as the sole input to exactly one fresh
   `post-work-reviewer`. Require JSON only, save its exact output outside the
   repository, and run `bash "$driver" record broad <review-json-file>`. Stop
   if `record` rejects it; never repair reviewer JSON. The result must report
   `reviewer_sandbox_mode: "read-only"`.
2. Run `bash "$driver" summarize`. Stop non-clean on `stop_reason=`. If
   `clean=true`, run `bash "$driver" mark` only when
   `marker_eligible=true`. If branch scope is clean but not marker-eligible
   because fixes remain dirty, commit them and restart at `prepare` for the new
   HEAD; this is a new gate, not a second broad call in the prior gate.
3. If actionable findings remain, fix only those findings from the stored
   results and `findings.tsv`. Run focused validation for changed files, then
   run `bash "$driver" prepare-verify`. Pass `verify_bundle=` to one fresh
   `post-work-verifier`; it may check only prior findings and obvious
   fix-introduced regressions.
4. Save the verifier's exact JSON outside the repository, run
   `bash "$driver" record verify <review-json-file>`, then `summarize`. If it
   remains non-clean without a stop reason, allow one final fix/validation/
   verifier round. Never exceed two verifier calls.
5. Stop non-clean without marking when a cap is exhausted, `truncated=true`, a
   finding fingerprint repeats after a fix round, or any `stop_reason=` is
   reported.

## Report

Report the reviewed scope, broad/verifier call counts, fixes made, final
`clean=`, final `stop_reason=`, and whether
`.git/post-work-review-passed` was written.
