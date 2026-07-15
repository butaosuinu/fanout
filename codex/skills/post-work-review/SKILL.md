---
name: post-work-review
description: Run the bounded isolated reviewer gate on current Git work before a commit or PR. Use for final review, post-review, "review して仕上げて", "コミット前確認", "二重チェック", or explicit post-work-review requests.
---

# Post-work review

Run the installed driver and delegate review to fresh native custom subagents.
The main agent validates and fixes the work; it must not review its own code.

## Runtime contract

- Use only the native `spawn_agent` tool. Never substitute `codex review`,
  `codex exec`, a local LLM, a prompt-only role, or a renamed `task_name`.
- Before `prepare`, inspect the model-visible `spawn_agent` schema. A
  supported V1 call exposes `agent_type` and uses `fork_context: false`; a
  supported V2 call exposes `agent_type` and uses `fork_turns: "none"`.
  If the schema cannot select a custom `agent_type`, stop non-clean with
  `stop_reason=unsupported_native_custom_agent`.
- Select `agent_type: "post-work-reviewer"` for the one broad call and
  `agent_type: "post-work-verifier"` for verification calls. Pass the
  absolute bundle path as the sole task message.
- The installed custom agent definitions must set `sandbox_mode = "read-only"`
  and `approval_policy = "never"`. The child rollout metadata, not the result
  JSON, proves that these settings and the custom role were applied.
- If a workspace-write controller cannot enforce the child's configured
  read-only/never policy, use a fresh controller started with read-only sandbox
  and approval policy never for the native spawn step. The writable main agent
  keeps ownership of driver state and fixes. If that split is unavailable,
  stop before reserving a call with
  `stop_reason=unsupported_read_only_custom_agent`.
- Reviewer calls are read-only. They must not edit, request approval, escalate,
  or run tests, linters, formatters, typechecks, project checks, local LLMs, or
  `codex review`.
- Allow one broad reservation and at most two verifier reservations. A failed
  or unrecorded spawn still consumes its reservation. Never start a second
  broad review.

## Resolve the driver

```bash
codex_dir="${CODEX_DIR:-$HOME/.codex}"
driver="$codex_dir/tools/post-work-review.sh"
if [ ! -x "$driver" ] && [ -n "${CODEX_HOME:-}" ]; then
  driver="$CODEX_HOME/tools/post-work-review.sh"
fi
if [ ! -x "$driver" ]; then
  echo "post-work-review driver not installed: $driver"
  echo "Run make install from fanout, then retry."
  exit 1
fi
```

Keep the matching `fanout` executable on `PATH`, or set `FANOUT_BIN`.
Driver state lives under the worktree Git metadata directory. Apply only a
scoped escalation if that location is not writable.

If the request or briefing supplies `POST_WORK_REVIEW_BASE=<base>`, pass it
to every driver command.

## Validate the candidate

Inspect `git status --short`, then choose one path:

- For a clean committed branch, run the repository's canonical aggregate
  validation command exactly once for the candidate HEAD. Do not also run its
  component targets. Record `validated_head="$(git rev-parse HEAD)"` only
  after it passes.
- For a dirty uncommitted review, run focused checks only. This scope can be
  reviewed but cannot receive the exact-HEAD marker.

If validation changes a committed candidate, commit it and restart before
`prepare`. After broad findings have been recorded, keep the driver state and
use the verifier loop below.

## Run one native call

The writable main agent runs the driver commands. The controller UUID supplied
to `reserve-call` must be the canonical `CODEX_THREAD_ID` of the controller
that will invoke `spawn_agent`.

1. Run `bash "$driver" reserve-call <broad|verify> <controller-uuid>`.
2. Use the reported role and absolute bundle path in exactly one native call:

   - V1 broad:
     `{"agent_type":"post-work-reviewer","message":"<bundle>","fork_context":false}`
   - V2 broad:
     `{"agent_type":"post-work-reviewer","task_name":"post_work_reviewer","message":"<bundle>","fork_turns":"none"}`
   - Replace the role with `post-work-verifier` and the task name with
     `post_work_verifier` for a verifier.

3. Wait for completion and obtain the canonical child UUID returned by the
   native tool. Do not copy or repair the child's JSON.
4. Run
   `bash "$driver" record-session <broad|verify> <child-uuid>`.

`record-session` reads the child rollout, extracts
`task_complete.last_agent_message`, and rejects the result unless the rollout
has a fresh child UUID, the reserved parent, the expected role, read-only
sandbox, approval policy never, and the exact bundle task. It also requires the
result's `reviewer_session_id` to equal the child UUID. Stop non-clean on any
rejection.

## Run the gate

1. Run `bash "$driver" prepare`, then run one broad native call as above.
2. Run `bash "$driver" summarize`. Stop on any non-empty `stop_reason=`.
   If `clean=true`, continue to the marker checks.
3. If findings remain, fix only the recorded findings. Run focused checks while
   editing. For branch scope, commit the fixes, run the canonical validation
   command exactly once on that new HEAD, and update `validated_head` only
   after it passes.
4. Run `bash "$driver" prepare-verify`, then one fresh verifier native call.
   Summarize again. If findings remain without a stop reason, repeat the fix,
   validation, `prepare-verify`, and fresh verifier sequence once more.
5. Stop without marking on a pending call, invalid rollout or result,
   exhausted budget, `truncated=true`, a repeated finding fingerprint, or any
   non-empty stop reason.
6. Run `bash "$driver" mark` only when `clean=true`,
   `marker_eligible=true`, the worktree is clean, and current HEAD equals the
   last canonically validated HEAD. Never commit after marking.

## Report

Report the validation command and result, reviewed scope, broad/verifier
reservation counts, child UUIDs and verified metadata, fixes made, final
`clean=`, final `stop_reason=`, and whether
`.git/post-work-review-passed` was written.
