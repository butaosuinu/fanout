#!/usr/bin/env bats
#
# Tier 1 — flag validation and prerequisite checks.
#
# Locks in the CLI surface identified as Invariant #5 in issue #20: every
# flag's error message and exit code is frozen here so the future Go
# rewrite can target the same contract. No live tmux panes, no GitHub network —
# every case fails before fanout reaches its external collaborators.
#
# Exit code convention (matches fanout:140-143, 338-342, 356-359):
#   0 = success (help)
#   1 = prerequisite / invocation problem routed through `die` (includes
#       --limit / --sleep / --popup-timeout / --only|--skip validation)
#   2 = positional missing, unknown option, unexpected positional
#
# Issue #20 narrative says "exit 2" for --limit abc et al., but the
# implementation uses `die` (exit 1). Per the invariant "CLI surface is 1:1
# frozen", we keep the implementation's behavior as truth; the issue text
# is an unintentional inconsistency.

load helpers

@test "prepare-dev-cache rejects symlinks and makes owned caches private" {
  local cache="$BATS_TEST_TMPDIR/dev-cache"
  local cache_link="$BATS_TEST_TMPDIR/dev-cache-link"
  local mode

  mkdir "$cache"
  chmod 755 "$cache"
  ln -s "$cache" "$cache_link"

  run env CI= make -C "$REPO_ROOT" --no-print-directory \
    FANOUT_DEV_CACHE_DIR="$cache_link" prepare-dev-cache
  [ "$status" -ne 0 ]
  [[ "$output" == *"fanout dev cache must not be a symlink"* ]]

  run env CI= make -C "$REPO_ROOT" --no-print-directory \
    FANOUT_DEV_CACHE_DIR="$cache" prepare-dev-cache
  [ "$status" -eq 0 ]

  if mode="$(stat -f '%Lp' "$cache" 2>/dev/null)"; then
    :
  else
    mode="$(stat -c '%a' "$cache")"
  fi
  [ "$mode" = "700" ]
}

@test "prepare-dev-cache ignores a repo-local TMPDIR" {
  local sandbox_repo="$BATS_TEST_TMPDIR/project"
  local repo_tmp="$sandbox_repo/tmp"
  local status_before
  local status_after

  mkdir -p "$sandbox_repo"
  cp "$REPO_ROOT/Makefile" "$REPO_ROOT/.golangci-lint-version" "$sandbox_repo/"
  git -C "$sandbox_repo" init -q
  git -C "$sandbox_repo" add Makefile .golangci-lint-version
  status_before="$(git -C "$sandbox_repo" status --short)"

  run env CI= TMPDIR="$repo_tmp" make -C "$sandbox_repo" --no-print-directory \
    prepare-dev-cache
  [ "$status" -eq 0 ]
  [ ! -e "$repo_tmp" ]

  status_after="$(git -C "$sandbox_repo" status --short)"
  [ "$status_after" = "$status_before" ]
}

# --- Help & usage -----------------------------------------------------------

@test "-h prints usage and exits 0" {
  run_fanout -h
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout"* ]]
  [[ "$output" == *"fanout plan <spec.json|plan-slug> [options]"* ]]
}

@test "--help prints usage and exits 0" {
  run_fanout --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout"* ]]
  [[ "$output" == *"plan                Subcommand."* ]]
  [[ "$output" == *"update              Subcommand."* ]]
  [[ "$output" == *"--codex-plan-mode / --no-codex-plan-mode"* ]]
  [[ "$output" == *"Exit codes (update):"* ]]
  [[ "$output" == *"1 prerequisite / environment problem, or missing option value"* ]]
  [[ "$output" == *"2 unknown option, unexpected argument, or cannot compare version strings"* ]]
}

@test "--check-update on dev build exits 0 without gh" {
  force_missing gh
  run_fanout --check-update
  [ "$status" -eq 0 ]
  [[ "$output" == *"fanout dev build"* ]]
  [[ "$output" == *"--check-update only works for released versions"* ]]
}

