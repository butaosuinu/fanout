#!/usr/bin/env bats

# Tier 1 — compact pr-watch polling helper. GitHub is fixture-backed; these
# tests never read or mutate a live PR.

TESTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
REPO_ROOT="$(cd "$TESTS_DIR/.." && pwd)"
WATCHER="$REPO_ROOT/codex/skills/pr-watch/scripts/watch-pr.sh"

setup() {
  export PR_WATCH_FIXTURE="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$PR_WATCH_FIXTURE" "$BATS_TEST_TMPDIR/bin"
  unset PR_WATCH_CONTINUE PR_WATCH_INTERVAL PR_WATCH_MAX_SECONDS PR_WATCH_PLUS1_ACTOR_RE
  unset PR_WATCH_CHECKS_ERROR PR_WATCH_CHECKS_STATUS

  cat >"$BATS_TEST_TMPDIR/bin/gh" <<'EOF'
#!/bin/sh
case "${1:-} ${2:-}" in
  "pr view")
    cat "$PR_WATCH_FIXTURE/pr.tsv"
    if [ -f "$PR_WATCH_FIXTURE/pr.next.tsv" ]; then
      mv "$PR_WATCH_FIXTURE/pr.next.tsv" "$PR_WATCH_FIXTURE/pr.tsv"
    fi
    exit "${PR_WATCH_PR_STATUS:-0}"
    ;;
  "pr checks")
    cat "$PR_WATCH_FIXTURE/checks.tsv"
    [ -z "${PR_WATCH_CHECKS_ERROR:-}" ] || printf '%s\n' "$PR_WATCH_CHECKS_ERROR" >&2
    exit "${PR_WATCH_CHECKS_STATUS:-0}"
    ;;
  "api --paginate")
    cat "$PR_WATCH_FIXTURE/reactions.tsv"
    exit "${PR_WATCH_REACTION_STATUS:-0}"
    ;;
esac
printf 'unexpected gh call: %s\n' "$*" >&2
exit 2
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/gh"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"

  write_pr OPEN false MERGEABLE CLEAN NONE 0 head-one 2026-07-10T00:00:00Z
  : >"$PR_WATCH_FIXTURE/checks.tsv"
  : >"$PR_WATCH_FIXTURE/reactions.tsv"
}

write_pr() {
  local state="$1" draft="$2" mergeable="$3" merge_state="$4" review="$5"
  local requests="$6" head="$7" updated="$8"
  printf '27\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\thttps://github.com/acme/widget/pull/27\n' \
    "$state" "$draft" "$mergeable" "$merge_state" "$review" "$requests" \
    "$updated" "$head" >"$PR_WATCH_FIXTURE/pr.tsv"
}

setup_repo() {
  local repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.email fanout-test@example.com
  git -C "$repo" config user.name "fanout test"
  printf 'base\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm initial
}

run_watch() {
  local repo="$1"
  shift
  run bash -c 'cd "$1" || exit 1; shift; "$@"' bash "$repo" "$WATCHER" "$@"
}

state_dir_for() {
  git -C "$1" rev-parse --path-format=absolute --git-path pr-watch-state
}

@test "a GitHub no-checks response stays explicit instead of becoming green" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  export PR_WATCH_CHECKS_STATUS=1
  export PR_WATCH_CHECKS_ERROR="no checks reported on the branch"

  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"checks_reported=false"* ]]
  [[ "$output" == *"checks_pass=0"* ]]
}

@test "snapshot emits changes and suppresses an unchanged continued digest" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"

  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"checks_fail=0"* ]]
  [[ "$output" == *"head=head-one"* ]]

  export PR_WATCH_CONTINUE=1
  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [ -z "$output" ]

  printf 'fail\tunit\tFAILURE\tci\thttps://example.test/run/1\n' >"$PR_WATCH_FIXTURE/checks.tsv"
  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"checks_fail=1"* ]]
}

@test "an empty review decision does not shift snapshot fields" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  write_pr OPEN false MERGEABLE CLEAN "" 0 head-one 2026-07-10T00:00:00Z

  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == *"review=NONE"* ]]
  [[ "$output" == *"review_requests=0"* ]]
  [[ "$output" == *"head=head-one"* ]]
  [[ "$output" == *"updated=2026-07-10T00:00:00Z"* ]]
  [[ "$output" == *"url=https://github.com/acme/widget/pull/27"* ]]
}

