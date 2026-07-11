#!/usr/bin/env bats
#
# Tier 1 — repo-local agent hook scripts (scripts/agent-*.sh) contract.
#
# These tests drive the Claude Code / Codex hooks directly over their
# stdin-JSON + exit-code contract: the push gate (PreToolUse), the Codex stop
# gate backstop (Stop), the format-on-edit hook (PostToolUse), and the
# Makefile check-marker writer the gates depend on. No agent runs here; heavy
# `make check` runs are replaced by stub Makefiles inside sandbox repos.

load helpers

PUSH_GATE="$REPO_ROOT/scripts/agent-push-gate.sh"
STOP_GATE="$REPO_ROOT/scripts/agent-stop-gate.sh"
FORMAT_HOOK="$REPO_ROOT/scripts/agent-format-on-edit.sh"
HOOKS_LIB="$REPO_ROOT/scripts/agent-hooks-lib.sh"

setup_hook_repo() {
  local repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.email "fanout-test@example.com"
  git -C "$repo" config user.name "fanout test"
  printf 'base\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "initial"
  git -C "$repo" branch -M main
}

write_marker() {
  git -C "$1" rev-parse HEAD >"$1/.git/fanout-check-passed"
}

# Feed a payload to a hook script exactly like the agents do: JSON on stdin,
# combined output captured, ambient hook env scrubbed so a developer's own
# Claude session (CLAUDE_PROJECT_DIR) or exported bypasses cannot leak in.
run_hook() {
  local script="$1" payload="$2"
  run bash -c 'printf "%s" "$1" | env -u CLAUDE_PROJECT_DIR -u FANOUT_SKIP_PUSH_CHECK -u FANOUT_SKIP_STOP_GATE bash "$0" 2>&1' "$script" "$payload"
}

# run_push_gate CMD CWD — CMD must not itself contain JSON-special characters;
# tests that need embedded quotes build their payload literally instead.
run_push_gate() {
  run_hook "$PUSH_GATE" "{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"$1\"},\"cwd\":\"$2\"}"
}

# --- push gate ---------------------------------------------------------------

@test "push gate: denies git push without a marker" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  run_push_gate 'git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  [[ "$output" == *"fanout push gate"* ]]
  [[ "$output" == *"make check"* ]]

  # Composite commands are still scanned segment by segment.
  run_push_gate 'echo done && git push origin main' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: allows git push when marker matches the pushed tip" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" branch feature-branch
  write_marker "$repo"

  run_push_gate 'git push origin HEAD' "$repo"
  [ "$status" -eq 0 ]
  [ -z "$output" ]

  # A named refspec is validated against its own tip, which here equals HEAD.
  run_push_gate 'git push -u origin feature-branch' "$repo"
  [ "$status" -eq 0 ]
}

@test "push gate: validates the refspec tip, not the checked-out HEAD" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" branch stale-branch
  printf 'more\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm "second"
  write_marker "$repo" # marker == new HEAD, not stale-branch

  run_push_gate 'git push origin HEAD' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push origin stale-branch' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: denies a stale marker after a new commit" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"
  printf 'more\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm "second"

  run_push_gate 'git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  [[ "$output" == *"marker"* ]]
}

@test "push gate: allows branch deletion and tag pushes without a marker" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" tag v1.0.0

  run_push_gate 'git push --delete origin old-branch' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push origin :old-branch' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push --tags origin' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push origin v1.0.0' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push origin refs/tags/v1.0.0' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push origin tag v1.0.0' "$repo"
  [ "$status" -eq 0 ]
  # An unqualified dst can expand to an existing remote branch, so it is gated.
  run_push_gate 'git push origin v1.0.0:release-tag' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'git push -o ci.skip origin v1.0.0' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'git push --dry-run origin HEAD' "$repo"
  [ "$status" -eq 0 ]

  # A branch smuggled next to a deletion still needs the marker.
  run_push_gate 'git push origin main :old-branch' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: tag exemptions do not cover branch updates" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" tag tmp

  # --tags / --follow-tags alongside a branch refspec still gate the branch.
  run_push_gate 'git push --tags origin main' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'git push --follow-tags origin main' "$repo"
  [ "$status" -eq 2 ]
  # A tag pushed to a branch destination is a branch update.
  run_push_gate 'git push origin tmp:refs/heads/feature' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: follows cd and git -C to the pushed repository" {
  local repo_a="$BATS_TEST_TMPDIR/repo-a"
  local repo_b="$BATS_TEST_TMPDIR/repo-b"
  setup_hook_repo "$repo_a"
  setup_hook_repo "$repo_b"
  write_marker "$repo_a" # only repo A is validated

  # Payload cwd is the validated repo A, but the push targets repo B.
  run_push_gate "cd $repo_b && git push origin HEAD" "$repo_a"
  [ "$status" -eq 2 ]
  run_push_gate "git -C $repo_b push origin HEAD" "$repo_a"
  [ "$status" -eq 2 ]

  write_marker "$repo_b"
  run_push_gate "cd $repo_b && git push origin HEAD" "$repo_a"
  [ "$status" -eq 0 ]
}

