#!/usr/bin/env bats
#
# Tier 1 — post-work-review shell driver contract.
#
# These tests exercise the bounded review gate without running an AI reviewer.
# They synthesize isolated reviewer/verifier JSON so prepare/record/summarize/
# mark stay covered by the normal test target.

load helpers

POST_WORK_REVIEW_DRIVER="$REPO_ROOT/codex/tools/post-work-review.sh"
export POST_WORK_REVIEW_JSON_HELPER="${POST_WORK_REVIEW_JSON_HELPER:-$FANOUT_BIN}"

@test "post-work-review shard-7: Codex skill uses native V1 and V2 custom roles" {
  local skill="$REPO_ROOT/codex/skills/post-work-review/SKILL.md"
  grep -Fq '"agent_type":"post-work-reviewer"' "$skill"
  grep -Fq 'agent_type: "post-work-verifier"' "$skill"
  grep -Fq '"fork_context":false' "$skill"
  grep -Fq '"fork_turns":"none"' "$skill"
  grep -Fq 'reserve-call <broad|verify> <controller-uuid>' "$skill"
  grep -Fq 'record-session <broad|verify> <child-uuid>' "$skill"
  ! grep -Fq 'authorize-spawn' "$skill"
}

@test "post-work-review shard-7: Claude legacy marker clears Codex metadata" {
  local skill="$REPO_ROOT/claude/skills/post-work-review/SKILL.md"
  local marker_step

  marker_step="$(sed -n '/^## Step 5/,/^## 完了報告/p' "$skill")"
  [[ "$marker_step" == *'marker="$(git rev-parse --git-dir)/post-work-review-passed"'* ]] || return 1
  [[ "$marker_step" == *'rm -f "${marker}.meta"'* ]] || return 1
  [[ "$marker_step" == *'git rev-parse HEAD > "$marker"'* ]] || return 1
  ! grep -Fq 'POST_WORK_REVIEW_BASE' "$skill" || return 1
  ! grep -Fq 'bounded-isolated-reviewer' "$skill" || return 1
}

@test "post-work-review shard-7: distributed skills stay repository-agnostic" {
  local claude_skill="$REPO_ROOT/claude/skills/post-work-review/SKILL.md"
  local codex_skill="$REPO_ROOT/codex/skills/post-work-review/SKILL.md"
  local unwanted

  for unwanted in \
    'git diff main..HEAD' \
    '`make check`' \
    '`make test`' \
    '`make lint`' \
    '`make lint-web`' \
    'docs/review-checklist.ja.md' \
    '.claude/hooks/pre-pr-review-gate.sh' \
    'fanout の並列ペイン' \
    '[[feedback_reviewer_role]]'; do
    ! grep -Fq "$unwanted" "$claude_skill" || return 1
  done
  for unwanted in '`make check`' 'make install-integrations'; do
    ! grep -Fq "$unwanted" "$codex_skill" || return 1
  done
  grep -Fq 'canonical full check' "$claude_skill"
  grep -Fq '包括的な単一コマンド' "$claude_skill"
  grep -Fq 'focused check' "$claude_skill"
}