@test "check-update subcommand on dev build exits 0 without gh" {
  force_missing gh
  run_fanout check-update
  [ "$status" -eq 0 ]
  [[ "$output" == *"fanout dev build"* ]]
}

@test "--check-update release build reports update available from fixture" {
  local release_bin="$BATS_TEST_TMPDIR/fanout-release"
  GOCACHE="$REPO_ROOT/.cache/go-build" go build \
    -ldflags "-X main.version=v0.1.0 -X main.commit=test" \
    -o "$release_bin" ./cmd/fanout
  use_fixture scenario-check-update

  local old_bin="$FANOUT_BIN"
  FANOUT_BIN="$release_bin"
  run_fanout --check-update
  FANOUT_BIN="$old_bin"

  [ "$status" -eq 0 ]
  [[ "$output" == *"fanout update available: v0.1.0 -> v0.2.0"* ]]
}

@test "update --help prints usage and exits 0" {
  run_fanout update --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout update"* ]]
}

@test "update unknown flag: error + usage + exit 2" {
  run_fanout update --bogus
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown update option: --bogus"* ]]
  [[ "$output" == *"Usage: fanout update"* ]]
}

@test "update --version missing value: exit 1" {
  run_fanout update --version
  [ "$status" -eq 1 ]
  [[ "$output" == *"--version requires an argument"* ]]
}

@test "update dev build rejects replacement before gh/downloader" {
  force_missing gh curl wget
  run_fanout update
  [ "$status" -eq 1 ]
  [[ "$output" == *"fanout dev build"* ]]
  [[ "$output" == *"update only replaces released versions"* ]]
}

@test "update release build requires curl or wget before installer" {
  local release_dir="$BATS_TEST_TMPDIR/release-missing-downloader"
  mkdir -p "$release_dir"
  local release_bin="$release_dir/fanout"
  GOCACHE="$REPO_ROOT/.cache/go-build" go build \
    -ldflags "-X main.version=v0.1.0 -X main.commit=test" \
    -o "$release_bin" ./cmd/fanout
  use_fixture scenario-check-update
  force_missing curl wget

  local old_bin="$FANOUT_BIN"
  FANOUT_BIN="$release_bin"
  run_fanout update
  FANOUT_BIN="$old_bin"

  [ "$status" -eq 1 ]
  [[ "$output" == *"curl or wget is required"* ]]
}

@test "update already latest skips installer" {
  local release_dir="$BATS_TEST_TMPDIR/release-current"
  mkdir -p "$release_dir"
  local release_bin="$release_dir/fanout"
  GOCACHE="$REPO_ROOT/.cache/go-build" go build \
    -ldflags "-X main.version=v0.2.0 -X main.commit=test" \
    -o "$release_bin" ./cmd/fanout
  use_fixture scenario-check-update
  force_missing curl wget

  local old_bin="$FANOUT_BIN"
  FANOUT_BIN="$release_bin"
  run_fanout update
  FANOUT_BIN="$old_bin"

  [ "$status" -eq 0 ]
  [[ "$output" == *"fanout is already up to date: v0.2.0"* ]]
}

@test "plan --help prints usage and exits 0" {
  run_fanout plan --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout plan"* ]]
  [[ "$output" == *"--only <task-id[,id...]>"* ]]
}

@test "plan missing spec: usage + exit 2" {
  run_fanout plan
  [ "$status" -eq 2 ]
  [[ "$output" == *"Usage: fanout plan"* ]]
}

@test "plan unknown flag rejects issue-mode-only --include: exit 2" {
  run_fanout plan "$BATS_TEST_TMPDIR/spec.json" --include 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown plan option: --include"* ]]
  [[ "$output" == *"Usage: fanout plan"* ]]
}

@test "plan rejects --only and --skip together: exit 1" {
  run_fanout plan "$BATS_TEST_TMPDIR/spec.json" --only api-client --skip base-types
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only and --skip are mutually exclusive"* ]]
}

@test "plan --status missing gh: preflight exit 1" {
  force_missing gh
  run_fanout plan launch-plan --status
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"gh (brew install gh)"* ]]
}