@test "push gate: payload cwd wins over CLAUDE_PROJECT_DIR" {
  local repo_a="$BATS_TEST_TMPDIR/repo-a"
  local repo_b="$BATS_TEST_TMPDIR/repo-b"
  setup_hook_repo "$repo_a"
  setup_hook_repo "$repo_b"
  write_marker "$repo_a" # session root is validated, the pushed repo is not

  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git push origin HEAD\"},\"cwd\":\"$repo_b\"}"
  run bash -c 'printf "%s" "$1" | env -u FANOUT_SKIP_PUSH_CHECK CLAUDE_PROJECT_DIR="$2" bash "$0" 2>&1' \
    "$PUSH_GATE" "$payload" "$repo_a"
  [ "$status" -eq 2 ]
}

@test "push gate: FANOUT_SKIP_PUSH_CHECK=1 bypasses the gate" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git push origin HEAD\"},\"cwd\":\"$repo\"}"
  run bash -c 'printf "%s" "$1" | env -u CLAUDE_PROJECT_DIR FANOUT_SKIP_PUSH_CHECK=1 bash "$0" 2>&1' "$PUSH_GATE" "$payload"
  [ "$status" -eq 0 ]

  run_push_gate 'FANOUT_SKIP_PUSH_CHECK=1 git push origin HEAD' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'export FANOUT_SKIP_PUSH_CHECK=1 && git push origin HEAD' "$repo"
  [ "$status" -eq 0 ]
}

@test "push gate: a mere mention of the escape hatch does not bypass" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  run_push_gate 'echo FANOUT_SKIP_PUSH_CHECK=1 > note.txt; git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: ignores non-push commands and quoted push-like strings" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  run_push_gate 'ls -la' "$repo"
  [ "$status" -eq 0 ]

  # `git push` inside a quoted argument is not a command word.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git commit -m \\\"docs: explain git push flow\\\"\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 0 ]

  # A heredoc body mentioning git push is content, not a command.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat > doc.md <<'EOF'\\ngit push origin main\\nEOF\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 0 ]
}

@test "push gate: quote and subshell tricks cannot hide a real push" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  # Apostrophes inside double quotes must not pair up and swallow the push.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git commit -m \\\"isn't done\\\" && git push origin main && echo \\\"won't stop\\\"\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  run_push_gate '(git push origin main)' "$repo"
  [ "$status" -eq 2 ]

  run_push_gate 'if git push origin HEAD; then echo pushed; fi' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: quoted paths keep cd and git -C traceable" {
  local repo_a="$BATS_TEST_TMPDIR/repo-a"
  local repo_b="$BATS_TEST_TMPDIR/repo-b"
  setup_hook_repo "$repo_a"
  setup_hook_repo "$repo_b"
  write_marker "$repo_a" # only repo A is validated

  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git -C \\\"$repo_b\\\" push origin HEAD\"},\"cwd\":\"$repo_a\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cd \\\"$repo_b\\\" && git push origin HEAD\"},\"cwd\":\"$repo_a\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  write_marker "$repo_b"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 0 ]
}

