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
- Do not run tests, linters, formatters, typecheck, tsc, or project-specific
  checks from this skill.
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
codex_dir="${CODEX_DIR:-${CODEX_HOME:-$HOME/.codex}}"
driver="$codex_dir/tools/post-work-review.sh"
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

## Procedure

1. Prepare one broad review bundle:

   ```bash
   bash "$driver" prepare
   ```

   Read only the key=value output. Use `review_bundle=` as the sole broad
   review input. Do not split by file.

2. Spawn exactly one fresh isolated `post-work-reviewer` subagent. Give it only
   `review_bundle=` and require JSON only. Store the exact JSON in a temporary
   file outside the repository, then record it:

   ```bash
   bash "$driver" record broad <review-json-file>
   ```

   If `record` rejects the result, stop. Do not repair reviewer JSON yourself.

3. Summarize:

   ```bash
   bash "$driver" summarize
   ```

   If `stop_reason=` is non-empty, stop non-clean and do not mark.

4. If `clean=true`, run `mark` only when `marker_eligible=true`:

   ```bash
   bash "$driver" mark
   ```

5. If findings remain and no stop reason is set, fix only actionable findings
   from the stored JSON/results and generated `findings.tsv`. Then prepare a
   verifier bundle:

   ```bash
   bash "$driver" prepare-verify
   ```

   Give `verify_bundle=` to one fresh isolated `post-work-verifier` subagent.
   It is not a broad reviewer. It may check only prior findings and obvious
   regressions introduced by the fix. It must not hunt for unrelated issues.

6. Record the verifier result:

   ```bash
   bash "$driver" record verify <review-json-file>
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
