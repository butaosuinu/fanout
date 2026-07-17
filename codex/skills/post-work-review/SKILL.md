---
name: post-work-review
description: Run a fresh native subagent review on current Git work before a commit or PR. Use for final review, post-review, "review して仕上げて", "コミット前確認", "二重チェック", or explicit post-work-review requests.
---

# Post-work review

Use Codex's native generic subagents for one broad review and, after fixes, a
fresh verification review. The parent agent interprets their natural-language
findings. Do not parse reviewer output or require a result schema.

## Boundary

- Tell the user that the reviewed repository content is sent to the Codex
  model before spawning a reviewer.
- Use `spawn_agent` and `wait_agent` directly. Set `fork_turns: "none"` so each
  reviewer starts without the parent's conversation history.
- `task_name` labels the task; it does not select a custom role. Do not use
  custom agents, `agent_type`, `codex exec`, `codex review`, app-server, or an
  external controller.
- A native subagent inherits the parent session's sandbox, approval policy, and
  network restrictions. Reviewer instructions below prohibit edits, approval
  requests, and network use, but this is not a stronger child-only sandbox. If
  enforced read-only access is required, start the parent session read-only
  before invoking this skill.
- Reviewer messages must allow only read-only local inspection such as
  `git diff`, `rg`, and `sed`. Explicitly prohibit builds, typechecks,
  generators, package managers, tests, formatters, file writes, web/browser
  tools, MCP/connectors, external services, nested agents, agent messaging,
  approval requests, and escalation.
- If native subagent spawning or waiting is unavailable, no concurrency slot is
  available, or the reviewer fails to complete, stop with a clear error. Do not
  use a fallback reviewer and do not write the review marker.

## Prepare the target

1. Finish other writer agents before starting the gate.
2. Require a Git worktree, a committed candidate, and a clean working tree as
   reported by `git status --porcelain -uall --ignore-submodules=none`.
3. Resolve and record:
   - repository root;
   - exact `HEAD` commit;
   - target base branch and the exact commit resolved from
     `refs/remotes/origin/<base>`;
   - the branch review bundle, defined as that recorded base commit through the
     recorded `HEAD`, including submodule changes.
4. Run `scripts/mark-reviewed-head.sh clear` from this skill directory before
   the first spawn. This removes any stale success marker.
5. Resolve the project's canonical full validation command from repository
   instructions, but do not run it yet.

Stop if the base cannot be resolved, the tree is dirty, or the target changes
during preparation.

## Broad review

Spawn exactly one generic subagent with a payload shaped like this:

```json
{
  "task_name": "post_work_review_<head-prefix>_<unique>",
  "fork_turns": "none",
  "message": "Review the recorded <base-commit>...<head-commit> bundle at the recorded repository. Read repository instructions first. Use only read-only local inspection commands. Do not edit files or run tests, builds, typechecks, linters, formatters, generators, or package managers. Do not use web, browser, MCP/connectors, external-service, or network tools. Do not spawn or message agents, request approval, or escalate. Inspect the diff and relevant surrounding code. Report only blocker or major correctness, security, data-loss, or contract findings. For each finding include severity, file:line, reason, and a concrete recommendation. If none exist, explicitly say no blocker or major findings."
}
```

Replace the recorded placeholders in the message with the absolute repository
root, base branch, full base commit, and full HEAD SHA. Replace every task-name
placeholder: use lowercase hexadecimal characters for `<head-prefix>`, a
decimal verifier round for `<round>`, and an unused lowercase alphanumeric
suffix for `<unique>`. The final name must match `[a-z0-9_]+`; never reuse a
name from a completed agent in the same parent session. Wait until the
subagent finishes; repeated waits are allowed. Do not accept an interrupted,
failed, ambiguous, or missing completion as clean.

The parent reads the response as ordinary review feedback. It may reject a
finding only with concrete evidence from the target diff or repository. Do not
turn wording variations into a machine validation problem.

## Fix and verify

If the broad review has actionable findings:

1. Apply the fixes in the parent session and run focused checks for edited
   files.
2. Commit the fixes and record the new clean `HEAD`.
3. Spawn a new generic subagent with an unused task name shaped as
   `post_work_verify_<head-prefix>_<round>_<unique>` and
   `fork_turns: "none"`. Include the prior findings, the exact recorded base
   commit, the new exact HEAD, and the new bundle in its message.
4. Give the verifier every read-only restriction from the broad reviewer,
   including no external tools or nested agents. Tell it to check only whether
   the prior findings are resolved plus obvious regressions caused by those
   fixes.

Use at most two verifier rounds. Never rerun the broad review in the same gate.
If findings remain after the second verifier, stop and report them without a
marker.

## Validate and mark

After the broad reviewer, or the latest verifier, is clean:

1. Confirm the worktree is still clean and `HEAD` still equals the reviewed
   target.
2. Run the single canonical full validation command once. Do not duplicate it
   with separate full lint and test commands.
3. Confirm the tree and `HEAD` again.
4. Run:

   ```sh
   scripts/mark-reviewed-head.sh mark <reviewed-head> <base-branch> <reviewed-base-head>
   ```

The helper validates only Git facts: clean exact HEAD, remote base, and bundle
hash. The marker means the parent accepted the native subagent's result for
that target; it is not proof of a custom role, model, or child-only sandbox.
Any later commit, base movement, diff change, validation failure, reviewer
failure, or unclear result leaves the gate incomplete and requires a fresh run.