@test "push gate: fails closed when the pushed repository cannot be resolved" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"

  run_push_gate 'cd /nonexistent-fanout-dir && git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'cd $WORKTREE && git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: --all validates every branch tip and --mirror fails closed" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" branch old-branch
  printf 'more\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm "second"
  write_marker "$repo" # old-branch's tip is one commit behind the marker

  run_push_gate 'git push --all origin' "$repo"
  [ "$status" -eq 2 ]

  git -C "$repo" branch -f old-branch HEAD
  run_push_gate 'git push --all origin' "$repo"
  [ "$status" -eq 0 ]

  # --mirror sends refs beyond refs/heads: always fail closed.
  run_push_gate 'git push --mirror origin' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: shell continuation and substitution forms are still pushes" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  # Backslash line continuation splices into one command.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git push \\\\\\n  origin HEAD\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  # Command substitution inside double quotes executes the push.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"result=\\\"\$(git push origin HEAD)\\\"\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  run_push_gate 'env -i git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # A command chained onto a quoted heredoc opener is not heredoc body.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat <<'EOF' && git push origin HEAD\\nbody\\nEOF\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
}

@test "push gate: repository-switching options fail closed" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"

  run_push_gate 'git --git-dir /elsewhere/.git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'git --work-tree=/elsewhere push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: implicit refspecs from git config are validated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" branch release
  printf 'more\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm "second"
  write_marker "$repo" # release's tip is one commit behind the marker

  # Bare `git push origin` normally validates HEAD only.
  run_push_gate 'git push origin' "$repo"
  [ "$status" -eq 0 ]

  # remote.<name>.push redirects the implicit refspec to a stale tip.
  git -C "$repo" config remote.origin.push refs/heads/release:refs/heads/release
  run_push_gate 'git push origin' "$repo"
  [ "$status" -eq 2 ]
  git -C "$repo" config --unset remote.origin.push

  # push.default=matching pushes every shared branch; so does a bare colon.
  git -C "$repo" config push.default matching
  run_push_gate 'git push origin' "$repo"
  [ "$status" -eq 2 ]
  git -C "$repo" config --unset push.default
  run_push_gate 'git push origin :' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: wrapper and config evasions stay gated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  run_push_gate '/usr/bin/git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate '>trace.log git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'env -C /elsewhere git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  write_marker "$repo"
  # An inline config override can change what a push sends: fail closed.
  run_push_gate 'git -c remote.origin.push=refs/heads/x:refs/heads/x push origin' "$repo"
  [ "$status" -eq 2 ]

  # Quote state resumes after "$(...)": the chained push is still seen.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"branch=\\\"\$(echo main)\\\" && git push origin stale-ref\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  # An unquoted heredoc body runs its substitutions.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat <<EOF\\n\$(git push origin stale-ref)\\nEOF\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
}

@test "push gate: --repo and forced deletion refspecs classify correctly" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  git -C "$repo" branch stale-branch
  printf 'more\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm "second"
  write_marker "$repo"

  # After --repo <remote>, the first positional is a refspec, not the remote.
  run_push_gate 'git push --repo origin stale-branch' "$repo"
  [ "$status" -eq 2 ]

  # +:branch is a forced deletion and stays ungated.
  run_push_gate 'git push origin +:old-branch' "$repo"
  [ "$status" -eq 0 ]
}

@test "push gate: a mirror remote fails closed" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"
  git -C "$repo" config remote.origin.mirror true

  # Even a marker-matching HEAD cannot vouch for every mirrored ref.
  run_push_gate 'git push origin' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: inner shells and gh pr create are gated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  # A push handed to an inner shell cannot be verified: fail closed.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"bash -lc 'git push origin HEAD'\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"sh -c \\\"git push origin main\\\"\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
  run_push_gate 'bash -c ls' "$repo"
  [ "$status" -eq 0 ]

  # gh pr create pushes the branch itself when it is not fully pushed.
  run_push_gate 'gh pr create --fill' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'gh pr view 12' "$repo"
  [ "$status" -eq 0 ]
  run_push_gate 'gh pr comment 12 --body create' "$repo"
  [ "$status" -eq 0 ]

  write_marker "$repo"
  run_push_gate 'gh pr create --fill' "$repo"
  [ "$status" -eq 0 ]
}

@test "push gate: a ref-mutating command before the push in one call is denied" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"

  # The gate resolves refs before the call runs; the commit would move HEAD.
  run_push_gate 'git commit -am wip && git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  [[ "$output" == *"単独のコマンド"* ]]
  run_push_gate 'git rebase origin/main && git push --force-with-lease origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # A push standalone (or after non-mutating commands) still passes.
  run_push_gate 'git status && git push origin HEAD' "$repo"
  [ "$status" -eq 0 ]
}