@test "plan --cleanup missing gh: preflight exit 1" {
  force_missing gh
  run_fanout plan launch-plan --cleanup
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"gh (brew install gh)"* ]]
}

@test "plan missing spec file: exit 1" {
  run_fanout plan "$BATS_TEST_TMPDIR/missing.json" --dry-run --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"read plan spec"* ]]
}

@test "plan invalid JSON: exit 1" {
  local spec="$BATS_TEST_TMPDIR/invalid.json"
  printf '{"version":' > "$spec"

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"parse plan spec"* ]]
}

@test "plan version mismatch: exit 1" {
  local spec="$BATS_TEST_TMPDIR/version.json"
  cat > "$spec" <<'JSON'
{
  "version": 2,
  "plan": {"slug": "launch-plan", "title": "Launch plan"},
  "tasks": [{"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"}]
}
JSON

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"version must be 1, got 2"* ]]
}

@test "plan duplicate task id: exit 1" {
  local spec="$BATS_TEST_TMPDIR/duplicate.json"
  cat > "$spec" <<'JSON'
{
  "version": 1,
  "plan": {"slug": "launch-plan", "title": "Launch plan"},
  "tasks": [
    {"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"},
    {"id": "api-client", "title": "Extract API client again", "briefing": "## Goal\nExtract it again"}
  ]
}
JSON

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"duplicates tasks[0].id"* ]]
}

@test "plan duplicate final slug: exit 1" {
  local spec="$BATS_TEST_TMPDIR/duplicate-final-slug.json"
  cat > "$spec" <<'JSON'
{
  "version": 1,
  "plan": {"slug": "launch-plan", "title": "Launch plan"},
  "tasks": [
    {"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"},
    {"id": "worker", "title": "Worker", "slug": "launch-plan-extract-api-client-api-client", "briefing": "## Goal\nBuild it"}
  ]
}
JSON

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"final slug"* ]]
}

@test "plan duplicate final branch: exit 1" {
  local spec="$BATS_TEST_TMPDIR/duplicate-final-branch.json"
  cat > "$spec" <<'JSON'
{
  "version": 1,
  "plan": {"slug": "launch-plan", "title": "Launch plan"},
  "tasks": [
    {"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"},
    {"id": "worker", "title": "Worker", "branch": "fanout/launch-plan-extract-api-client-api-client", "briefing": "## Goal\nBuild it"}
  ]
}
JSON

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"final branch"* ]]
}

@test "plan blocked_by cycle: exit 1" {
  local spec="$BATS_TEST_TMPDIR/cycle.json"
  cat > "$spec" <<'JSON'
{
  "version": 1,
  "plan": {"slug": "launch-plan", "title": "Launch plan"},
  "tasks": [
    {"id": "base-types", "title": "Define base types", "briefing": "## Goal\nDefine it", "blocked_by": ["api-client"]},
    {"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it", "blocked_by": ["base-types"]}
  ]
}
JSON

  run_fanout plan "$spec" --dry-run --agent claude

  [ "$status" -eq 1 ]
  [[ "$output" == *"blocked_by cycle detected"* ]]
}

@test "no positional argument enters TUI mode: outside git exits 1" {
  local outside="$BATS_TEST_TMPDIR/outside-git"
  mkdir -p "$outside"

  run bash -c "cd '$outside' && '$FANOUT_BIN' 2>&1"

  [ "$status" -eq 1 ]
  [[ "$output" == *"current directory is not inside a git work tree"* ]]
  [[ "$output" != *"Usage: fanout"* ]]
}

@test "unknown long option: error + usage + exit 2" {
  run_fanout --not-a-flag
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option: --not-a-flag"* ]]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "unexpected positional after parent: error + usage + exit 2" {
  run_fanout 20 extra-word
  [ "$status" -eq 2 ]
  [[ "$output" == *"unexpected positional argument: extra-word"* ]]
}

# --- Positional / required-argument validation -----------------------------