@test "post-work-review shard-7: PR gate supports Codex metadata and Claude marker-only reviews" {
  command -v python3 >/dev/null 2>&1 || skip "python3 is required"

  local repo="$BATS_TEST_TMPDIR/pr-gate-review-modes"
  local gitdir head hook release_hash main_hash
  setup_review_repo "$repo"
  git -C "$repo" remote add origin git@github.com:butaosuinu/fanout.git
  make_branch_change "$repo"
  git -C "$repo" config init.defaultBranch main
  gitdir="$(gitdir_for "$repo")"
  head="$(git -C "$repo" rev-parse HEAD)"
  git -C "$repo" branch release/v1 "$head^"
  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$head^"
  git -C "$repo" update-ref refs/remotes/origin/main "$head^"
  release_hash="$(branch_diff_hash "$repo" release/v1 "$head")"
  main_hash="$(branch_diff_hash "$repo" main "$head")"
  hook="$REPO_ROOT/.claude/hooks/pre-pr-review-gate.py"
  printf '%s\n' "$head" >"$gitdir/post-work-review-passed"

  # Claude's legacy review writes only the exact-HEAD marker. It may open a PR
  # against the default base, but not a non-default base it did not record.
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  run run_pr_gate "$repo" "gh pr create --base main" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  git -C "$repo" config branch.feature.gh-merge-base release/v1
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1
  run run_pr_gate "$repo" "gh pr create --base main" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1
  git -C "$repo" config --unset branch.feature.gh-merge-base

  # A present but stale metadata file fails closed until the legacy writer
  # removes it before recording its marker.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$(git -C "$repo" rev-parse HEAD^)"
    printf 'scope=branch\n'
    printf 'base=release/v1\n'
    printf 'diff_hash=%s\n' "$release_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  rm "$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  # Codex metadata matching the target HEAD permits exactly its reviewed base.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'base=release/v1\n'
    printf 'diff_hash=%s\n' "$release_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$head"
  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1
  [[ "$output" == *"marker_reason=review_diff_changed"* ]] || return 1

  git -C "$repo" update-ref -d refs/remotes/origin/release/v1
  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1
  [[ "$output" == *"marker_reason=review_diff_changed"* ]] || return 1
  git -C "$repo" update-ref refs/remotes/origin/release/v1 "$head^"

  run run_pr_gate "$repo" "gh pr create --base main" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  git -C "$repo" config branch.feature.gh-merge-base release/v1
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1
  git -C "$repo" config --unset branch.feature.gh-merge-base

  # Metadata may also store the remote-qualified spelling from the driver.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'base=origin/release/v1\n'
    printf 'diff_hash=%s\n' "$release_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create --base release/v1" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  # Metadata for this HEAD fails closed when its reviewed base is missing.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'diff_hash=%s\n' "$release_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  # Metadata for this HEAD also fails closed without the reviewed diff hash.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'base=main\n'
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  # The exact-HEAD marker remains mandatory in both modes.
  {
    printf 'backend=bounded-isolated-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'base=main\n'
    printf 'diff_hash=%s\n' "$main_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [ -z "$output" ] || return 1

  printf '%s\n' "$(git -C "$repo" rev-parse HEAD^)" >"$gitdir/post-work-review-passed"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  # Present but malformed Codex metadata never falls back to legacy mode.
  printf '%s\n' "$head" >"$gitdir/post-work-review-passed"
  {
    printf 'backend=unexpected-reviewer\n'
    printf 'head=%s\n' "$head"
    printf 'scope=branch\n'
    printf 'base=main\n'
    printf 'diff_hash=%s\n' "$main_hash"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
  } >"$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  # A dangling metadata symlink is present-invalid, not legacy absence.
  rm "$gitdir/post-work-review-passed.meta"
  ln -s no-such-meta "$gitdir/post-work-review-passed.meta"
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1

  # Legacy mode also fails closed when the default base cannot be resolved.
  rm "$gitdir/post-work-review-passed.meta"
  git -C "$repo" config init.defaultBranch ""
  run run_pr_gate "$repo" "gh pr create" "$hook"
  [ "$status" -eq 0 ] || return 1
  [[ "$output" == *'"permissionDecision": "deny"'* ]] || return 1
}

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
  if [ "${1:-}" = "record" ]; then
    run record_result_command "$repo" "$2" "$3"
    return
  fi
  run bash -c 'cd "$1" || exit 1; shift; bash "$@" 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$@"
}

run_pr_gate() {
  local repo="$1"
  local command="$2"
  local hook="$3"
  local python
  python="$(command -v python3)"
  printf '{"tool_name":"Bash","tool_input":{"command":"%s"},"cwd":"%s"}\n' "$command" "$repo" | \
  PATH=/usr/bin:/bin:/usr/sbin:/sbin "$python" "$hook"
}

branch_diff_hash() {
  local repo="$1"
  local base="$2"
  local target="$3"
  local diff_file="$BATS_TEST_TMPDIR/pr-gate-current.diff"

  if git -C "$repo" diff --no-ext-diff --no-textconv --ignore-submodules=none \
    --no-color --binary "$base"..."$target" -- >"$diff_file" 2>/dev/null; then
    :
  elif git -C "$repo" diff --no-ext-diff --no-textconv --ignore-submodules=none \
    --no-color --binary "$base" "$target" -- >"$diff_file" 2>/dev/null; then
    :
  else
    return 1
  fi
  git -C "$repo" hash-object "$diff_file"
}

run_review_base() {
  local repo="$1"
  shift
  run bash -c 'cd "$1" || exit 1; shift; POST_WORK_REVIEW_BASE=main bash "$@" 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$@"
}

