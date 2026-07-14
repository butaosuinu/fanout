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
- At every reviewer or verifier spawn, require the review controller's current
  permission profile to be enforceably `read-only`. Codex reapplies that live
  profile after it loads a custom agent, so the agent file's
  `sandbox_mode = "read-only"` is not sufficient by itself. Stop non-clean
  before `prepare` if `--yolo`, `danger-full-access`, or an equivalent override
  weakens sandboxing. Only driver bookkeeping and exact reviewer-result capture
  may use scoped escalation while the controller is read-only; never escalate
  subagent review work.
- Treat the controller rollout as the authority for that permission profile.
  `prepare` must attest its current `turn_context` before writing a bundle.
  Immediately before every native spawn, run the driver's `authorize-spawn`
  command and require `review_controller_sandbox_mode=read-only`. Spawn in the
  same controller turn without another command in between. The driver binds
  the authorized turn ID, context SHA-256, and authorization timestamp to the
  actual spawn record and final marker; a manual permission-profile claim is
  not evidence.
- Require each installed custom agent to set top-level
  `approval_policy = "never"`. The driver attests that policy from every child
  turn context and binds the attested approval policy into each call receipt
  and final marker. Stop non-clean if the policy is missing or differs; never
  ask a reviewer or verifier to approve an escalation.
- `approval_policy = "never"` does not replace rollout inspection. The attestor
  must still reject every sandbox permission override request and every
  noncanonical code-mode exec. Treat either as a terminal attestation failure.
- Use exactly one fresh `post-work-reviewer` call for the complete bundle, then
  at most two fresh `post-work-verifier` calls after fixes. Never split by
  file, start another broad review after fixes, or accept same-agent,
  hooks-only, or manual self-review as clean.
- Use those configured roles and models as installed. If either is unavailable
  or fails to start, stop non-clean; never substitute another role or model.
- Select each role with the native subagent tool's `agent_type` field. With
  MultiAgentV2, set `fork_turns: "none"`; with MultiAgentV1, set
  `fork_context: false`. A `task_name`, prompt text, or installed agent file is
  not proof that the requested custom agent ran.
- Pass only the absolute bundle path printed by the driver as the child
  `message`. Never inline bundle contents into the native tool call; large
  diffs may be truncated or replaced by a placeholder before the child starts.
  The installed custom agent validates the path against the worktree's absolute
  Git directory and reads the complete bundle directly.
- Preserve the bundle SHA-256 printed by `prepare` or `prepare-verify`. Before
  spawning, use the companion `fanout __post-work-review-json digest` command
  to require that the current exact bundle bytes still match it. The child
  repeats that digest before and after its complete read and returns it as the
  required `bundle_sha256` result field; the driver binds both the current file
  and the attested child result to the prepared digest at `record` time.
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

After validation, keep `validated_head` and the validation command/result as
handoff data. If the current thread already has a compatible custom-agent
schema, switch its permission profile to read-only, then recheck that the tree
is clean and HEAD still equals `validated_head`; do not rerun the canonical
command. If a fresh thread is required to load installed agents or obtain a
compatible schema, start a fresh **interactive** read-only Codex controller and
pass it the same handoff data. The fresh controller must perform the same
tree/HEAD checks before `prepare` and must not rerun canonical validation.
Never use `codex exec`, including as the controller, anywhere in the review
execution path.

## Validate custom-agent selection

After validation succeeds, but before running `prepare`, inspect the visible
input schema for the native `spawn_agent` tool. Require an explicit
`agent_type` field and one of these native no-history controls:

- MultiAgentV2: a `fork_turns` field that accepts `"none"`.
- MultiAgentV1: a boolean `fork_context` field that accepts `false`.

Do not infer custom-agent support from `task_name`, the message or prompt, an
agent config file, or a returned task label. Installing an agent definition
does not refresh an already-running thread's tool schema. If needed, start a
fresh interactive Codex controller after installation and inspect that new
thread. Do not use `codex review` as the reviewer.

If the required selector is absent or unusable, do not spawn a reviewer and do
not run `prepare`. Stop non-clean and report the full visible `spawn_agent`
input schema, including field names and types, with these exact values:

```text
clean=false
stop_reason=custom_agent_selection_unavailable
custom_role_selector=false
marker_written=false
```

After the schema passes, inspect the controller's current permission profile.
If it is not enforceably read-only, do not spawn a reviewer and do not run
`prepare`. Stop non-clean and report:

```text
clean=false
stop_reason=review_controller_not_read_only
custom_role_selector=true
marker_written=false
```

Do not fall back to a generic subagent, another role, prompt-based role
impersonation, self-review, a local LLM, or `codex review`.

When the schema supports the contract, use the matching call shape for every
child. Do not pass `model` or reasoning overrides; the installed custom agent
definition owns them.

MultiAgentV2 broad call:

```text
agent_type: "post-work-reviewer"
task_name: "post_work_review_broad"
message: "<absolute review_bundle path>"
fork_turns: "none"
```