@test "parent must be an integer or Projects v2 URL: exit 1" {
  run_fanout abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL, got: abc"* ]]
}

@test "parent URL with wrong host rejected: exit 1" {
  run_fanout https://example.com/users/foo/projects/3
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "parent URL missing project number rejected: exit 1" {
  run_fanout https://github.com/users/foo/projects/
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "--project-status missing value: exit 1" {
  run_fanout 20 --project-status
  [ "$status" -eq 1 ]
  [[ "$output" == *"--project-status requires an argument"* ]]
}

@test "--base-branch missing value: exit 1" {
  run_fanout 20 --base-branch
  [ "$status" -eq 1 ]
  [[ "$output" == *"--base-branch requires an argument"* ]]
}

@test "--branch-prefix missing value: exit 1" {
  run_fanout 20 --branch-prefix
  [ "$status" -eq 1 ]
  [[ "$output" == *"--branch-prefix requires an argument"* ]]
}

@test "--close missing value: exit 1" {
  run_fanout 20 --close
  [ "$status" -eq 1 ]
  [[ "$output" == *"--close requires an argument"* ]]
}

@test "--merge missing value: exit 1" {
  run_fanout 20 --merge
  [ "$status" -eq 1 ]
  [[ "$output" == *"--merge requires an argument"* ]]
}

# Project URL parser must accept the canonical links users copy from GitHub
# Projects: the bare /projects/<n>, /projects/<n>/views/<id>, and either
# form with a ?query suffix. We only assert the parser does not reject
# them — downstream stages (tmux/gh) fail in the test env but that's not
# the parser's concern.
@test "Projects URL bare form is accepted by parser" {
  run_fanout 'https://github.com/users/foo/projects/3'
  [[ "$output" != *"parent must be"* ]]
}

@test "Projects URL with /views/<id> is accepted by parser" {
  run_fanout 'https://github.com/orgs/bar/projects/7/views/1'
  [[ "$output" != *"parent must be"* ]]
}

@test "Projects URL with query string is accepted by parser" {
  run_fanout 'https://github.com/users/foo/projects/3?filterQuery=is%3Aopen'
  [[ "$output" != *"parent must be"* ]]
}

@test "--agent missing value: exit 1" {
  run_fanout 20 --agent
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent requires an argument"* ]]
}

@test "--agent per-issue empty name rejected: exit 1" {
  run_fanout 20 --agent 4=
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent #4: agent name must not be empty"* ]]
}

@test "--agent per-issue empty NUM rejected: exit 1" {
  run_fanout 20 --agent =claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent: <NUM> must be a positive integer"* ]]
}

@test "--agent per-issue non-integer NUM rejected: exit 1" {
  run_fanout 20 --agent foo=claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent: <NUM> must be a positive integer"* ]]
}

@test "plan --agent per-task empty name rejected: exit 1" {
  run_fanout plan launch-plan --agent api-client=
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent api-client: agent name must not be empty"* ]]
}

@test "plan --agent per-task invalid task id rejected: exit 1" {
  run_fanout plan launch-plan --agent Api=codex
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent: <task-id> must be lowercase kebab-case"* ]]
}

@test "--limit missing value: exit 1" {
  run_fanout 20 --limit
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit requires an argument"* ]]
}

# --- Numeric / regex validation --------------------------------------------

@test "--limit abc: exit 1" {
  run_fanout 20 --limit abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit must be a positive integer, got: abc"* ]]
}

@test "--limit 0 (positive-integer regex rejects zero): exit 1" {
  run_fanout 20 --limit 0
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit must be a positive integer, got: 0"* ]]
}

@test "--sleep non-numeric: exit 1" {
  run_fanout 20 --sleep X
  [ "$status" -eq 1 ]
  [[ "$output" == *"--sleep must be a number, got: X"* ]]
}

@test "--popup-timeout 0 (positive-integer regex rejects zero): exit 1" {
  run_fanout 20 --popup-timeout 0
  [ "$status" -eq 1 ]
  [[ "$output" == *"--popup-timeout must be a positive integer (seconds), got: 0"* ]]
}

@test "--base-branch containing whitespace rejected: exit 1" {
  run_fanout 20 --base-branch "bad branch"
  [ "$status" -eq 1 ]
  [[ "$output" == *"--base-branch must not contain whitespace"* ]]
}

@test "--branch-prefix containing whitespace rejected: exit 1" {
  run_fanout 20 --branch-prefix "bad prefix/"
  [ "$status" -eq 1 ]
  [[ "$output" == *"--branch-prefix must not contain whitespace"* ]]
}

@test "--close non-integer: exit 1" {
  run_fanout 20 --close abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--close must be a positive integer, got: abc"* ]]
}

@test "--merge zero rejected: exit 1" {
  run_fanout 20 --merge 0
  [ "$status" -eq 1 ]
  [[ "$output" == *"--merge must be a positive integer, got: 0"* ]]
}

# --- --only / --skip / --include CSV parsing -------------------------------

@test "--only and --skip are mutually exclusive: exit 1" {
  run_fanout 20 --only 4 --skip 5
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only and --skip are mutually exclusive"* ]]
}

@test "--close and --merge are mutually exclusive: exit 2" {
  run_fanout 20 --close 4 --merge 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--close, --merge, and --cleanup are mutually exclusive"* ]]
}

@test "--cleanup conflicts with action flags: exit 2" {
  run_fanout 20 --cleanup --agent claude
  [ "$status" -eq 2 ]
  [[ "$output" == *"--close/--merge/--cleanup cannot be combined with --agent"* ]]
}

@test "--cleanup conflicts with per-issue --agent: exit 2" {
  run_fanout 20 --cleanup --agent 4=claude
  [ "$status" -eq 2 ]
  [[ "$output" == *"--close/--merge/--cleanup cannot be combined with --agent"* ]]
}

@test "--close conflicts with --team: exit 2" {
  run_fanout 20 --close 4 --team
  [ "$status" -eq 2 ]
  [[ "$output" == *"--close/--merge/--cleanup cannot be combined with --team"* ]]
}

@test "--status conflicts with --team: exit 2" {
  run_fanout --status 1 --team
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --team"* ]]
}

@test "--only with non-integer entry: exit 1" {
  run_fanout 20 --only 4,abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only: invalid entry 'abc'"* ]]
}

@test "--only with empty entry (double comma): exit 1" {
  run_fanout 20 --only 4,,5
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only: invalid entry ''"* ]]
}

@test "--skip with non-integer entry: exit 1" {
  run_fanout 20 --skip foo
  [ "$status" -eq 1 ]
  [[ "$output" == *"--skip: invalid entry 'foo'"* ]]
}

@test "--include with non-integer entry: exit 1" {
  run_fanout 20 --include bar
  [ "$status" -eq 1 ]
  [[ "$output" == *"--include: invalid entry 'bar'"* ]]
}

# --- --name NUM=<slug-hint>[|<display-name>[|<branch-name>]] ---------------

@test "--name: NUM must be a positive integer: exit 1" {
  run_fanout 20 --name foo=bar
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name: <NUM> must be a positive integer"* ]]
}

@test "--name: slug-hint with uppercase rejected: exit 1" {
  run_fanout 20 --name 4=aBc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: slug-hint must be lowercase kebab-case"* ]]
}

@test "--name: all three segments empty (NUM=||) rejected: exit 1" {
  run_fanout 20 --name 4=||
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: slug-hint, display-name, and branch-name are all empty"* ]]
}

@test "--name: more than 3 '|'-separated segments rejected: exit 1" {
  run_fanout 20 --name 4=slug\|disp\|branch\|extra
  [ "$status" -eq 1 ]
  [[ "$output" == *"too many '|'-separated segments"* ]]
}

@test "--name: branch-name containing whitespace rejected: exit 1" {
  run_fanout 20 --name "4=slug|disp|bad branch"
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: branch-name must not contain whitespace"* ]]
}

# --- Prerequisite detection (missing_deps[]) --------------------------------
# force_missing rebuilds PATH to exclude the named command(s) while keeping
# jq / awk / grep / ... reachable so fanout can still run its prereq loop.

@test "missing gh: exit 1" {
  force_missing gh
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"gh (brew install gh)"* ]]
}

