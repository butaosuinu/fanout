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
  mkdir -p "$repo"
  cp "$REPO_ROOT/.golangci-lint-version" "$REPO_ROOT/.golangci.yml" "$repo/"
  printf 'package main\n\nfunc main() {\nprintln( "x" )\n}\n' >"$repo/bad.go"

  run_hook "$FORMAT_HOOK" "$(format_payload "$repo/bad.go" "$repo")"
  [ "$status" -eq 0 ]
  [[ "$(cat "$repo/bad.go")" == *'println("x")'* ]]
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