MultiAgentV2 first verifier call:

```text
agent_type: "post-work-verifier"
task_name: "post_work_review_verify_1"
message: "<absolute verify_bundle path>"
fork_turns: "none"
```

If the final verifier call is needed, use the same shape with the second call's
unique display name:

```text
agent_type: "post-work-verifier"
task_name: "post_work_review_verify_2"
message: "<absolute verify_bundle path>"
fork_turns: "none"
```

Never reuse a MultiAgentV2 verifier `task_name`; the unique call index binds
each child `agent_path` to one parent spawn output during re-attestation.

MultiAgentV1 broad call:

```text
agent_type: "post-work-reviewer"
message: "<absolute review_bundle path>"
fork_context: false
```

MultiAgentV1 verifier call:

```text
agent_type: "post-work-verifier"
message: "<absolute verify_bundle path>"
fork_context: false
```

The broad call's `message` is only the exact absolute path value printed after
`review_bundle=`; a verifier call's `message` is only the value printed after
`verify_bundle=`. Do not pass the file contents, the key name, or a wrapper
prompt. MultiAgentV2 requires `task_name` as display metadata. MultiAgentV1
does not accept `task_name`. In neither version is it role evidence. The driver
must attest the child's actual session metadata before accepting the result.

## Run the gate

1. Run `bash "$driver" prepare`. Read only its key/value output and pass the
   absolute path value reported after `review_bundle=` as the entire `message`
   to exactly one fresh
   `post-work-reviewer` using `agent_type: "post-work-reviewer"` and the
   preflighted no-history control (`fork_turns: "none"` or
   `fork_context: false`). Before spawning, require that the value is an
   absolute path equal to
   `$(git rev-parse --absolute-git-dir)/post-work-review/review-bundle.md`, and
   that it is a readable, non-empty regular file, not a symbolic link. Require
   its driver header, backend, review type, and required sections to be present.
   Require `review_bundle_sha256=` to be one lowercase 64-character SHA-256
   digest, and require the companion digest command for the bundle path to
   return the same value. After all bundle checks, run
   `bash "$driver" authorize-spawn broad`; require `spawn_authorized=true`,
   the expected kind and call index, a canonical
   `review_controller_turn_id`, a lowercase 64-character
   `review_controller_context_sha256`, and
   `review_controller_sandbox_mode=read-only`. Make the native spawn the next
   controller command in the same turn. Do not reuse or overwrite an
   authorization.
   On failure, do not spawn and report `clean=false`,
   `stop_reason=review_bundle_invalid`, and `marker_written=false`. Do not read
   and inline the bundle in the controller; the custom reviewer repeats these
   checks and reads the file directly. If the child returns
   `REVIEW_BUNDLE_INVALID`, stop with the same non-clean reason; do not call
   `record`, retry the broad review, or fall back to inline content. Otherwise,
   require JSON only. Do not transcribe the result, ask the model to encode it,
   or construct base64 from the displayed text. Run
   `bash "$driver" record-session broad <child-session-uuid>` so the companion
   helper extracts `task_complete.last_agent_message` directly from the unique
   child rollout and feeds those UTF-8 bytes to the existing record path. If
   read-only sandboxing blocks this driver bookkeeping, use scoped escalation
   only for that exact driver command. Stop if `record-session` rejects the
   result or its session attestation. The result's `reviewer_session_id`
   must be the child's actual canonical UUID from `CODEX_THREAD_ID`; a
   `task_name`, role label, prompt, or arbitrary string is not a session ID.
   The result's `bundle_sha256` must equal the digest printed by `prepare`; a
   missing, malformed, or different digest is a terminal invalid result.
   The child's attested approval policy must be `never`; self-reported
   read-only fields do not satisfy this requirement.
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
   Return the review controller to an enforceably read-only permission profile
   and recheck it before spawning the verifier.
   Pass only the absolute path value after `verify_bundle=` as the verifier's
   entire `message`, using `agent_type: "post-work-verifier"` and the same
   preflighted no-history control. Apply the same path, regular-file, symlink,
   and driver-content checks for `verify-bundle.md`. On failure, or if the child
   returns `VERIFY_BUNDLE_INVALID`, stop with `clean=false`,
   `stop_reason=verify_bundle_invalid`, and `marker_written=false`; do not call
   `record`, retry, or inline the file. The verifier may check only prior
   findings and obvious fix-introduced regressions.
   Also require the current round's `verify_bundle_sha256_<N>=` value to be a
   lowercase 64-character SHA-256 digest, and require the companion digest
   command to return the same value. After all verifier-bundle checks, run
   `bash "$driver" authorize-spawn verify` and apply the same exact-output,
   same-turn, next-command requirements before spawning. The verifier result
   must return that exact value as `bundle_sha256`.
4. Run `bash "$driver" record-session verify <child-session-uuid>`, then
   `summarize`. Do not transcribe, encode, repair, or reformat the child output;
   the driver extracts the exact final message from the child rollout. If it is
   clean, mark only
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