@test "push gate: substitutions, comments, and odd heredoc delimiters stay safe" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"

  # A substituted -C path is untraceable: fail closed even with a marker.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"git -C \\\"\$(git rev-parse --show-toplevel)\\\" push origin HEAD\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  local repo2="$BATS_TEST_TMPDIR/repo2"
  setup_hook_repo "$repo2"

  # An apostrophe in a comment must not swallow the next line's push.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"# don't retry\\ngit push origin HEAD\"},\"cwd\":\"$repo2\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  # A hyphenated heredoc delimiter still terminates the body.
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat <<'END-JSON'\\n{}\\nEND-JSON\\ngit push origin HEAD\"},\"cwd\":\"$repo2\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  run_push_gate 'GIT_DIR=/other/.git git push origin HEAD' "$repo2"
  [ "$status" -eq 2 ]
  run_push_gate 'git --config-env=remote.origin.push=SPEC push origin' "$repo2"
  [ "$status" -eq 2 ]
}

@test "push gate: timeout, env --unset, --namespace, and symbolic-ref stay gated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  # No marker: each form must be seen as an unvalidated push (or a mutation)
  # and denied, proving the wrapper/option is not silently skipped.

  # timeout wraps the real push.
  run_push_gate 'timeout 30 git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'timeout -s KILL 5 git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # env --unset takes a value; the push must still be seen.
  run_push_gate 'env --unset FOO git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # --namespace re-points the ref space: fail closed.
  run_push_gate 'git --namespace foo push origin main' "$repo"
  [ "$status" -eq 2 ]

  # symbolic-ref before a push changes the source ref.
  run_push_gate 'git symbolic-ref HEAD refs/heads/stale && git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # env -C before a commit is still a ref mutation ahead of the push.
  run_push_gate "env -C $repo git commit -am wip && git push origin HEAD" "$repo"
  [ "$status" -eq 2 ]

  # A tag pushed to an unqualified (branch-like) destination is gated.
  git -C "$repo" tag rel
  run_push_gate 'git push origin rel:main' "$repo"
  [ "$status" -eq 2 ]

  # time wraps the real push (POSIX -p and GNU value options).
  run_push_gate 'time git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'time -p git push origin HEAD' "$repo"
  [ "$status" -eq 2 ]

  # env --unset before an inner shell must still expose the push.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"env --unset FOO bash -c 'git push origin HEAD'\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  # A ref mutation before gh pr create in one call is stale.
  git -C "$repo" checkout -qb pr-branch
  git -C "$repo" rev-parse HEAD >"$repo/.git/fanout-check-passed"
  run_push_gate 'git commit --allow-empty -m wip && gh pr create --fill' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: eval, config-then-push, tag-then-push, and wrapped gh pr create stay gated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_marker "$repo"

  # eval executes its quoted arguments in the current shell.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"eval 'git push origin HEAD'\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]

  # Mutating config or a tag before a gated push in one call is stale.
  run_push_gate 'git config remote.origin.push refs/heads/x:refs/heads/x; git push origin' "$repo"
  [ "$status" -eq 2 ]
  run_push_gate 'git tag -f moveme HEAD && git push origin moveme:refs/heads/x' "$repo"
  [ "$status" -eq 2 ]
  # Same-call tag creation is unverifiable at gate time; split commands work.
  run_push_gate 'git tag v9.9.9 && git push origin v9.9.9' "$repo"
  [ "$status" -eq 2 ]
  git -C "$repo" tag v9.9.9
  run_push_gate 'git push origin v9.9.9' "$repo"
  [ "$status" -eq 0 ]

  # gh pr create behind an inner shell or env prefix is still PR creation.
  git -C "$repo" checkout -qb unpushed
  printf 'x\n' >>"$repo/tracked.txt"
  git -C "$repo" commit -aqm second # marker is now stale
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"bash -lc 'gh pr create --fill'\"},\"cwd\":\"$repo\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
  run_push_gate 'env -u FOO gh pr create --fill' "$repo"
  [ "$status" -eq 2 ]
}