@test "reaction targets count only actors matching the configured policy" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'other-user\t2026-07-10T00:01:00Z\nchatgpt-codex-connector[bot]\t2026-07-10T00:02:00Z\n' \
    >"$PR_WATCH_FIXTURE/reactions.tsv"

  export PR_WATCH_PLUS1_ACTOR_RE='^chatgpt-codex-connector\[bot\]$'
  run_watch "$repo" snapshot --repo acme/widget --pr 27 --reaction-target issue

  [ "$status" -eq 0 ]
  [[ "$output" == *"reactions=2"* ]]
  [[ "$output" == *"reaction_match=1"* ]]
  [[ "$output" == *"plus1=true"* ]]
}

@test "reaction targets do not treat an escaped class as a character class" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'chatgpt-codex-connectorb\t2026-07-10T00:01:00Z\n' \
    >"$PR_WATCH_FIXTURE/reactions.tsv"

  export PR_WATCH_PLUS1_ACTOR_RE='^chatgpt-codex-connector\[bot\]$'
  run_watch "$repo" snapshot --repo acme/widget --pr 27 --reaction-target issue

  [ "$status" -eq 0 ]
  [[ "$output" == *"reactions=1"* ]]
  [[ "$output" == *"reaction_match=0"* ]]
  [[ "$output" == *"plus1=false"* ]]
}

@test "reaction targets do not approve without an actor policy" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'chatgpt-codex-connector[bot]\t2026-07-10T00:01:00Z\n' \
    >"$PR_WATCH_FIXTURE/reactions.tsv"

  run_watch "$repo" snapshot --repo acme/widget --pr 27 --reaction-target issue

  [ "$status" -eq 0 ]
  [[ "$output" == *"reactions=1"* ]]
  [[ "$output" == *"reaction_match=0"* ]]
  [[ "$output" == *"plus1=false"* ]]
}

@test "wait does not report readiness before merge state is ready" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'pass\tunit\tSUCCESS\tci\thttps://example.test/run/1\n' \
    >"$PR_WATCH_FIXTURE/checks.tsv"
  write_pr OPEN false MERGEABLE CLEAN NONE 0 head-one 2026-07-10T00:01:00Z
  mv "$PR_WATCH_FIXTURE/pr.tsv" "$PR_WATCH_FIXTURE/pr.next.tsv"
  write_pr OPEN false MERGEABLE UNSTABLE NONE 0 head-one 2026-07-10T00:00:00Z
  export PR_WATCH_INTERVAL=0

  run_watch "$repo" wait --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"merge_state=CLEAN"* ]]
}

@test "wait times out and reset removes only the selected state" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local state_dir
  setup_repo "$repo"
  export PR_WATCH_MAX_SECONDS=0

  run_watch "$repo" wait --repo acme/widget --pr 27

  [ "$status" -eq 124 ]
  [[ "$output" == event=timeout* ]]
  [[ "$output" == *"state=OPEN"* ]]
  [[ "$output" == *"checks_reported=false"* ]]
  [[ "$output" == *"head=head-one"* ]]

  unset PR_WATCH_MAX_SECONDS
  run_watch "$repo" snapshot --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  state_dir="$(state_dir_for "$repo")"
  [ "$(find "$state_dir" -type f -name '*.state' | wc -l | tr -d ' ')" -eq 1 ]

  run_watch "$repo" reset --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(find "$state_dir" -type f -name '*.state' | wc -l | tr -d ' ')" -eq 0 ]
}

@test "state is isolated by GitHub repository as well as PR number" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local state_dir
  setup_repo "$repo"

  run_watch "$repo" snapshot --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  run_watch "$repo" snapshot --repo other/widget --pr 27
  [ "$status" -eq 0 ]

  state_dir="$(state_dir_for "$repo")"
  [ "$(find "$state_dir" -type f -name '*.state' | wc -l | tr -d ' ')" -eq 2 ]
}

@test "linked worktrees store state through git rev-parse --git-path" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local worktree="$BATS_TEST_TMPDIR/linked"
  local state_dir
  setup_repo "$repo"
  git -C "$repo" worktree add -qb linked "$worktree"
  [ -f "$worktree/.git" ]

  run_watch "$worktree" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  state_dir="$(state_dir_for "$worktree")"
  [ "$(find "$state_dir" -type f -name '*.state' | wc -l | tr -d ' ')" -eq 1 ]
}