finding_one() {
  printf '{"severity":"major","file":"tracked.txt","line":1,"title":"Bug remains","description":"The feature still writes the bad value.","recommendation":"Write the fixed value."}'
}

session_uuid() {
  local value="$1" number
  [[ "$value" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] && { printf '%s\n' "$value"; return; }
  number="$(printf '%s' "$value" | cksum | awk '{print $1}')"
  printf '00000000-0000-4000-8000-%012d\n' "$number"
}

record_result_command() {
  local repo="$1" kind="$2" json_file="$3" helper="${4:-}"
  local parent child role bundle sessions rollout result
  parent="11111111-1111-4111-8111-111111111111"
  child="$(sed -n 's/.*"reviewer_session_id":"\([^"]*\)".*/\1/p' "$json_file")"; child="${child:-22222222-2222-4222-8222-222222222222}"
  [ "$kind" = "broad" ] && role="post-work-reviewer" || role="post-work-verifier"
  (cd "$repo" && bash "$POST_WORK_REVIEW_DRIVER" reserve-call "$kind" "$parent") >/dev/null || return
  bundle="$(env_value "$repo" "${kind/broad/review}_bundle")"
  sessions="$(state_dir_for "$repo")/test-sessions"
  rollout="$sessions/$child.jsonl"
  mkdir -p "$sessions"
  result="$(cat "$json_file"; printf x)"; result="${result%x}"
  result="${result//\\/\\\\}"
  result="${result//\"/\\\"}"; result="${result//$'\n'/\\n}"
  cat >"$rollout" <<EOF
{"type":"session_meta","payload":{"id":"$child","parent_thread_id":"$parent","timestamp":"2999-01-01T00:00:00Z","thread_source":"subagent","agent_role":"$role","multi_agent_version":"v1","source":{"subagent":{"thread_spawn":{"parent_thread_id":"$parent","agent_role":"$role"}}}}}
{"type":"event_msg","payload":{"type":"user_message","message":"$bundle"}}
{"type":"turn_context","payload":{"sandbox_policy":{"type":"read-only"},"approval_policy":"never"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"$result"}}
EOF
  if [ -n "$helper" ]; then
    (cd "$repo" && POST_WORK_REVIEW_JSON_HELPER="$helper" POST_WORK_REVIEW_SESSIONS_ROOT="$sessions" \
      bash "$POST_WORK_REVIEW_DRIVER" record-session "$kind" "$child")
  else
    (cd "$repo" && POST_WORK_REVIEW_SESSIONS_ROOT="$sessions" bash "$POST_WORK_REVIEW_DRIVER" record-session "$kind" "$child")
  fi
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
  session_id="$(session_uuid "$session_id")"
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
{"backend":"bounded-isolated-reviewer","review_type":"broad","reviewer_agent":"post-work-reviewer","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"$session_id","same_agent_review":$same_agent,"reviewer_isolated":true,"reviewer_sandbox_mode":"read-only","hooks_only_success":$hooks_only,"head":"$head","diff_hash":"$diff_hash","truncated":$truncated,"finding_count":$count,"findings":[$findings]}
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
  session_id="$(session_uuid "$session_id")"
  head="$(env_value "$repo" head)"
  diff_hash="$(env_value "$repo" diff_hash)"
  if [ -n "$findings" ]; then
    count=1
  else
    count=0
  fi
  cat >"$out_file" <<EOF
{"backend":"bounded-isolated-reviewer","review_type":"verify","reviewer_agent":"post-work-verifier","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"$session_id","same_agent_review":false,"reviewer_isolated":true,"reviewer_sandbox_mode":"read-only","hooks_only_success":false,"head":"$head","diff_hash":"$diff_hash","all_previous_findings_fixed":$all_fixed,"new_regressions":$new_regressions,"truncated":false,"finding_count":$count,"findings":[$findings]}
EOF
}

record_clean_broad() {
  local repo="$1"
  local session_id="${2:-session-broad-clean}"
  local json_file="$BATS_TEST_TMPDIR/broad-clean.json"
  write_broad_result_json "$repo" "$session_id" false false false "" "$json_file"
  record_result_command "$repo" broad "$json_file" \
    >"$BATS_TEST_TMPDIR/record-broad-clean.out"
}

prepare_branch_review() {
  local repo="$1"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  [[ "$output" == *"scope=branch"* ]]
  record_clean_broad "$repo" || return 1
}

@test "post-work-review shard-12: prepare writes one bundle, not per-file packets" {
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
  [[ "$output" == *"pending_verify=0"* ]]
  [[ "$output" == *"review_bundle=$state/review-bundle.md"* ]]
  [[ "$output" == *"findings_tsv=$state/findings.tsv"* ]]
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

@test "post-work-review shard-8: prepare paths are usable from caller subdirectories" {
  local repo="$BATS_TEST_TMPDIR/review-subdir"
  local state bundle_path
  setup_review_repo "$repo"
  mkdir -p "$repo/subdir"
  printf 'dirty\n' >"$repo/tracked.txt"

  run bash -c 'cd "$1/subdir" || exit 1; bash "$2" prepare 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER"

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  state="$(cd "$state" && pwd -P)"
  bundle_path="$(printf '%s\n' "$output" | awk -F= '$1 == "review_bundle" { print $2; exit }')"
  [ "$bundle_path" = "$state/review-bundle.md" ]
  [ -f "$bundle_path" ]
}

@test "post-work-review shard-12: rejects no-sandbox Codex overrides" {
  local repo="$BATS_TEST_TMPDIR/review-no-sandbox"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run bash -c 'cd "$1" || exit 1; CODEX_SANDBOX=none bash "$2" prepare 2>&1' bash "$repo" "$POST_WORK_REVIEW_DRIVER"

  [ "$status" -eq 1 ]
  [[ "$output" == *"isolated reviewer requires an enforceable read-only subagent sandbox"* ]]
}

@test "post-work-review shard-8: resolve_base prefers GitHub default before main fallback" {
  local repo="$BATS_TEST_TMPDIR/review-default-branch"
  local gh_bin
  setup_review_repo "$repo"
  git -C "$repo" checkout -qb develop
  printf 'develop-base\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "develop base"
  git -C "$repo" update-ref refs/remotes/origin/main main
  git -C "$repo" update-ref refs/remotes/origin/develop develop
  git -C "$repo" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main
  git -C "$repo" checkout -qb feature main
  printf 'feature\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "feature"
  gh_bin="$BATS_TEST_TMPDIR/gh-bin"
  mkdir -p "$gh_bin"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ "${1:-}" = repo ] && [ "${2:-}" = view ]; then\n'
    printf '  echo develop\n'
    printf '  exit 0\n'
    printf 'fi\n'
    printf 'exit 1\n'
  } >"$gh_bin/gh"
  chmod +x "$gh_bin/gh"
  export PATH="$gh_bin:$PATH"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  [[ "$output" == *"base=origin/develop"* ]]
}

@test "post-work-review shard-12: ignores external diff drivers for review bundles" {
  local repo="$BATS_TEST_TMPDIR/review-no-ext-diff"
  local state external_diff
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"
  external_diff="$BATS_TEST_TMPDIR/external-diff.sh"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'echo EXTERNAL-DIFF\n'
  } >"$external_diff"
  chmod +x "$external_diff"
  export GIT_EXTERNAL_DIFF="$external_diff"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fq "+dirty" "$state/current.diff"
  ! grep -Fq "EXTERNAL-DIFF" "$state/current.diff"
  ! grep -Fq "EXTERNAL-DIFF" "$state/review-bundle.md"
}

@test "post-work-review shard-8: disables color for review bundles" {
  local repo="$BATS_TEST_TMPDIR/review-no-color"
  local state
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"
  git -C "$repo" config color.ui always
  git -C "$repo" config color.diff always

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fq "+dirty" "$state/current.diff"
  ! LC_ALL=C grep -q "$(printf '\033')" "$state/current.diff"
  ! LC_ALL=C grep -q "$(printf '\033')" "$state/review-bundle.md"
}

@test "post-work-review shard-12: includes dangling symlink diffs" {
  local repo="$BATS_TEST_TMPDIR/review-dangling-symlink"
  local state
  setup_review_repo "$repo"
  ln -s missing-target "$repo/dangling-link"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fxq "dangling-link" "$state/changed-files.txt"
  grep -Fq "new file mode 120000" "$state/current.diff"
  grep -Fq "+missing-target" "$state/current.diff"
  grep -Fq "dangling-link" "$state/review-bundle.md"
}

@test "post-work-review shard-8: includes directory symlink diffs" {
  local repo="$BATS_TEST_TMPDIR/review-directory-symlink"
  local state
  setup_review_repo "$repo"
  mkdir -p "$repo/target-dir"
  ln -s target-dir "$repo/linkdir"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fxq "linkdir" "$state/changed-files.txt"
  grep -Fq "new file mode 120000" "$state/current.diff"
  grep -Fq "+target-dir" "$state/current.diff"
  grep -Fq "linkdir" "$state/review-bundle.md"
}

@test "post-work-review shard-12: includes quoted untracked path diffs" {
  local repo="$BATS_TEST_TMPDIR/review-quoted-untracked"
  local state weird
  setup_review_repo "$repo"
  weird="line"$'\n'"break.txt"
  printf 'strange\n' >"$repo/$weird"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fq 'line\nbreak.txt' "$state/changed-files.txt"
  grep -Fq "+strange" "$state/current.diff"
  grep -Fq "line" "$state/review-bundle.md"
}

@test "post-work-review shard-8: fences changed files in review bundle" {
  local repo="$BATS_TEST_TMPDIR/review-changed-files-fence"
  local state fence_name
  setup_review_repo "$repo"
  fence_name='```json'
  printf 'fence\n' >"$repo/$fence_name"

  run_review "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fq '````text' "$state/review-bundle.md"
  grep -Fq '```json' "$state/review-bundle.md"
}

@test "post-work-review shard-12: ignores textconv filters for review bundles" {
  local repo="$BATS_TEST_TMPDIR/review-no-textconv"
  local state textconv
  setup_review_repo "$repo"
  printf '*.foo diff=foo\n' >"$repo/.gitattributes"
  printf 'base\n' >"$repo/sample.foo"
  git -C "$repo" add .gitattributes sample.foo
  git -C "$repo" commit -qm "add textconv sample"
  git -C "$repo" checkout -qb feature
  printf 'changed\n' >"$repo/sample.foo"
  git -C "$repo" add sample.foo
  git -C "$repo" commit -qm "change textconv sample"
  textconv="$BATS_TEST_TMPDIR/textconv.sh"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'echo TEXTCONV\n'
  } >"$textconv"
  chmod +x "$textconv"
  git -C "$repo" config diff.foo.textconv "$textconv"

  run_review_base "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fq "+changed" "$state/current.diff"
  ! grep -Fq "TEXTCONV" "$state/current.diff"
  ! grep -Fq "TEXTCONV" "$state/review-bundle.md"
}

@test "post-work-review shard-8: includes submodule changes ignored by repo config" {
  local repo="$BATS_TEST_TMPDIR/review-submodule-ignore"
  local sub="$BATS_TEST_TMPDIR/review-submodule-source"
  local state next_sub_head
  setup_review_repo "$repo"
  mkdir -p "$sub"
  git -C "$sub" init -q
  git -C "$sub" config user.email "fanout-test@example.com"
  git -C "$sub" config user.name "fanout test"
  printf 'base\n' >"$sub/lib.txt"
  git -C "$sub" add lib.txt
  git -C "$sub" commit -qm "submodule base"
  git -C "$repo" -c protocol.file.allow=always submodule add "$sub" deps/sub >/dev/null
  git -C "$repo" config -f .gitmodules submodule.deps/sub.ignore all
  git -C "$repo" add .gitmodules deps/sub
  git -C "$repo" commit -qm "add ignored submodule"

  printf 'next\n' >"$sub/lib.txt"
  git -C "$sub" add lib.txt
  git -C "$sub" commit -qm "submodule next"
  next_sub_head="$(git -C "$sub" rev-parse HEAD)"
  git -C "$repo" checkout -qb feature
  git -C "$repo/deps/sub" -c protocol.file.allow=always fetch origin >/dev/null
  git -C "$repo/deps/sub" checkout -q "$next_sub_head"
  git -C "$repo" add -f deps/sub
  git -C "$repo" commit -qm "bump submodule"

  run_review_base "$repo" prepare

  [ "$status" -eq 0 ]
  state="$(state_dir_for "$repo")"
  grep -Fxq "deps/sub" "$state/changed-files.txt"
  grep -Fq "Subproject commit" "$state/current.diff"
  grep -Fq "deps/sub" "$state/review-bundle.md"
}

@test "post-work-review shard-11: records, summarizes, and marks a clean branch review" {
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
  grep -Fxq "head=$(git -C "$repo" rev-parse HEAD)" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "base=main" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "backend=bounded-isolated-reviewer" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "broad_review_calls=1" "$gitdir/post-work-review-passed.meta"
  grep -Fxq "clean=true" "$gitdir/post-work-review-passed.meta"
}

@test "post-work-review shard-7: parses each reviewer result once per driver command" {
  local repo="$BATS_TEST_TMPDIR/review-json-batch"
  local json_file="$BATS_TEST_TMPDIR/json-batch.json"
  local helper_wrapper="$BATS_TEST_TMPDIR/fanout-json-helper"
  local count_file="$BATS_TEST_TMPDIR/json-parser-count"
  local real_helper="$POST_WORK_REVIEW_JSON_HELPER"
  setup_review_repo "$repo"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-json-batch" false false false "" "$json_file"

  cat >"$helper_wrapper" <<'EOF'
#!/usr/bin/env bash
printf 'parse\n' >>"$JSON_PARSER_COUNT_FILE"
exec "$REAL_JSON_HELPER" "$@"
EOF
  chmod +x "$helper_wrapper"
  export REAL_JSON_HELPER="$real_helper"
  export JSON_PARSER_COUNT_FILE="$count_file"
  export POST_WORK_REVIEW_JSON_HELPER="$helper_wrapper"

  : >"$count_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$count_file")" -eq 2 ]
  cmp "$json_file" "$(state_dir_for "$repo")/results/broad-001.json"

  : >"$count_file"
  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$count_file")" -eq 1 ]

  : >"$count_file"
  run_review "$repo" mark
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$count_file")" -eq 1 ]
}