@test "missing tmux: exit 1" {
  force_missing tmux
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"tmux 3.3+ (brew install tmux)"* ]]
}

@test "missing git: exit 1" {
  force_missing git
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"git"* ]]
}

@test "--agent missing in action mode: explicit error" {
  run_fanout 20
  [ "$status" -eq 1 ]
  [[ "$output" == *"agent is required; pass --agent <name> or set FANOUT_AGENT"* ]]
}

@test "FANOUT_AGENT supplies default agent" {
  export FANOUT_AGENT=claude
  run_fanout 20 --dry-run --limit 1
  [[ "$output" != *"agent is required"* ]]
}

@test "--codex-plan-mode requires --agent codex: exit 1" {
  use_fixture scenario-sub-issue-only
  run_fanout 100 --agent claude --codex-plan-mode --dry-run
  [ "$status" -eq 1 ]
  [[ "$output" == *"codex plan mode requires every selected child to use agent codex"* ]]
}

@test "--codex-plan-mode rejects non-codex per-issue override: exit 1" {
  use_fixture scenario-sub-issue-only
  run_fanout 100 --agent codex --agent 101=claude --codex-plan-mode --dry-run
  [ "$status" -eq 1 ]
  [[ "$output" == *"codex plan mode requires every selected child to use agent codex"* ]]
}