@test "push gate: exported repo vars, env -S, pushDefault, and popd stay gated" {
  local repo_a="$BATS_TEST_TMPDIR/repo-a"
  local repo_b="$BATS_TEST_TMPDIR/repo-b"
  setup_hook_repo "$repo_a"
  setup_hook_repo "$repo_b"
  write_marker "$repo_a"

  # Exported GIT_DIR redirects every later git call: fail closed.
  run_push_gate 'export GIT_DIR=/other/.git GIT_WORK_TREE=/other; git push origin HEAD' "$repo_a"
  [ "$status" -eq 2 ]

  # env -S splices a command string the scanner cannot follow.
  local payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"env -S 'git push origin HEAD'\"},\"cwd\":\"$repo_a\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 2 ]
  payload="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"env -S 'ls -la'\"},\"cwd\":\"$repo_a\"}"
  run_hook "$PUSH_GATE" "$payload"
  [ "$status" -eq 0 ]

  # An inline remote.pushDefault override redirects the implicit refspec.
  run_push_gate 'git -c remote.pushDefault=evil push origin HEAD' "$repo_a"
  [ "$status" -eq 2 ]
  # include.path can pull push-affecting config in from a file.
  run_push_gate 'git -c include.path=/tmp/evil push origin HEAD' "$repo_a"
  [ "$status" -eq 2 ]

  # A path-prefixed env wrapper is still env.
  run_push_gate '/usr/bin/env git push origin HEAD' "$repo_b"
  [ "$status" -eq 2 ]
  # env -C before gh pr create leaves the target repo untraceable.
  run_push_gate 'env -C /elsewhere gh pr create --fill' "$repo_a"
  [ "$status" -eq 2 ]

  # popd returns to the original (unvalidated) repo before the push.
  run_push_gate "pushd $repo_a && popd && git push origin HEAD" "$repo_b"
  [ "$status" -eq 2 ]
  run_push_gate "pushd $repo_a && git push origin HEAD && popd" "$repo_b"
  [ "$status" -eq 0 ]
}

@test "push gate: fails closed when extraction fails but the payload mentions git push" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"

  run_hook "$PUSH_GATE" "{\"tool_name\":\"Bash\",\"note\":\"git push\",\"tool_input\":{},\"cwd\":\"$repo\"}"
  [ "$status" -eq 2 ]
  [[ "$output" == *"fail closed"* ]]
}

# --- stop gate (Codex backstop) ----------------------------------------------

stop_payload() {
  printf '{"hook_event_name":"Stop","stop_hook_active":false,"cwd":"%s"}' "$1"
}

write_failing_check_makefile() {
  printf 'check:\n\t@echo boom from stub check >&2; exit 1\n' >"$1/Makefile"
}

@test "stop gate: runs from a subdirectory by walking to the repo root" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  mkdir -p "$repo/web"

  run_hook "$STOP_GATE" "$(stop_payload "$repo/web")"
  [ "$status" -eq 2 ]
  [[ "$output" == *"boom from stub check"* ]]
}

@test "stop gate: skips when stop_hook_active is true" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile

  run_hook "$STOP_GATE" "{\"hook_event_name\":\"Stop\",\"stop_hook_active\":true,\"cwd\":\"$repo\"}"
  [ "$status" -eq 0 ]
}

@test "stop gate: skips on a dirty tree" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  printf 'wip\n' >>"$repo/tracked.txt"

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 0 ]
}

@test "stop gate: blocks a dirty stop when the pushed upstream tip is unvalidated" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local remote="$BATS_TEST_TMPDIR/remote.git"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  git init -q --bare "$remote"
  git -C "$repo" remote add origin "$remote"
  git -C "$repo" checkout -qb feature
  git -C "$repo" push -qu origin feature
  printf 'wip\n' >>"$repo/tracked.txt" # dirty, but HEAD is already pushed

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 2 ]
  [[ "$output" == *"push 済みの HEAD"* ]]
}