@test "post-work-review shard-7: keeps JSON cache outside a repo-local TMPDIR" {
  local repo="$BATS_TEST_TMPDIR/review-json-repo-tmpdir"
  local repo_tmp="$repo/repo-tmp"
  local json_file="$BATS_TEST_TMPDIR/json-repo-tmpdir.json"
  setup_review_repo "$repo"
  mkdir -p "$repo_tmp"
  # macOS may create this system cache in TMPDIR; keep the driver cache visible.
  printf 'xcrun_db\n' >"$repo_tmp/.gitignore"
  git -C "$repo" add repo-tmp/.gitignore
  git -C "$repo" commit -qm "track local temporary directory"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-json-repo-tmpdir" false false false "" "$json_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 0 ]

  run bash -c 'cd "$1" || exit 1; TMPDIR="$2" bash "$3" mark 2>&1' \
    bash "$repo" "$repo_tmp" "$POST_WORK_REVIEW_DRIVER"

  [ "$status" -eq 0 ]
  [[ "$output" == *"marker_written=true"* ]]
  [ -z "$(git -C "$repo" status --porcelain -uall)" ]
}

@test "post-work-review shard-10: uses FANOUT_BIN without Ruby or Python" {
  local repo="$BATS_TEST_TMPDIR/review-json-fanout-bin"
  local json_file="$BATS_TEST_TMPDIR/json-fanout-bin.json"
  local runtime_bin="$BATS_TEST_TMPDIR/forbidden-json-runtimes"
  local runtime_log="$BATS_TEST_TMPDIR/forbidden-json-runtimes.log"
  setup_review_repo "$repo"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-json-fanout-bin" false false false "" "$json_file"

  mkdir -p "$runtime_bin"
  for runtime in ruby python3; do
    cat >"$runtime_bin/$runtime" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$0" >>"$FORBIDDEN_RUNTIME_LOG"
exit 99
EOF
    chmod +x "$runtime_bin/$runtime"
  done
  export FORBIDDEN_RUNTIME_LOG="$runtime_log"
  export PATH="$runtime_bin:$PATH"
  export FANOUT_BIN
  unset POST_WORK_REVIEW_JSON_HELPER

  run_review "$repo" record broad "$json_file"

  [ "$status" -eq 0 ]
  [[ "$output" == *"recorded=true"* ]]
  [ ! -e "$runtime_log" ]
  cmp "$json_file" "$(state_dir_for "$repo")/results/broad-001.json"
}

