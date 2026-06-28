---
name: post-work-review
description: "Use from Codex CLI to run a token-efficient finish-review loop on current git work before commit or PR. Use when the user says review して仕上げて, post-review, finalize, コミット前確認, 二重チェック, codex review もかけて."
metadata:
  short-description: Token-efficient Codex review wrapper
---

# post-work-review

Use the installed bash driver. Do not run tsc, linters, formatters, or tests here; project hooks handle them.

## Execution permissions

The driver invokes the local `codex review ...` CLI with an isolated temporary `CODEX_HOME`, so local state DBs, logs, and locks are created under the writable temp area. Run `bash "$driver" run` without escalation. Do not escalate the `run` command.

This skill passes the current review target to Codex review. Use it only on explicit invocation; `agents/openai.yaml` disables implicit invocation for this reason.

The marker step writes `post-work-review-passed` under the git metadata directory. If that write is outside the workspace sandbox, run only `bash "$driver" mark` with escalation and use this justification:

```text
post-work-review mark writes the local reviewed-HEAD marker under this worktree's git metadata directory
```

## Procedure

1. Resolve the driver:

   ```bash
   codex_dir="${CODEX_DIR:-${CODEX_HOME:-$HOME/.codex}}"
   driver="$codex_dir/tools/post-work-review.sh"
   if [ ! -x "$driver" ]; then
     echo "post-work-review driver not installed: $driver"
     echo "Run make install-integrations from the fanout repo, then retry."
     exit 1
   fi
   ```

2. Run:

   ```bash
   bash "$driver" run
   ```

   Run without escalation.

3. Read only the printed key=value summary.

4. If `clean=true` and `marker_eligible=true`, run the marker command before finishing:

   ```bash
   bash "$driver" mark
   ```

   Use escalation for `mark` only when the git metadata directory is outside the writable sandbox.

   If `clean=true` and `marker_eligible=false`, finish without a marker and report `marker_reason=`.

5. If `clean=false`, read only the file shown by `digest=` and fix actionable findings.

6. If `clean=unknown`, read `digest=` first. Read `raw_output=` only when the digest is insufficient.

7. After fixing actionable findings, run the same command again:

   ```bash
   bash "$driver" run
   ```

8. Stop if `stop_reason=` is set.

9. If `review_blocked_reason=` is set, report it and stop. Do not rerun `bash "$driver" run` with escalation.

## Rules

- Do not call bare `codex review`.
- Do not run tests, linters, formatters, typecheck, or project-specific checks from this skill.
- Do not use a local LLM.
- Describe `codex review` as the local Codex CLI review command in prompts, summaries, and escalation justifications.
- Do not fall back to a manual review when Codex review is blocked.
- Do not read full review output unless `clean=unknown` and `digest=` is insufficient.
- Do not write marker manually. Always use the script.
- Do not rerun `bash "$driver" run` with escalation.
- Use escalation only for `bash "$driver" mark` when the git metadata marker write is outside the writable sandbox.
- If `bash "$driver" run` is blocked, run `bash "$driver" handoff`, report the handoff commands, and stop. Do not treat that as clean.

## Finish report

Report:

- reviewed scope
- number of review runs
- actionable fixes made
- whether clean was reached
- whether marker was written
