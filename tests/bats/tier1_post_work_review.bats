#!/usr/bin/env bats

load helpers

MARK_REVIEWED_HEAD="$REPO_ROOT/codex/skills/post-work-review/scripts/mark-reviewed-head.sh"

setup_review_repo() {
  local repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.email "fanout-test@example.com"
  git -C "$repo" config user.name "fanout test"
  git -C "$repo" config init.defaultBranch main
  printf 'base\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "initial"
  git -C "$repo" branch -M main
}

make_branch_change() {
  local repo="$1"
  git -C "$repo" checkout -qb feature
  printf 'feature\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "feature"
}

gitdir_for() {
  local repo="$1" gitdir
  gitdir="$(git -C "$repo" rev-parse --git-dir)"
  case "$gitdir" in
    /*) printf '%s\n' "$gitdir" ;;
    *) printf '%s/%s\n' "$repo" "$gitdir" ;;
  esac
}

run_marker() {
  local repo="$1"
  shift
  run bash -c 'cd "$1" && shift && "$@"' bash "$repo" "$MARK_REVIEWED_HEAD" "$@"
}

run_pr_gate() {
  local repo="$1" command="$2" hook="$3" python
  python="$(command -v python3)"
  printf '{"tool_name":"Bash","tool_input":{"command":"%s"},"cwd":"%s"}\n' "$command" "$repo" |
    PATH=/usr/bin:/bin:/usr/sbin:/sbin "$python" "$hook"
}

@test "Codex post-work-review uses fresh generic subagents" {
  local skill="$REPO_ROOT/codex/skills/post-work-review/SKILL.md"

  grep -Fq 'post_work_review_<head-prefix>_<unique>' "$skill"
  grep -Fq 'post_work_verify_<head-prefix>_<round>_<unique>' "$skill"
  grep -Fq '[a-z0-9_]+' "$skill"
  grep -Fq '"fork_turns": "none"' "$skill"
  grep -Fq 'natural-language' "$skill"
  grep -Fq 'inherits the parent session' "$skill"
  grep -Fq 'MCP/connectors' "$skill"
  grep -Fq 'nested agents' "$skill"
  grep -Fq 'fallback reviewer' "$skill"
  grep -Fq 'Do not edit files' "$skill"
  grep -Fq 'dirty uncommitted review' "$skill"
  grep -Fq 'staged, unstaged, untracked, and dirty' "$skill"
  grep -Fq 'run focused checks only' "$skill"
  grep -Fq 'must not write the review marker' "$skill"
  grep -Fq 'Normalize `refs/remotes/origin/`, `origin/`, and `refs/heads/` prefixes' "$skill"
  grep -Fq 'recorded repository root as the' "$skill"
  grep -Fq '"$helper" mark <reviewed-head>' "$skill"
  ! grep -Fq 'native-call' "$skill"
  ! grep -Fq 'model_catalog_json' "$skill"
  ! grep -Fq 'reviewer_session_id' "$skill"
  [ ! -e "$REPO_ROOT/codex/tools/post-work-review.sh" ]
  [ ! -e "$REPO_ROOT/codex/agents/post-work-reviewer.toml" ]
  [ ! -e "$REPO_ROOT/codex/agents/post-work-verifier.toml" ]
}

@test "binary-only install rejects the retired Codex review driver" {
  local home="$BATS_TEST_TMPDIR/no-skills-home" codex_dir="$BATS_TEST_TMPDIR/no-skills-home/.codex"
  mkdir -p "$codex_dir/tools"
  printf '#!/bin/sh\n' >"$codex_dir/tools/post-work-review.sh"

  run env HOME="$home" CODEX_DIR="$codex_dir" BIN_DIR="$home/bin" sh "$REPO_ROOT/install.sh" --no-skills
  [ "$status" -ne 0 ]
  [[ "$output" == *'retired Codex post-work-review driver'* ]]
  [[ "$output" == *'Rerun without --no-skills'* ]]
  [ ! -e "$home/bin/fanout" ]
}

@test "review marker binds the clean exact HEAD, base, and diff" {
  command -v python3 >/dev/null 2>&1 || skip "python3 is required"

  local repo="$BATS_TEST_TMPDIR/review-marker" gitdir head hook base_tip before_hash after_hash
  setup_review_repo "$repo"
  git -C "$repo" remote add origin git@github.com:butaosuinu/fanout.git
  make_branch_change "$repo"
  head="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" update-ref refs/remotes/origin/main "$head^"
  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$head^"
  gitdir="$(gitdir_for "$repo")"
  hook="$REPO_ROOT/.claude/hooks/pre-pr-review-gate.py"

  run_marker "$repo" clear
  [ "$status" -eq 0 ]
  [ ! -e "$gitdir/post-work-review-passed" ]

  run_marker "$repo" mark "$head" release/v1 "$(git -C "$repo" rev-parse refs/remotes/origin/release/v1)"
  [ "$status" -eq 0 ]
  [ "$(<"$gitdir/post-work-review-passed")" = "$head" ]
  grep -Fxq 'post_work_review_version=8' "$gitdir/post-work-review-passed.meta"
  grep -Fxq "head=$head" "$gitdir/post-work-review-passed.meta"
  grep -Fxq 'base=release/v1' "$gitdir/post-work-review-passed.meta"
  grep -Fxq "base_head=$(git -C "$repo" rev-parse HEAD^)" "$gitdir/post-work-review-passed.meta"
  grep -Eq '^diff_hash=[0-9a-f]+$' "$gitdir/post-work-review-passed.meta"

  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ]
  [ -z "$output" ]

  before_hash="$(git -C "$repo" diff --binary refs/remotes/origin/release/v1..."$head" -- | git -C "$repo" hash-object --stdin)"
  base_tip="$(git -C "$repo" commit-tree "$head^{tree}" -p "$head^" -m "base advance")"
  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$base_tip"
  after_hash="$(git -C "$repo" diff --binary refs/remotes/origin/release/v1..."$head" -- | git -C "$repo" hash-object --stdin)"
  [ "$before_hash" = "$after_hash" ]
  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"permissionDecision": "deny"'* ]]
  [[ "$output" == *'marker_reason=review_base_changed'* ]]
}

@test "review marker fails closed for stale or dirty targets" {
  local repo="$BATS_TEST_TMPDIR/review-marker-fail" head gitdir
  setup_review_repo "$repo"
  make_branch_change "$repo"
  head="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" update-ref refs/remotes/origin/main "$head^"
  gitdir="$(gitdir_for "$repo")"

  run_marker "$repo" mark "$head" main "$head"
  [ "$status" -ne 0 ]
  [[ "$output" == *'base changed during review'* ]]
  [ ! -e "$gitdir/post-work-review-passed" ]

  run_marker "$repo" mark "$(git -C "$repo" rev-parse HEAD^)" main "$(git -C "$repo" rev-parse refs/remotes/origin/main)"
  [ "$status" -ne 0 ]
  [[ "$output" == *'HEAD changed during review'* ]]
  [ ! -e "$gitdir/post-work-review-passed" ]

  printf 'dirty\n' >>"$repo/tracked.txt"
  run_marker "$repo" mark "$head" main "$(git -C "$repo" rev-parse refs/remotes/origin/main)"
  [ "$status" -ne 0 ]
  [[ "$output" == *'working tree is dirty'* ]]
  [ ! -e "$gitdir/post-work-review-passed" ]
}

@test "review marker rejects dirty submodules hidden by repository config" {
  local repo="$BATS_TEST_TMPDIR/review-marker-submodule" subrepo="$BATS_TEST_TMPDIR/review-submodule" head base_head gitdir
  setup_review_repo "$repo"
  setup_review_repo "$subrepo"
  git -C "$repo" remote add origin git@github.com:butaosuinu/fanout.git
  git -C "$repo" -c protocol.file.allow=always submodule add "$subrepo" vendor/sub >/dev/null
  git -C "$repo" config -f .gitmodules submodule.vendor/sub.ignore all
  git -C "$repo" add .gitmodules vendor/sub
  git -C "$repo" commit -qm "add ignored submodule"
  head="$(git -C "$repo" rev-parse HEAD)"
  base_head="$(git -C "$repo" rev-parse HEAD^)"
  git -C "$repo" update-ref refs/remotes/origin/main "$base_head"
  gitdir="$(gitdir_for "$repo")"
  printf 'dirty\n' >>"$repo/vendor/sub/tracked.txt"

  [ -z "$(git -C "$repo" status --porcelain -uall)" ]
  run_marker "$repo" mark "$head" main "$base_head"
  [ "$status" -ne 0 ]
  [[ "$output" == *'working tree is dirty'* ]]
  [ ! -e "$gitdir/post-work-review-passed" ]
}

@test "Claude marker-only reviews remain default-base-only" {
  command -v python3 >/dev/null 2>&1 || skip "python3 is required"

  local repo="$BATS_TEST_TMPDIR/legacy-review-marker" gitdir head hook
  setup_review_repo "$repo"
  git -C "$repo" remote add origin git@github.com:butaosuinu/fanout.git
  make_branch_change "$repo"
  head="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" update-ref refs/remotes/origin/main "$head^"
  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$head^"
  gitdir="$(gitdir_for "$repo")"
  hook="$REPO_ROOT/.claude/hooks/pre-pr-review-gate.py"
  printf '%s\n' "$head" >"$gitdir/post-work-review-passed"

  run run_pr_gate "$repo" "gh pr create --base main" "$hook"
  [ "$status" -eq 0 ]
  [ -z "$output" ]

  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"permissionDecision": "deny"'* ]]
}