@test "post-work-review shard-10: fails closed when the JSON helper is missing" {
  local repo="$BATS_TEST_TMPDIR/review-json-helper-missing"
  local json_file="$BATS_TEST_TMPDIR/json-helper-missing.json"
  local gitdir
  setup_review_repo "$repo"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-json-helper-missing" false false false "" "$json_file"
  gitdir="$(gitdir_for "$repo")"

  run record_result_command "$repo" broad "$json_file" "$BATS_TEST_TMPDIR/missing-helper"

  [ "$status" -eq 1 ]
  [[ "$output" == *"post-work-review JSON helper is not executable"* ]]
  [ ! -e "$(state_dir_for "$repo")/results/broad-001.json" ]
  [ ! -e "$gitdir/post-work-review-passed" ]
}

@test "post-work-review shard-10: fails closed when the JSON helper rejects a result" {
  local repo="$BATS_TEST_TMPDIR/review-json-helper-failure"
  local json_file="$BATS_TEST_TMPDIR/json-helper-failure.json"
  local helper="$BATS_TEST_TMPDIR/failing-json-helper"
  local gitdir
  setup_review_repo "$repo"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-json-helper-failure" false false false "" "$json_file"
  gitdir="$(gitdir_for "$repo")"
  cat >"$helper" <<'EOF'
#!/usr/bin/env bash
exit 42
EOF
  chmod +x "$helper"

  run record_result_command "$repo" broad "$json_file" "$helper"

  [ "$status" -eq 1 ]
  [[ "$output" == *"post-work-review JSON helper is incompatible"* ]]
  [ ! -e "$(state_dir_for "$repo")/results/broad-001.json" ]
  [ ! -e "$gitdir/post-work-review-passed" ]
}

