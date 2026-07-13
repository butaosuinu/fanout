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
- Select each role with the native subagent tool's `agent_type` field and set
  `fork_turns: "none"`. A `task_name`, prompt text, or installed agent file is
  not proof that the requested custom agent ran.
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
  echo "Reinstall this skill and its companion fanout executable, then retry."
  exit 1
fi
```

The driver uses the `fanout` executable distributed with this skill to parse
reviewer results. Keep `fanout` on `PATH`, or set `FANOUT_BIN` to that
executable before running the driver. A source-only integration install must
provide the executable separately.

The driver stores state under the worktree git metadata directory. If the
sandbox blocks `.git/post-work-review` or `.git/post-work-review-passed`, rerun
only that driver subcommand with scoped escalation.

If the request or fanout briefing supplies `POST_WORK_REVIEW_BASE=<base>`, pass
that environment variable explicitly to every driver command below. Do not
assume a shell-like `$post-work-review` prefix reached the driver.

## Validate before the gate

Inspect `git status --short` before `prepare`, then choose one validation path:

- **Clean committed branch (final gate):** resolve the project's one canonical
  full validation command from its contributor instructions or build
  configuration. If the project declares an aggregate command, run it exactly
  once for the candidate HEAD; do not also run its component targets. If
  validation fails, stop before `prepare`. Fix only failures caused by the
  branch, run focused checks while
  editing, commit the fixes, then restart the final gate on the new HEAD.
  After the canonical command passes, record that exact commit as
  `validated_head="$(git rev-parse HEAD)"`.
  Environment or pre-existing failures are non-clean gate results, not reasons
  to mark an unvalidated commit.
- **Dirty uncommitted review:** run only the focused validation needed for the
  changed area. Do not spend a branch-wide full-validation pass on a target
  that cannot receive the exact-HEAD marker. The `uncommitted|HEAD` bundle may
  still be reviewed, but the caller must commit the candidate and restart this
  skill in branch scope before pushing.

If the project documents no validation command, report that in one line. Main
agent validation runs outside the isolated reviewer/verifier calls and never
replaces them.

If validation changes a committed candidate, commit the fix and restart the
final gate on the new HEAD. If the work was already uncommitted, leave focused
validation fixes uncommitted so the same bundle includes all reviewed work.
These restart rules apply before the initial `prepare`; after a broad result
has recorded findings, keep its driver state and follow step 3 instead.

## Validate custom-agent selection

After validation succeeds, but before running `prepare`, inspect the visible
input schema for the native `spawn_agent` tool. Require an explicit
`agent_type` field and a `fork_turns` field that accepts `"none"`. Do not infer
custom-agent support from `task_name`, the message or prompt, an agent config
file, or a returned task label.

If the required selector is absent or unusable, do not spawn a reviewer and do
not run `prepare`. Stop non-clean and report the full visible `spawn_agent`
input schema, including field names and types, with these exact values:

```text
clean=false
stop_reason=custom_agent_selection_unavailable
custom_role_selector=false
marker_written=false
```

Do not fall back to a generic subagent, another role, prompt-based role
impersonation, self-review, a local LLM, or `codex review`.

When the schema supports the contract, create every child with
`fork_turns: "none"`. Use these exact selectors:

```text
agent_type: "post-work-reviewer"
agent_type: "post-work-verifier"
```

The broad call receives only the exact `review_bundle`; a verifier call
receives only the exact `verify_bundle`. `task_name` is optional display
metadata and never role evidence. The driver must attest the child's actual
session metadata before accepting the result.

## Run the gate

1. Run `bash "$driver" prepare`. Read only its key/value output and pass the
   reported `review_bundle=` as the sole input to exactly one fresh
   `post-work-reviewer` using `agent_type: "post-work-reviewer"` and
   `fork_turns: "none"`. Require JSON only and save the exact bytes returned by
   the child outside the repository without extracting, repairing, or
   reformatting them. Run
   `bash "$driver" record broad <review-json-file>`. Stop if `record` rejects
   the result or its session attestation. The result's `reviewer_session_id`
   must be the child's actual canonical UUID from `CODEX_THREAD_ID`; a
   `task_name`, role label, prompt, or arbitrary string is not a session ID.
2. Run `bash "$driver" summarize`. Stop non-clean on `stop_reason=`. If
   `clean=true`, run `bash "$driver" mark` only when
   `marker_eligible=true` and every stored result has passed the driver's
   session attestation. Never use self-reported role, model, sandbox, or
   isolation fields as attestation. Before `mark`, require a clean worktree
   and confirm that the current HEAD equals the last exact HEAD that passed
   canonical full validation. If no actionable-finding verifier path applies
   and either condition fails, stop without marking.
3. If actionable findings remain, fix only those findings from the stored
   results and `findings.tsv`. Run focused validation while editing. For branch
   scope, commit the fixes, run the canonical full validation command exactly
   once on that new exact HEAD, and replace `validated_head` only after it
   passes. Require a clean worktree and the same current HEAD before continuing.
   Do not run `prepare` again or start another broad review; continue the
   existing driver state with `bash "$driver" prepare-verify`. For dirty
   uncommitted scope, run focused validation only because that scope cannot
   receive a marker, then continue the same driver state with `prepare-verify`.
   Pass `verify_bundle=` as the sole input to one fresh verifier using
   `agent_type: "post-work-verifier"` and `fork_turns: "none"`; it may check
   only prior findings and obvious fix-introduced regressions.
4. Save the verifier's exact JSON outside the repository, run
   `bash "$driver" record verify <review-json-file>`, then `summarize`. Do not
   extract, repair, or reformat the child output. If it is clean, mark only
   after driver attestation and under the exact-HEAD condition in step 2. If
   it remains non-clean without a stop reason, allow one final
   fix/validation/verifier round using the same sequence in step 3 and the
   existing driver state. Never exceed two verifier calls.
5. Stop non-clean without marking when a cap is exhausted, `truncated=true`, a
   finding fingerprint repeats after a fix round, or any `stop_reason=` is
   reported.

## Report

Report the canonical or focused validation command and result, reviewed scope,
broad/verifier call counts, fixes made, final `clean=`, final `stop_reason=`,
actual attested child UUID/role/model/sandbox metadata when available, and
whether `.git/post-work-review-passed` was written. If custom-agent selection
is unavailable, include the visible `spawn_agent` input schema in the report.
