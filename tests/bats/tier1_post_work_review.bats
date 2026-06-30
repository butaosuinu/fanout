#!/usr/bin/env bats
#
# Tier 1 — post-work-review shell driver contract.
#
# These tests exercise the bounded review gate without running an AI reviewer.
# They synthesize isolated reviewer/verifier JSON so prepare/record/summarize/
# mark stay covered by the normal test target.

load helpers

POST_WORK_REVIEW_DRIVER="$REPO_ROOT/codex/tools/post-work-review.sh"

setup_review_repo() {
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

make_branch_change() {
  local repo="$1"
  git -C "$repo" checkout -qb feature
  printf 'feature\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "feature"
}

gitdir_for() {
  local repo="$1"
  local gitdir
  gitdir="$(cd "$repo" && git rev-parse --git-dir)"
  case "$gitdir" in
    /*) printf '%s\n' "$gitdir" ;;
    *) printf '%s/%s\n' "$repo" "$gitdir" ;;
  esac
}

state_dir_for() {
  printf '%s/post-work-review\n' "$(gitdir_for "$1")"
}

env_value() {
  local repo="$1"
  local key="$2"
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; found=1; exit } END { exit(found ? 0 : 1) }' \
    "$(state_dir_for "$repo")/review.env"
}

run_review() {
  local repo="$1"
  shift
  run bash -c 'cd "$1" || exit 1; shift; bash "$@" 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$@"
}

run_review_base() {
  local repo="$1"
  shift
  run bash -c 'cd "$1" || exit 1; shift; POST_WORK_REVIEW_BASE=main bash "$@" 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$@"
}

finding_one() {
  printf '{"severity":"major","file":"tracked.txt","line":1,"title":"Bug remains","description":"The feature still writes the bad value.","recommendation":"Write the fixed value."}'
}

write_broad_result_json() {
  local repo="$1"
  local session_id="$2"
  local same_agent="$3"
  local hooks_only="$4"
  local truncated="$5"
  local findings="$6"
  local out_file="$7"
  local head diff_hash count
  head="$(env_value "$repo" head)"
  diff_hash="$(env_value "$repo" diff_hash)"
  if [ "$#" -ge 8 ]; then
    count="$8"
  elif [ -n "$findings" ]; then
    count=1
  else
    count=0
  fi
  cat >"$out_file" <<EOF
{"backend":"bounded-isolated-reviewer","review_type":"broad","reviewer_agent":"post-work-reviewer","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"$session_id","same_agent_review":$same_agent,"reviewer_isolated":true,"hooks_only_success":$hooks_only,"head":"$head","diff_hash":"$diff_hash","truncated":$truncated,"finding_count":$count,"findings":[$findings]}
EOF
}

write_verify_result_json() {
  local repo="$1"
  local session_id="$2"
  local all_fixed="$3"
  local new_regressions="$4"
  local findings="$5"
  local out_file="$6"
  local head diff_hash count
  head="$(env_value "$repo" head)"
  diff_hash="$(env_value "$repo" diff_hash)"
  if [ -n "$findings" ]; then
    count=1
  else
    count=0
  fi
  cat >"$out_file" <<EOF
{"backend":"bounded-isolated-reviewer","review_type":"verify","reviewer_agent":"post-work-verifier","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"$session_id","same_agent_review":false,"reviewer_isolated":true,"hooks_only_success":false,"head":"$head","diff_hash":"$diff_hash","all_previous_findings_fixed":$all_fixed,"new_regressions":$new_regressions,"truncated":false,"finding_count":$count,"findings":[$findings]}
EOF
}

record_clean_broad() {
  local repo="$1"
  local session_id="${2:-session-broad-clean}"
  local json_file="$BATS_TEST_TMPDIR/broad-clean.json"
  write_broad_result_json "$repo" "$session_id" false false false "" "$json_file"
  (cd "$repo" && bash "$POST_WORK_REVIEW_DRIVER" record broad "$json_file") \
    >"$BATS_TEST_TMPDIR/record-broad-clean.out"
}

prepare_branch_review() {
  local repo="$1"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  [[ "$output" == *"scope=branch"* ]]
  record_clean_broad "$repo" || return 1
}

@test "post-work-review prepare writes one bundle, not per-file packets" {
  local repo="$BATS_TEST_TMPDIR/review-uncommitted"
  local state
  setup_review_repo "$repo"
  printf 'dirty\n```\n' >"$repo/tracked.txt"
  printf 'new\n' >"$repo/notes.md"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  [[ "$output" == *"scope=uncommitted"* ]]
  [[ "$output" == *"changed_files=2"* ]]
  [[ "$output" == *"review_bundle="* ]]
  [[ "$output" == *"broad_review_calls=0"* ]]
  [[ "$output" == *"verify_review_calls=0"* ]]
  [[ "$output" == *"max_total_reviewer_calls=3"* ]]
  state="$(state_dir_for "$repo")"
  [ -f "$state/review.env" ]
  [ -f "$state/review-bundle.md" ]
  [ -d "$state/results" ]
  [ -f "$state/findings.tsv" ]
  [ ! -e "$state/packet-list.txt" ]
  [ ! -e "$state/review-index.md" ]
  [ ! -d "$state/packets" ]
  grep -Fxq "tracked.txt" "$state/changed-files.txt"
  grep -Fxq "notes.md" "$state/changed-files.txt"
  grep -Fq "+dirty" "$state/review-bundle.md"
  grep -Fq '````diff' "$state/review-bundle.md"
  grep -Fq "+new" "$state/review-bundle.md"
}

@test "post-work-review records, summarizes, and marks a clean branch review" {
  local repo="$BATS_TEST_TMPDIR/review-branch"
  local gitdir
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  [[ "$output" == *"scope=branch"* ]]
  [[ "$output" == *"changed_files=1"* ]]

  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=unknown"* ]]
  [[ "$output" == *"broad_review_calls=0"* ]]

  record_clean_broad "$repo" || return 1

  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"broad_review_calls=1"* ]]
  [[ "$output" == *"clean=true"* ]]
  [[ "$output" == *"findings=0"* ]]
  [[ "$output" == *"marker_eligible=true"* ]]

  run_review "$repo" mark
  [ "$status" -eq 0 ]
  [[ "$output" == *"marker_written=true"* ]]
  gitdir="$(gitdir_for "$repo")"
  [ -f "$gitdir/post-work-review-passed" ]
  grep -Fxq "backend=bounded-isolated-reviewer" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "broad_review_calls=1" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "clean=true" "$gitdir/post-work-review-passed.meta"
}

@test "post-work-review record rejects same-agent and hooks-only results" {
  local repo="$BATS_TEST_TMPDIR/review-reject"
  local json_file="$BATS_TEST_TMPDIR/reject.json"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run_review "$repo" prepare
  [ "$status" -eq 0 ]

  write_broad_result_json "$repo" "session-same-agent" true false false "" "$json_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"same-agent review is rejected"* ]]

  write_broad_result_json "$repo" "session-hooks-only" false true false "" "$json_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"hooks-only success is rejected"* ]]
}

@test "post-work-review record rejects incomplete findings" {
  local repo="$BATS_TEST_TMPDIR/review-incomplete-finding"
  local json_file="$BATS_TEST_TMPDIR/incomplete-finding.json"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run_review "$repo" prepare
  [ "$status" -eq 0 ]

  write_broad_result_json "$repo" "session-incomplete-finding" false false false "{}" "$json_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"finding missing required fields"* ]]
}

@test "post-work-review record rejects stale review targets" {
  local repo="$BATS_TEST_TMPDIR/review-stale-record"
  local json_file="$BATS_TEST_TMPDIR/stale.json"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run_review "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-stale" false false false "" "$json_file"

  printf 'changed-after-prepare\n' >"$repo/tracked.txt"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"review target changed since prepare: diff_hash"* ]]
}

@test "post-work-review summarize rejects target changes after record" {
  local repo="$BATS_TEST_TMPDIR/review-stale-summary"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run_review "$repo" prepare
  [ "$status" -eq 0 ]
  record_clean_broad "$repo" "session-summary-target" || return 1

  printf 'changed-after-record\n' >"$repo/tracked.txt"
  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"findings=0"* ]]
  [[ "$output" == *"stop_reason=review_target_changed"* ]]
  [[ "$output" == *"marker_eligible=false"* ]]

  run_review "$repo" status
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"stop_reason=review_target_changed"* ]]
}

@test "post-work-review verifier requires prepared fix rounds and fresh sessions" {
  local repo="$BATS_TEST_TMPDIR/review-verify-guard"
  local broad_json="$BATS_TEST_TMPDIR/broad-finding.json"
  local verify_json="$BATS_TEST_TMPDIR/verify.json"
  local finding
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  write_broad_result_json "$repo" "session-broad" false false false "$finding" "$broad_json"
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  write_verify_result_json "$repo" "session-verify-early" true false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"verify bundle not prepared"* ]]

  printf 'fixed\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "fix"
  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]
  [[ "$output" == *"verify_bundle="* ]]
  [[ "$output" == *"fix_rounds=1"* ]]

  write_verify_result_json "$repo" "session-broad" true false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"reviewer_session_id already recorded"* ]]

  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]
  [[ "$output" == *"fix_rounds=2"* ]]
  grep -Fq "+fixed" "$(state_dir_for "$repo")/verify-bundle.md"
}

@test "post-work-review rejects failed verifier results without findings" {
  local repo="$BATS_TEST_TMPDIR/review-empty-failed-verifier"
  local broad_json="$BATS_TEST_TMPDIR/broad-empty-failed-verifier.json"
  local verify_json="$BATS_TEST_TMPDIR/verify-empty-failed-verifier.json"
  local finding
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  write_broad_result_json "$repo" "session-empty-failed-broad" false false false "$finding" "$broad_json"
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  printf 'fixed\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "fix"
  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]

  write_verify_result_json "$repo" "session-empty-failed-verify" false false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"failed verifier result requires findings"* ]]
}

@test "post-work-review branch verifier bundle includes uncommitted fixes" {
  local repo="$BATS_TEST_TMPDIR/review-branch-dirty-verify"
  local broad_json="$BATS_TEST_TMPDIR/broad-dirty-verify.json"
  local verify_json="$BATS_TEST_TMPDIR/verify-dirty-verify.json"
  local finding state
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  write_broad_result_json "$repo" "session-dirty-broad" false false false "$finding" "$broad_json"
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  printf 'fixed-without-commit\n```\n' >"$repo/tracked.txt"
  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]
  [[ "$output" == *"scope=branch"* ]]
  [[ "$output" == *"fix_rounds=1"* ]]
  state="$(state_dir_for "$repo")"
  grep -Fq "+fixed-without-commit" "$state/verify-bundle.md"
  grep -Fq '````diff' "$state/verify-bundle.md"

  write_verify_result_json "$repo" "session-dirty-verify" true false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 0 ]
  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=true"* ]]
  [[ "$output" == *"marker_eligible=false"* ]]
}

@test "post-work-review verifier clean path and repeated finding detection" {
  local clean_repo="$BATS_TEST_TMPDIR/review-verify-clean"
  local repeat_repo="$BATS_TEST_TMPDIR/review-repeat"
  local broad_json="$BATS_TEST_TMPDIR/broad.json"
  local verify_json="$BATS_TEST_TMPDIR/verify.json"
  local finding
  finding="$(finding_one)"

  setup_review_repo "$clean_repo"
  make_branch_change "$clean_repo"
  run_review_base "$clean_repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$clean_repo" "session-clean-broad" false false false "$finding" "$broad_json"
  run_review "$clean_repo" record broad "$broad_json"
  [ "$status" -eq 0 ]
  printf 'fixed\n' >"$clean_repo/tracked.txt"
  git -C "$clean_repo" add tracked.txt
  git -C "$clean_repo" commit -qm "fix"
  run_review_base "$clean_repo" prepare-verify
  [ "$status" -eq 0 ]
  write_verify_result_json "$clean_repo" "session-clean-verify" true false "" "$verify_json"
  run_review "$clean_repo" record verify "$verify_json"
  [ "$status" -eq 0 ]
  run_review "$clean_repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"verify_review_calls=1"* ]]
  [[ "$output" == *"clean=true"* ]]
  [[ "$output" == *"stop_reason="* ]]

  setup_review_repo "$repeat_repo"
  make_branch_change "$repeat_repo"
  run_review_base "$repeat_repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repeat_repo" "session-repeat-broad" false false false "$finding" "$broad_json"
  run_review "$repeat_repo" record broad "$broad_json"
  [ "$status" -eq 0 ]
  printf 'still-bad\n' >"$repeat_repo/tracked.txt"
  git -C "$repeat_repo" add tracked.txt
  git -C "$repeat_repo" commit -qm "still bad"
  run_review_base "$repeat_repo" prepare-verify
  [ "$status" -eq 0 ]
  write_verify_result_json "$repeat_repo" "session-repeat-verify" false false "$finding" "$verify_json"
  run_review "$repeat_repo" record verify "$verify_json"
  [ "$status" -eq 0 ]
  run_review "$repeat_repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"stop_reason=same_finding_repeated"* ]]
}

@test "post-work-review duplicate broad findings do not count as repeated after a clean verifier" {
  local repo="$BATS_TEST_TMPDIR/review-duplicate-broad"
  local broad_json="$BATS_TEST_TMPDIR/broad-duplicate.json"
  local verify_json="$BATS_TEST_TMPDIR/verify-duplicate-clean.json"
  local finding duplicate_findings
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  duplicate_findings="$finding,$finding"
  write_broad_result_json "$repo" "session-duplicate-broad" false false false "$duplicate_findings" "$broad_json" 2
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  printf 'fixed\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "fix"
  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]
  write_verify_result_json "$repo" "session-duplicate-verify" true false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 0 ]

  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=true"* ]]
  [[ "$output" == *"stop_reason="* ]]
  [[ "$output" != *"stop_reason=same_finding_repeated"* ]]
}

@test "post-work-review summarize stops on truncated and exhausted verifier budget" {
  local trunc_repo="$BATS_TEST_TMPDIR/review-truncated"
  local budget_repo="$BATS_TEST_TMPDIR/review-budget"
  local broad_json="$BATS_TEST_TMPDIR/broad.json"
  local verify_json="$BATS_TEST_TMPDIR/verify.json"
  local finding other_finding third_finding
  finding="$(finding_one)"
  other_finding='{"severity":"major","file":"tracked.txt","line":2,"title":"New issue","description":"A second unresolved issue.","recommendation":"Fix the second issue."}'
  third_finding='{"severity":"major","file":"tracked.txt","line":3,"title":"Third issue","description":"A third unresolved issue.","recommendation":"Fix the third issue."}'

  setup_review_repo "$trunc_repo"
  make_branch_change "$trunc_repo"
  run_review_base "$trunc_repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$trunc_repo" "session-truncated" false false true "" "$broad_json"
  run_review "$trunc_repo" record broad "$broad_json"
  [ "$status" -eq 0 ]
  run_review "$trunc_repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=unknown"* ]]
  [[ "$output" == *"stop_reason=review_truncated"* ]]

  setup_review_repo "$budget_repo"
  make_branch_change "$budget_repo"
  run_review_base "$budget_repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$budget_repo" "session-budget-broad" false false false "$finding" "$broad_json"
  run_review "$budget_repo" record broad "$broad_json"
  [ "$status" -eq 0 ]
  printf 'round1\n' >"$budget_repo/tracked.txt"
  git -C "$budget_repo" add tracked.txt
  git -C "$budget_repo" commit -qm "round1"
  run_review_base "$budget_repo" prepare-verify
  [ "$status" -eq 0 ]
  write_verify_result_json "$budget_repo" "session-budget-verify-1" false false "$other_finding" "$verify_json"
  run_review "$budget_repo" record verify "$verify_json"
  [ "$status" -eq 0 ]
  printf 'round2\n' >"$budget_repo/tracked.txt"
  git -C "$budget_repo" add tracked.txt
  git -C "$budget_repo" commit -qm "round2"
  run_review_base "$budget_repo" prepare-verify
  [ "$status" -eq 0 ]
  write_verify_result_json "$budget_repo" "session-budget-verify-2" false false "$third_finding" "$verify_json"
  run_review "$budget_repo" record verify "$verify_json"
  [ "$status" -eq 0 ]
  run_review "$budget_repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"total_reviewer_calls=3"* ]]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"stop_reason=review_budget_exhausted"* ]]
}

@test "post-work-review mark rejects dirty worktree and stale review targets" {
  local dirty_repo="$BATS_TEST_TMPDIR/review-dirty"
  setup_review_repo "$dirty_repo"
  make_branch_change "$dirty_repo"
  prepare_branch_review "$dirty_repo"
  printf 'untracked\n' >"$dirty_repo/after-review.txt"
  run_review "$dirty_repo" mark
  [ "$status" -eq 1 ]
  [[ "$output" == *"marker_reason=working_tree_dirty"* ]]

  local head_repo="$BATS_TEST_TMPDIR/review-head"
  setup_review_repo "$head_repo"
  make_branch_change "$head_repo"
  prepare_branch_review "$head_repo"
  printf 'next\n' >"$head_repo/next.txt"
  git -C "$head_repo" add next.txt
  git -C "$head_repo" commit -qm "next"
  run_review "$head_repo" mark
  [ "$status" -eq 1 ]
  [[ "$output" == *"marker_reason=head_changed_since_review"* ]]

  local diff_repo="$BATS_TEST_TMPDIR/review-diff"
  setup_review_repo "$diff_repo"
  make_branch_change "$diff_repo"
  prepare_branch_review "$diff_repo"
  git -C "$diff_repo" branch -f main HEAD
  run_review "$diff_repo" mark
  [ "$status" -eq 1 ]
  [[ "$output" == *"marker_reason=diff_changed_since_review"* ]]
}