@test "FANOUT_CODEX_PLAN_MODE rejects a non-codex child before launch" {
  use_fixture scenario-sub-issue-only
  export FANOUT_CODEX_PLAN_MODE=1
  run_fanout 100 --agent codex --agent 101=claude --dry-run
  [ "$status" -eq 1 ]
  [[ "$output" == *"codex plan mode requires every selected child to use agent codex"* ]]
  [[ "$output" == *"#101 resolves to claude"* ]]
}

@test "outside tmux: explicit error" {
  unset TMUX
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"fanout must be run inside tmux"* ]]
}

# --- --status CLI surface ---------------------------------------------------
# --status uses its own exit-code lane (0/2/3) per issue #35: 0=JSON emitted,
# 2=cannot enumerate (bad invocation, missing/malformed state),
# 3=gh API failure. The cases below cover the CLI surface only — the body /
# JSON shape lives in tier2_status.bats against fixtures.

@test "--status without parent: exit 2" {
  run_fanout --status
  [ "$status" -eq 2 ]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "--status non-integer parent: exit 2" {
  run_fanout --status abc
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "--status with Projects v2 URL parent: exit 2" {
  # --status only makes sense for integer-issue parents (panes carry
  # `[fanout #N of #<issue-number>]`). Projects URLs are not addressable
  # via that prefix, so the combination is rejected up-front.
  run_fanout --status 'https://github.com/users/foo/projects/3'
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with Projects v2 URLs as parent"* ]]
}

@test "--status conflicts with --agent: exit 2" {
  run_fanout --status 1 --agent claude
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --agent"* ]]
}

@test "--status conflicts with per-issue --agent: exit 2" {
  run_fanout --status 1 --agent 4=claude
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --agent"* ]]
}

@test "plan --status conflicts with per-task --agent: exit 2" {
  run_fanout plan launch-plan --status --agent api-client=codex
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --agent"* ]]
}

@test "plan --close conflicts with per-task --agent: exit 2" {
  run_fanout plan launch-plan --close api-client --agent api-client=codex
  [ "$status" -eq 2 ]
  [[ "$output" == *"--close/--merge/--cleanup cannot be combined with --agent"* ]]
}

@test "--status conflicts with --base-branch: exit 2" {
  run_fanout --status 1 --base-branch main
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --base-branch"* ]]
}

@test "--status conflicts with --branch-prefix: exit 2" {
  run_fanout --status 1 --branch-prefix fanout/
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --branch-prefix"* ]]
}

@test "--status conflicts with --close: exit 2" {
  run_fanout --status 1 --close 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --close"* ]]
}

@test "--status conflicts with --merge: exit 2" {
  run_fanout --status 1 --merge 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --merge"* ]]
}

@test "--status conflicts with --cleanup: exit 2" {
  run_fanout --status 1 --cleanup
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --cleanup"* ]]
}

@test "--status conflicts with --limit: exit 2" {
  run_fanout --status 1 --limit 3
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --limit"* ]]
}

@test "--status conflicts with --only: exit 2" {
  run_fanout --status 1 --only 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --only"* ]]
}

@test "--status conflicts with --skip: exit 2" {
  run_fanout --status 1 --skip 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --skip"* ]]
}

@test "--status conflicts with --include: exit 2" {
  run_fanout --status 1 --include 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --include"* ]]
}

@test "--status conflicts with --dry-run: exit 2" {
  run_fanout --status 1 --dry-run
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --dry-run"* ]]
}

@test "--status conflicts with --codex-plan-mode: exit 2" {
  run_fanout --status 1 --codex-plan-mode
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --codex-plan-mode"* ]]
}

@test "--status conflicts with --unblocked-only: exit 2" {
  run_fanout --status 1 --unblocked-only
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --unblocked-only"* ]]
}

@test "--status conflicts with --no-refresh: exit 2" {
  run_fanout --status 1 --no-refresh
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --no-refresh"* ]]
}

@test "--status conflicts with --name: exit 2" {
  run_fanout --status 1 --name 4=foo
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status conflicts with --session: exit 2" {
  run_fanout --status 1 --session work-repo
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --session"* ]]
}

@test "--status conflicts with --sleep even at default value: exit 2" {
  # Wrappers that always pass `--sleep 4` (the default) must still trigger
  # the exclusivity error. Detection is by flag presence, not value diff.
  run_fanout --status 1 --sleep 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --sleep"* ]]
}

@test "--status conflicts with --popup-timeout even at default value: exit 2" {
  run_fanout --status 1 --popup-timeout 20
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --popup-timeout"* ]]
}

@test "--status with bogus --sleep value: exclusivity wins (exit 2), not numeric die (exit 1)" {
  # Callers branch on --status's dedicated exit codes (0/2/3). The
  # exclusivity check must run before the `--sleep must be a number` die,
  # otherwise an invalid combination leaks through as a main-flow exit 1.
  run_fanout --status 1 --sleep nope
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --sleep"* ]]
}

@test "--status with bogus --popup-timeout value: exclusivity wins (exit 2)" {
  run_fanout --status 1 --popup-timeout zero
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --popup-timeout"* ]]
}

@test "--status with bogus --limit value: exclusivity wins (exit 2)" {
  run_fanout --status 1 --limit abc
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --limit"* ]]
}

@test "--status with bogus --name value: exclusivity wins (exit 2), not parse_name_arg die (exit 1)" {
  # --name normally die's inside parse_name_arg if the slug-hint/NUM is
  # malformed. Combined with --status, the conflict must surface first so
  # callers see the documented --status exit code 2.
  run_fanout --status 1 --name bad
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status with bogus --name slug-hint: exclusivity wins (exit 2)" {
  # Same as above but the malformed part is the slug-hint (would normally
  # die with "slug-hint must be lowercase kebab-case").
  run_fanout --status 1 --name 4=BAD
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status with FANOUT_STATE_PATH does not require tmux: exit 0" {
  # Offline contract: an empty state file plus FANOUT_STATE_PATH lets
  # `--status` complete without tmux installed. With no fanned children,
  # cmdStatus emits a zero summary before reaching any gh call.
  force_missing tmux
  local root="$BATS_TEST_TMPDIR/project"
  mkdir -p "$root/.fanout"
  printf '{"schemaVersion":1,"panes":[]}\n' > "$root/.fanout/state.json"
  export FANOUT_STATE_PATH="$root/.fanout/state.json"
  run_fanout --status 1
  [ "$status" -eq 0 ]
  [[ "$output" == *'"all_merged": false'* ]]
  [[ "$output" == *'"total": 0'* ]]
}

@test "--status without FANOUT_STATE_PATH still does not require tmux: exit 0" {
  local root="$BATS_TEST_TMPDIR/gitroot"
  mkdir -p "$root"
  git -C "$root" init --quiet
  (
    cd "$root"
    run_fanout --status 1
    [ "$status" -eq 0 ]
    [[ "$output" == *'"total": 0'* ]]
  )
}

@test "--status outside git without FANOUT_STATE_PATH: exit 2" {
  local root="$BATS_TEST_TMPDIR/notgit"
  mkdir -p "$root"
  (
    cd "$root"
    run_fanout --status 1
    [ "$status" -eq 2 ]
    [[ "$output" == *"--status: current directory is not inside a git work tree"* ]]
  )
}

@test "--status with malformed JSON state: exit 2" {
  # Contract: an unparseable state.json must surface as exit 2 with a
  # clear message. Without this, jq's raw exit code (5) or a silent
  # "total: 0" misreport would slip through and break wait-and-continue
  # automation that polls --status.
  force_missing tmux
  local root="$BATS_TEST_TMPDIR/project"
  mkdir -p "$root/.fanout"
  printf '{"panes": [bogus]\n' > "$root/.fanout/state.json"
  export FANOUT_STATE_PATH="$root/.fanout/state.json"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: fanout state at"* ]]
  [[ "$output" == *"is not valid JSON or has an invalid schema"* ]]
}

@test "--status with non-array state .panes: exit 2" {
  # JSON is valid but the state schema is wrong (.panes is a string).
  # Catch this up-front so status cannot turn it into a silent zero report.
  force_missing tmux
  local root="$BATS_TEST_TMPDIR/project"
  mkdir -p "$root/.fanout"
  printf '{"panes":"oops"}\n' > "$root/.fanout/state.json"
  export FANOUT_STATE_PATH="$root/.fanout/state.json"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: fanout state at"* ]]
  [[ "$output" == *"is not valid JSON or has an invalid schema"* ]]
}

@test "--status with valid state JSON missing .panes field: exit 0 with empty children" {
  # `.panes` may be absent on a freshly-initialized state file; that's valid.
  force_missing tmux
  local root="$BATS_TEST_TMPDIR/project"
  mkdir -p "$root/.fanout"
  printf '{}\n' > "$root/.fanout/state.json"
  export FANOUT_STATE_PATH="$root/.fanout/state.json"
  run_fanout --status 1
  [ "$status" -eq 0 ]
  [[ "$output" == *'"total": 0'* ]]
  [[ "$output" == *'"all_merged": false'* ]]
}

@test "--status with FANOUT_STATE_PATH whose project root is missing: exit 2 (not 3)" {
  # Stale / wrong state path root must be reported as an
  # enumeration problem (exit 2), not deferred until each gh_in_root cd
  # fails and reports exit 3 per child. The --status contract documents
  # 2 = "cannot enumerate / unusable config".
  force_missing tmux
  export FANOUT_STATE_PATH="/does/not/exist/anywhere/.fanout/state.json"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: project_root is not a directory"* ]]
}

@test "--status normalizes leading zeros in parent argument" {
  # Wrappers that pass IDs from external systems may forward "0300" instead
  # of "300". Without canonicalization, the parent field in the emitted JSON
  # would carry the leading zero and state filtering would miss entries
  # recorded under the canonical parent id.
  force_missing tmux
  local root="$BATS_TEST_TMPDIR/project"
  mkdir -p "$root/.fanout"
  printf '{"schemaVersion":1,"panes":[]}\n' > "$root/.fanout/state.json"
  export FANOUT_STATE_PATH="$root/.fanout/state.json"
  run_fanout --status 0300
  [ "$status" -eq 0 ]
  [[ "$output" == *'"parent": 300'* ]]
}