@test "post-work-review shard-10: stored results preserve JSON helper diagnostics" {
  local repo="$BATS_TEST_TMPDIR/review-json-stored-helper-errors"
  local incompatible_helper="$BATS_TEST_TMPDIR/incompatible-stored-json-helper"
  setup_review_repo "$repo"
  make_branch_change "$repo"
  prepare_branch_review "$repo" || return 1

  run bash -c 'cd "$1" || exit 1; POST_WORK_REVIEW_JSON_HELPER="$3" bash "$2" summarize 2>&1' \
    bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$BATS_TEST_TMPDIR/missing-stored-helper"

  [ "$status" -eq 1 ]
  [[ "$output" == *"post-work-review JSON helper is not executable"* ]]
  [[ "$output" != *"File exists"* ]]
  [[ "$output" != *"failed to create reviewer JSON cache entry"* ]]

  cat >"$incompatible_helper" <<'EOF'
#!/usr/bin/env bash
exit 42
EOF
  chmod +x "$incompatible_helper"

  run bash -c 'cd "$1" || exit 1; POST_WORK_REVIEW_JSON_HELPER="$3" bash "$2" status 2>&1' \
    bash "$repo" "$POST_WORK_REVIEW_DRIVER" "$incompatible_helper"

  [ "$status" -eq 1 ]
  [[ "$output" == *"post-work-review JSON helper is incompatible or failed before projection"* ]]
  [[ "$output" != *"File exists"* ]]
  [[ "$output" != *"failed to create reviewer JSON cache entry"* ]]
}