@test "stop gate: blocks a dirty stop when HEAD is any remote tip without upstream" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local remote="$BATS_TEST_TMPDIR/remote.git"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  git init -q --bare "$remote"
  git -C "$repo" remote add origin "$remote"
  git -C "$repo" checkout -qb feature
  # Push without -u: no upstream is configured, but the tip reaches origin.
  git -C "$repo" push -q origin feature
  git -C "$repo" fetch -q origin
  printf 'wip\n' >>"$repo/tracked.txt"

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 2 ]
  [[ "$output" == *"push 済みの HEAD"* ]]
}

@test "stop gate: skips when marker matches HEAD" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  write_marker "$repo"

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 0 ]
}

@test "stop gate: skips when HEAD is an ancestor of the origin default branch" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile
  git -C "$repo" update-ref refs/remotes/origin/main HEAD

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 0 ]
}

@test "stop gate: blocks on a failing check and reports the log path" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  write_failing_check_makefile "$repo"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 2 ]
  [[ "$output" == *"fanout stop gate"* ]]
  [[ "$output" == *"fanout-check.log"* ]]
  [[ "$output" == *"boom from stub check"* ]]
  [ -f "$repo/.git/fanout-check.log" ]
}

@test "stop gate: passes on a green check and honors FANOUT_SKIP_STOP_GATE=1" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_hook_repo "$repo"
  printf 'check:\n\t@echo stub check ok\n' >"$repo/Makefile"
  git -C "$repo" add Makefile && git -C "$repo" commit -qm makefile

  run_hook "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 0 ]

  write_failing_check_makefile "$repo"
  git -C "$repo" commit -aqm failing
  run bash -c 'printf "%s" "$1" | env -u CLAUDE_PROJECT_DIR FANOUT_SKIP_STOP_GATE=1 bash "$0" 2>&1' "$STOP_GATE" "$(stop_payload "$repo")"
  [ "$status" -eq 0 ]
}

# --- check-marker (Makefile) ---------------------------------------------------

@test "check-marker: writes HEAD only on a clean tree" {
  local sandbox="$BATS_TEST_TMPDIR/sandbox"
  mkdir -p "$sandbox"
  cp "$REPO_ROOT/Makefile" "$REPO_ROOT/.golangci-lint-version" "$sandbox/"
  git -C "$sandbox" init -q
  git -C "$sandbox" config user.email "fanout-test@example.com"
  git -C "$sandbox" config user.name "fanout test"
  git -C "$sandbox" add Makefile .golangci-lint-version
  git -C "$sandbox" commit -qm initial

  # Untracked files count as dirty (git status --porcelain -uall).
  printf 'scratch\n' >"$sandbox/scratch.txt"
  run bash -c 'make -C "$0" --no-print-directory check-marker 2>&1' "$sandbox"
  [ "$status" -eq 0 ]
  [[ "$output" == *"marker not written"* ]]
  [ ! -f "$sandbox/.git/fanout-check-passed" ]

  rm "$sandbox/scratch.txt"
  run bash -c 'make -C "$0" --no-print-directory check-marker 2>&1' "$sandbox"
  [ "$status" -eq 0 ]
  [ "$(cat "$sandbox/.git/fanout-check-passed")" = "$(git -C "$sandbox" rev-parse HEAD)" ]
}

# --- format-on-edit ------------------------------------------------------------

format_payload() {
  printf '{"tool_name":"Edit","tool_input":{"file_path":"%s"},"cwd":"%s"}' "$1" "$2"
}

@test "format-on-edit: exits 0 and leaves the file untouched when the toolchain is absent" {
  local repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo"
  printf 'vTEST-does-not-exist\n' >"$repo/.golangci-lint-version"
  printf 'package main\n\nfunc main() {\nprintln( "x" )\n}\n' >"$repo/bad.go"

  run bash -c 'printf "%s" "$1" | env -u CLAUDE_PROJECT_DIR FANOUT_DEV_CACHE_DIR="$2" bash "$0" 2>&1' \
    "$FORMAT_HOOK" "$(format_payload "$repo/bad.go" "$repo")" "$BATS_TEST_TMPDIR/empty-cache"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println( "x" )'* ]]
}

@test "format-on-edit: ignores non-target extensions" {
  local repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo"
  printf '#  messy   markdown\n' >"$repo/README.md"

  run_hook "$FORMAT_HOOK" "$(format_payload "$repo/README.md" "$repo")"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/README.md")" == '#  messy   markdown' ]]
}