@test "post-work-review shard-7: prepare clears stale marker files" {
  local repo="$BATS_TEST_TMPDIR/review-clear-marker"
  local gitdir
  setup_review_repo "$repo"
  make_branch_change "$repo"
  prepare_branch_review "$repo"
  run_review "$repo" mark
  [ "$status" -eq 0 ]
  gitdir="$(gitdir_for "$repo")"
  [ -f "$gitdir/post-work-review-passed" ]
  [ -f "$gitdir/post-work-review-passed.meta" ]

  run_review_base "$repo" prepare

  [ "$status" -eq 0 ]
  [ ! -e "$gitdir/post-work-review-passed" ]
  [ ! -e "$gitdir/post-work-review-passed.meta" ]
}

@test "post-work-review shard-8: record rejects same-agent and hooks-only results" {
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

  run_review "$repo" reset
  [ "$status" -eq 0 ]
  run_review "$repo" prepare
  [ "$status" -eq 0 ]
  write_broad_result_json "$repo" "session-hooks-only" false true false "" "$json_file"
  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"hooks-only success is rejected"* ]]
}

@test "post-work-review shard-8: record rejects incomplete findings" {
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

@test "post-work-review shard-11: record rejects invalid reviewer JSON" {
  local repo="$BATS_TEST_TMPDIR/review-invalid-json"
  local json_file="$BATS_TEST_TMPDIR/invalid-reviewer.json"
  setup_review_repo "$repo"
  printf 'dirty\n' >"$repo/tracked.txt"

  run_review "$repo" prepare
  [ "$status" -eq 0 ]
  printf '{"backend":' >"$json_file"

  run_review "$repo" record broad "$json_file"
  [ "$status" -eq 1 ]
  [[ "$output" == *"child rollout attestation failed"* ]]
}

@test "post-work-review shard-8: record rejects stale review targets" {
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

@test "post-work-review shard-4: summarize rejects target changes after record" {
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

@test "post-work-review shard-6: verifier requires prepared fix rounds and fresh sessions" {
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
  grep -Fq -- "- reviewer_agent: post-work-verifier" "$(state_dir_for "$repo")/verify-bundle.md"
  ! grep -Fq "verifier_agent" "$(state_dir_for "$repo")/verify-bundle.md"

  write_verify_result_json "$repo" "session-broad" true false "" "$verify_json"
  run_review "$repo" record verify "$verify_json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"reviewer_session_id already recorded"* ]]

  run_review_base "$repo" prepare-verify
  [ "$status" -eq 1 ]
  [[ "$output" == *"reserved reviewer call is incomplete"* ]]
}

@test "post-work-review shard-4: rejects failed verifier results without findings" {
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

@test "post-work-review shard-9: branch verifier bundle includes uncommitted fixes" {
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

@test "post-work-review shard-6: rejects prepare-verify after a clean broad result" {
  local repo="$BATS_TEST_TMPDIR/review-clean-prepare-verify"
  setup_review_repo "$repo"
  make_branch_change "$repo"
  prepare_branch_review "$repo"

  printf 'new-target\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "new target"

  run_review_base "$repo" prepare-verify

  [ "$status" -eq 1 ]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"findings=0"* ]]
  [[ "$output" == *"stop_reason=no_prior_findings_to_verify"* ]]
  [[ "$output" == *"marker_eligible=false"* ]]
}

@test "post-work-review shard-12: prepare does not reset an unresolved broad review" {
  local repo="$BATS_TEST_TMPDIR/review-prepare-budget-guard"
  local broad_json="$BATS_TEST_TMPDIR/broad-prepare-budget.json"
  local finding state
  setup_review_repo "$repo"
  make_branch_change "$repo"

  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  write_broad_result_json "$repo" "session-prepare-budget" false false false "$finding" "$broad_json"
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  run_review_base "$repo" prepare

  [ "$status" -eq 1 ]
  [[ "$output" == *"broad_review_calls=1"* ]]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"findings=1"* ]]
  [[ "$output" == *"stop_reason=review_budget_exhausted"* ]]
  state="$(state_dir_for "$repo")"
  [ -f "$state/results/broad-001.json" ]
}

@test "post-work-review shard-10: pending verify bundle prevents clean summarize and mark" {
  local repo="$BATS_TEST_TMPDIR/review-pending-verify"
  local broad_json="$BATS_TEST_TMPDIR/broad-pending-verify.json"
  local finding
  setup_review_repo "$repo"
  make_branch_change "$repo"
  run_review_base "$repo" prepare
  [ "$status" -eq 0 ]
  finding="$(finding_one)"
  write_broad_result_json "$repo" "session-pending-broad" false false false "$finding" "$broad_json"
  run_review "$repo" record broad "$broad_json"
  [ "$status" -eq 0 ]

  printf 'new-target\n' >"$repo/tracked.txt"
  git -C "$repo" add tracked.txt
  git -C "$repo" commit -qm "new target"
  run_review_base "$repo" prepare-verify
  [ "$status" -eq 0 ]
  [[ "$output" == *"pending_verify=1"* ]]

  run_review "$repo" summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"clean=false"* ]]
  [[ "$output" == *"pending_verify=1"* ]]
  [[ "$output" == *"marker_eligible=false"* ]]

  run_review "$repo" mark
  [ "$status" -eq 1 ]
  [[ "$output" == *"marker_reason=last_review_not_clean"* ]]
}

@test "post-work-review shard-3: verifier clean path and repeated finding detection" {
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

@test "post-work-review shard-5: duplicate broad findings do not count as repeated after a clean verifier" {
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

@test "post-work-review shard-1: summarize stops on truncated and exhausted verifier budget" {
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
  run_review_base "$trunc_repo" prepare-verify
  [ "$status" -eq 1 ]
  [[ "$output" == *"clean=false"* ]]
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

@test "post-work-review shard-2: mark rejects dirty worktree and stale review targets" {
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