@test "format-on-edit: formats a .go file when the pinned golangci-lint is cached" {
  local cache_root="${FANOUT_DEV_CACHE_DIR:-/tmp/fanout-dev-cache-$(id -u)}"
  local version
  version="$(tr -d '[:space:]' <"$REPO_ROOT/.golangci-lint-version")"
  [ -x "$cache_root/tools/golangci-lint-$version" ] || skip "pinned golangci-lint not cached locally"

  local repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo/sub"
  git -C "$repo" init -q
  cp "$REPO_ROOT/.golangci-lint-version" "$REPO_ROOT/.golangci.yml" "$repo/"
  printf 'package main\n\nfunc main() {\nprintln( "x" )\n}\n' >"$repo/bad.go"

  run_hook "$FORMAT_HOOK" "$(format_payload "$repo/bad.go" "$repo")"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println("x")'* ]]

  # A subdirectory cwd resolves the repo root for the version pin.
  printf 'package main\n\nfunc main() {\nprintln( "y" )\n}\n' >"$repo/bad.go"
  run_hook "$FORMAT_HOOK" "$(format_payload "$repo/bad.go" "$repo/sub")"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println("y")'* ]]
}

@test "format-on-edit: formats paths from a Codex apply_patch payload" {
  local cache_root="${FANOUT_DEV_CACHE_DIR:-/tmp/fanout-dev-cache-$(id -u)}"
  local version
  version="$(tr -d '[:space:]' <"$REPO_ROOT/.golangci-lint-version")"
  [ -x "$cache_root/tools/golangci-lint-$version" ] || skip "pinned golangci-lint not cached locally"

  local repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo"
  git -C "$repo" init -q
  cp "$REPO_ROOT/.golangci-lint-version" "$REPO_ROOT/.golangci.yml" "$repo/"
  printf 'package main\n\nfunc main() {\nprintln( "x" )\n}\n' >"$repo/bad.go"

  local payload="{\"tool_name\":\"apply_patch\",\"tool_input\":{\"command\":\"*** Begin Patch\\n*** Update File: bad.go\\n@@\\n-x\\n+y\\n*** End Patch\"},\"cwd\":\"$repo\"}"
  run_hook "$FORMAT_HOOK" "$payload"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println("x")'* ]]
}

@test "format-on-edit: refuses a foreign or symlinked shared cache" {
  local repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo"
  cp "$REPO_ROOT/.golangci-lint-version" "$repo/"
  printf 'package main\n\nfunc main() {\nprintln( "x" )\n}\n' >"$repo/bad.go"

  # A symlinked cache root is rejected, so the file stays untouched even if
  # the link points at a real, populated cache.
  local real_cache="${FANOUT_DEV_CACHE_DIR:-/tmp/fanout-dev-cache-$(id -u)}"
  ln -s "$real_cache" "$BATS_TEST_TMPDIR/evil-cache"
  run bash -c 'printf "%s" "$1" | env -u CLAUDE_PROJECT_DIR -u GOLANGCI_LINT_BIN FANOUT_DEV_CACHE_DIR="$2" bash "$0" 2>&1' \
    "$FORMAT_HOOK" "$(format_payload "$repo/bad.go" "$repo")" "$BATS_TEST_TMPDIR/evil-cache"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println( "x" )'* ]]
}

# --- hooks lib -----------------------------------------------------------------

@test "hooks lib: json_field decodes escaped command strings" {
  local payload='{"cwd":"/tmp/x","tool_input":{"command":"git commit -m \"say \\\\ hi\" && ls"}}'
  run bash -c '. "$0"; json_field "$1" command' "$HOOKS_LIB" "$payload"
  [ "$status" -eq 0 ]
  # JSON \\\\ decodes to two literal backslashes.
  [ "$output" = 'git commit -m "say \\ hi" && ls' ]

  run bash -c '. "$0"; json_field "$1" cwd' "$HOOKS_LIB" "$payload"
  [ "$output" = "/tmp/x" ]

  # \uXXXX decodes to a placeholder so adjacent tokens never merge.
  local upayload='{"tool_input":{"command":"echo A\u0022B"}}'
  run bash -c '. "$0"; json_field "$1" command' "$HOOKS_LIB" "$upayload"
  [ "$output" = "echo A_B" ]
}
