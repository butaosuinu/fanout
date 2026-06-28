#!/usr/bin/env bash
set -u

VERSION="1"
MAX_DIGEST_LINES="${POST_WORK_REVIEW_MAX_DIGEST_LINES:-160}"
MAX_TAIL_LINES="${POST_WORK_REVIEW_MAX_TAIL_LINES:-80}"

die() {
  echo "error=$*" >&2
  exit 1
}

ensure_repo() {
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "git repository not found"
}

repo_root() {
  git rev-parse --show-toplevel
}

git_dir_abs() {
  local root dir
  root="$(repo_root)"
  dir="$(git rev-parse --git-dir)"
  case "$dir" in
    /*) printf '%s\n' "$dir" ;;
    *) printf '%s\n' "$root/$dir" ;;
  esac
}

state_dir_abs() {
  local root base key
  root="$(repo_root)"
  if [ -n "${POST_WORK_REVIEW_STATE_DIR:-}" ]; then
    printf '%s\n' "$POST_WORK_REVIEW_STATE_DIR"
    return 0
  fi
  base="${TMPDIR:-/tmp}"
  key="$(printf '%s\n' "$root" | git hash-object --stdin 2>/dev/null || true)"
  if [ -z "$key" ]; then
    key="$(printf '%s\n' "$root" | sed 's/[^[:alnum:]_.-]/_/g')"
  fi
  printf '%s\n' "$base/post-work-review/$key"
}

display_path() {
  local root path
  root="$(repo_root)"
  path="$1"
  case "$path" in
    "$root"/*) printf '%s\n' "${path#$root/}" ;;
    *) printf '%s\n' "$path" ;;
  esac
}

has_dirty_tree() {
  [ -n "$(git status --porcelain -uall)" ]
}

now_utc() {
  date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date
}

run_id() {
  local ts
  ts="$(date -u '+%Y%m%dT%H%M%SZ' 2>/dev/null || date '+%Y%m%dT%H%M%S' 2>/dev/null || echo run)"
  printf '%s-%s\n' "$ts" "$$"
}

is_number() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
    *) return 0 ;;
  esac
}

bounded_number() {
  local value default
  value="$1"
  default="$2"
  if is_number "$value" && [ "$value" -gt 0 ]; then
    printf '%s\n' "$value"
  else
    printf '%s\n' "$default"
  fi
}

resolve_base() {
  local remote_head candidate
  if [ -n "${POST_WORK_REVIEW_BASE:-}" ]; then
    printf '%s\n' "$POST_WORK_REVIEW_BASE"
    return 0
  fi

  remote_head="$(git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  if [ -n "$remote_head" ]; then
    printf '%s\n' "$remote_head"
    return 0
  fi

  for candidate in origin/main origin/master main master; do
    if git rev-parse --verify "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

detect_scope() {
  local base
  case "${POST_WORK_REVIEW_SCOPE:-auto}" in
    auto)
      if has_dirty_tree; then
        printf '%s\n' "uncommitted|HEAD"
      else
        base="$(resolve_base)" || die "default branch not found; set POST_WORK_REVIEW_BASE"
        printf '%s|%s\n' "branch" "$base"
      fi
      ;;
    uncommitted)
      printf '%s\n' "uncommitted|HEAD"
      ;;
    branch)
      base="$(resolve_base)" || die "default branch not found; set POST_WORK_REVIEW_BASE"
      printf '%s|%s\n' "branch" "$base"
      ;;
    commit)
      printf '%s\n' "commit|HEAD"
      ;;
    *)
      die "invalid POST_WORK_REVIEW_SCOPE=${POST_WORK_REVIEW_SCOPE}"
      ;;
  esac
}

write_uncommitted_target() {
  local diff_file files_file
  diff_file="$1"
  files_file="$2"

  {
    git diff --binary HEAD -- 2>/dev/null || git diff --binary -- 2>/dev/null || true
    printf '\n# git status --porcelain -uall\n'
    git status --porcelain -uall
  } >"$diff_file"

  git status --porcelain -uall | sed 's/^...//' | sort -u >"$files_file"
}

write_branch_target() {
  local base diff_file files_file
  base="$1"
  diff_file="$2"
  files_file="$3"

  git diff --binary "$base"...HEAD -- >"$diff_file" 2>/dev/null || \
    git diff --binary "$base" HEAD -- >"$diff_file" 2>/dev/null || \
    : >"$diff_file"
  git diff --name-only "$base"...HEAD -- >"$files_file" 2>/dev/null || \
    git diff --name-only "$base" HEAD -- >"$files_file" 2>/dev/null || \
    : >"$files_file"
}

write_commit_target() {
  local diff_file files_file
  diff_file="$1"
  files_file="$2"

  git show --format= --binary HEAD >"$diff_file" 2>/dev/null || : >"$diff_file"
  git diff-tree --no-commit-id --name-only -r HEAD >"$files_file" 2>/dev/null || : >"$files_file"
}

write_target() {
  local scope base diff_file files_file
  scope="$1"
  base="$2"
  diff_file="$3"
  files_file="$4"

  case "$scope" in
    uncommitted) write_uncommitted_target "$diff_file" "$files_file" ;;
    branch) write_branch_target "$base" "$diff_file" "$files_file" ;;
    commit) write_commit_target "$diff_file" "$files_file" ;;
    *) die "invalid scope=$scope" ;;
  esac
}

review_command_label() {
  local scope base
  scope="$1"
  base="$2"
  case "$scope" in
    uncommitted) printf '%s\n' "codex review --uncommitted" ;;
    branch) printf '%s\n' "codex review --base $base" ;;
    commit) printf '%s\n' "codex review --commit HEAD" ;;
    *) die "invalid scope=$scope" ;;
  esac
}

script_path() {
  local dir base
  case "$0" in
    /*)
      printf '%s\n' "$0"
      ;;
    */*)
      dir="${0%/*}"
      base="${0##*/}"
      (cd "$dir" 2>/dev/null && printf '%s/%s\n' "$(pwd -P)" "$base") || printf '%s\n' "$0"
      ;;
    *)
      command -v "$0" 2>/dev/null || printf '%s\n' "$0"
      ;;
  esac
}

shell_quote() {
  printf "'"
  printf '%s' "$1" | sed "s/'/'\\\\''/g"
  printf "'"
}

print_handoff_command() {
  local root driver subcommand
  root="$1"
  driver="$2"
  subcommand="$3"
  printf 'cd '
  shell_quote "$root"
  printf ' && bash '
  shell_quote "$driver"
  printf ' %s\n' "$subcommand"
}

source_codex_home() {
  printf '%s\n' "${CODEX_HOME:-$HOME/.codex}"
}

prepare_review_codex_home() {
  local target source file
  target="$1"
  source="$(source_codex_home)"

  mkdir -p "$target" "$target/log" "$target/sqlite" || return 1
  chmod 700 "$target" "$target/log" "$target/sqlite" 2>/dev/null || true

  for file in auth.json config.toml; do
    if [ -f "$source/$file" ]; then
      cp "$source/$file" "$target/$file" || return 1
      chmod 600 "$target/$file" 2>/dev/null || true
    fi
  done
}

cleanup_review_codex_home() {
  local target
  target="$1"
  case "$target" in
    */post-work-review-codex-home.*) rm -rf "$target" ;;
  esac
}

detect_scope_soft() {
  local base
  case "${POST_WORK_REVIEW_SCOPE:-auto}" in
    auto)
      if has_dirty_tree; then
        printf '%s\n' "uncommitted|HEAD|"
      elif base="$(resolve_base)"; then
        printf '%s|%s|\n' "branch" "$base"
      else
        printf '%s\n' "unknown||default branch not found; set POST_WORK_REVIEW_BASE"
      fi
      ;;
    uncommitted)
      printf '%s\n' "uncommitted|HEAD|"
      ;;
    branch)
      if base="$(resolve_base)"; then
        printf '%s|%s|\n' "branch" "$base"
      else
        printf '%s\n' "unknown||default branch not found; set POST_WORK_REVIEW_BASE"
      fi
      ;;
    commit)
      printf '%s\n' "commit|HEAD|"
      ;;
    *)
      printf 'unknown||invalid POST_WORK_REVIEW_SCOPE=%s\n' "${POST_WORK_REVIEW_SCOPE}"
      ;;
  esac
}

run_codex_review() {
  local scope base stdout_file stderr_file runtime_home cleanup status
  scope="$1"
  base="$2"
  stdout_file="$3"
  stderr_file="$4"
  runtime_home="${POST_WORK_REVIEW_CODEX_HOME:-}"
  cleanup="false"

  if [ -z "$runtime_home" ]; then
    runtime_home="$(mktemp -d "${TMPDIR:-/tmp}/post-work-review-codex-home.XXXXXX")" || return 1
    cleanup="true"
    prepare_review_codex_home "$runtime_home" || {
      cleanup_review_codex_home "$runtime_home"
      return 1
    }
  fi

  case "$scope" in
    uncommitted)
      CODEX_HOME="$runtime_home" codex review \
        -c "sqlite_home=\"$runtime_home/sqlite\"" \
        -c "log_dir=\"$runtime_home/log\"" \
        -c 'analytics.enabled=false' \
        --disable apps \
        --uncommitted >"$stdout_file" 2>"$stderr_file"
      status="$?"
      ;;
    branch)
      CODEX_HOME="$runtime_home" codex review \
        -c "sqlite_home=\"$runtime_home/sqlite\"" \
        -c "log_dir=\"$runtime_home/log\"" \
        -c 'analytics.enabled=false' \
        --disable apps \
        --base "$base" >"$stdout_file" 2>"$stderr_file"
      status="$?"
      ;;
    commit)
      CODEX_HOME="$runtime_home" codex review \
        -c "sqlite_home=\"$runtime_home/sqlite\"" \
        -c "log_dir=\"$runtime_home/log\"" \
        -c 'analytics.enabled=false' \
        --disable apps \
        --commit HEAD >"$stdout_file" 2>"$stderr_file"
      status="$?"
      ;;
    *)
      die "invalid scope=$scope"
      ;;
  esac

  if [ "$cleanup" = "true" ]; then
    cleanup_review_codex_home "$runtime_home"
  fi
  return "$status"
}

grep_tail() {
  local pattern limit file
  pattern="$1"
  limit="$2"
  file="$3"
  if [ -s "$file" ]; then
    grep -Ein "$pattern" "$file" 2>/dev/null | tail -n "$limit"
  fi
}

extract_digest() {
  local stdout_file stderr_file digest_file section_limit tail_limit
  stdout_file="$1"
  stderr_file="$2"
  digest_file="$3"
  section_limit="$(bounded_number "$MAX_DIGEST_LINES" 160)"
  section_limit=$((section_limit / 4))
  [ "$section_limit" -lt 20 ] && section_limit=20
  tail_limit="$(bounded_number "$MAX_TAIL_LINES" 80)"
  [ "$tail_limit" -gt "$section_limit" ] && tail_limit="$section_limit"

  {
    printf '%s\n\n' "# codex review digest"
    printf '%s\n' "## Finding-like lines"
    grep_tail 'finding|issue|bug|security|warning|regression|request changes|changes requested|指摘|問題|要修正|バグ|脆弱|危険|修正が必要' "$section_limit" "$stdout_file"
    printf '\n%s\n' "## File/line-like lines"
    grep_tail '(^|[[:space:]])[[:alnum:]_.\/-]+:[0-9]+(:[0-9]+)?' "$section_limit" "$stdout_file"
    printf '\n%s\n' "## Tail"
    if [ -s "$stdout_file" ]; then
      tail -n "$tail_limit" "$stdout_file"
    fi
    if [ -s "$stderr_file" ]; then
      printf '\n%s\n' "## stderr tail"
      tail -n "$tail_limit" "$stderr_file"
    fi
  } | awk -v max="$(bounded_number "$MAX_DIGEST_LINES" 160)" 'NR <= max { print }' >"$digest_file"
}

contains_pattern() {
  local pattern file
  pattern="$1"
  file="$2"
  [ -s "$file" ] && grep -Eiq "$pattern" "$file"
}

combined_contains_pattern() {
  local pattern stdout_file stderr_file
  pattern="$1"
  stdout_file="$2"
  stderr_file="$3"
  { [ -s "$stdout_file" ] && cat "$stdout_file"; [ -s "$stderr_file" ] && cat "$stderr_file"; } | grep -Eiq "$pattern"
}

classify_clean() {
  local exit_code stdout_file stderr_file positive negative
  exit_code="$1"
  stdout_file="$2"
  stderr_file="$3"

  positive='approved|looks good|no (major )?issues|no findings|0 findings|didn'\''t find (any )?(major )?issues|did not find (any )?(major )?issues|lgtm|指摘なし|問題なし|修正不要'
  negative='not approved|cannot approve|can'\''t approve|do not approve|request changes|changes requested|要修正|approve できない|修正が必要|問題があります|found [1-9][0-9]*|[1-9][0-9]* findings?'
  if [ "$exit_code" -ne 0 ]; then
    printf '%s\n' "unknown"
    return 0
  fi

  if contains_pattern "$positive" "$stdout_file"; then
    if contains_pattern "$negative" "$stdout_file"; then
      printf '%s\n' "unknown"
    else
      printf '%s\n' "true"
    fi
    return 0
  fi

  if contains_pattern "$negative" "$stdout_file"; then
    printf '%s\n' "false"
  else
    printf '%s\n' "unknown"
  fi
}

requires_escalation() {
  printf '%s\n' "false"
}

review_blocked_reason() {
  local exit_code stdout_file stderr_file
  exit_code="$1"
  stdout_file="$2"
  stderr_file="$3"

  if [ "$exit_code" -eq 0 ]; then
    return 0
  fi

  if combined_contains_pattern 'failed to lookup|error sending request|stream disconnected|backend-api|responses|websocket|dns error' "$stdout_file" "$stderr_file"; then
    printf '%s\n' "codex_connectivity"
  elif combined_contains_pattern 'operation not permitted|attempt to write a readonly database|permission denied|sandbox' "$stdout_file" "$stderr_file"; then
    printf '%s\n' "local_runtime_permission"
  else
    return 0
  fi
}

hash_file() {
  git hash-object "$1" 2>/dev/null || wc -c "$1" | awk '{print $1}'
}

read_last_value() {
  local key file
  key="$1"
  file="$2"
  [ -f "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; found=1; exit } END { exit(found ? 0 : 1) }' "$file"
}

write_last_env() {
  local file
  file="$1"
  shift
  : >"$file"
  while [ "$#" -gt 0 ]; do
    printf '%s\n' "$1" >>"$file"
    shift
  done
}

cmd_run() {
  local root state_dir logs_dir rid stdout_file stderr_file digest_file
  local scope_pair scope base head diff_file marker_diff_file files_file
  local diff_hash changed_files diff_lines review_cmd review_exit clean
  local digest_hash previous_hash stop_reason escalation marker_eligible marker_reason
  local blocked_reason
  local digest_display stdout_display stderr_display last_env
  local base_resolves

  ensure_repo
  root="$(repo_root)"
  cd "$root" || die "failed to enter repo root"
  command -v codex >/dev/null 2>&1 || die "codex command not found"

  state_dir="$(state_dir_abs)"
  logs_dir="$state_dir/logs"
  mkdir -p "$logs_dir" || die "failed to create state directory"

  scope_pair="$(detect_scope)" || exit 1
  scope="${scope_pair%%|*}"
  base="${scope_pair#*|}"
  head="$(git rev-parse HEAD 2>/dev/null || echo HEAD)"
  diff_file="$state_dir/current.diff"
  marker_diff_file="$state_dir/current-for-marker.diff"
  files_file="$state_dir/files.txt"
  write_target "$scope" "$base" "$diff_file" "$files_file"
  cp "$diff_file" "$marker_diff_file" 2>/dev/null || :

  diff_hash="$(hash_file "$diff_file")"
  changed_files="$(sed '/^[[:space:]]*$/d' "$files_file" 2>/dev/null | wc -l | awk '{print $1}')"
  diff_lines="$(wc -l "$diff_file" | awk '{print $1}')"
  review_cmd="$(review_command_label "$scope" "$base")"
  base_resolves="true"
  if [ "$scope" = "branch" ] && ! git rev-parse --verify "$base" >/dev/null 2>&1; then
    base_resolves="false"
  fi

  if [ "$base_resolves" = "true" ] && [ "$scope" != "uncommitted" ] && [ "$changed_files" -eq 0 ] && [ "$diff_lines" -eq 0 ]; then
    printf '%s\n' "post_work_review_version=$VERSION"
    printf '%s\n' "scope=$scope"
    printf '%s\n' "base=$base"
    printf '%s\n' "head=$head"
    printf '%s\n' "diff_hash=$diff_hash"
    printf '%s\n' "changed_files=$changed_files"
    printf '%s\n' "diff_lines=$diff_lines"
    printf '%s\n' "review_cmd=$review_cmd"
    printf '%s\n' "review_skipped=true"
    printf '%s\n' "skip_reason=empty_review_target"
    printf '%s\n' "clean=unknown"
    printf '%s\n' "rerun_requires_escalation=false"
    printf '%s\n' "marker_eligible=false"
    printf '%s\n' "marker_reason=empty_review_target"
    exit 0
  fi

  rid="$(run_id)"
  stdout_file="$logs_dir/codex-review-$rid.out"
  stderr_file="$logs_dir/codex-review-$rid.err"
  digest_file="$logs_dir/codex-review-$rid.digest.md"

  run_codex_review "$scope" "$base" "$stdout_file" "$stderr_file"
  review_exit="$?"
  extract_digest "$stdout_file" "$stderr_file" "$digest_file"
  clean="$(classify_clean "$review_exit" "$stdout_file" "$stderr_file")"
  escalation="$(requires_escalation "$review_exit" "$stdout_file" "$stderr_file")"
  blocked_reason="$(review_blocked_reason "$review_exit" "$stdout_file" "$stderr_file" || true)"

  digest_hash="$(hash_file "$digest_file")"
  previous_hash=""
  if [ -f "$state_dir/previous-digest-sig" ]; then
    previous_hash="$(cat "$state_dir/previous-digest-sig")"
  fi
  stop_reason=""
  if [ "$clean" != "true" ] && [ -n "$previous_hash" ] && [ "$previous_hash" = "$digest_hash" ]; then
    stop_reason="same_digest_repeated"
  elif [ -n "$blocked_reason" ]; then
    stop_reason="codex_review_blocked"
  fi
  printf '%s\n' "$digest_hash" >"$state_dir/previous-digest-sig"

  if has_dirty_tree; then
    marker_eligible="false"
    marker_reason="working_tree_dirty"
  elif [ "$clean" != "true" ]; then
    marker_eligible="false"
    marker_reason="review_not_clean"
  elif [ "$scope" != "branch" ]; then
    marker_eligible="false"
    marker_reason="non_branch_review_scope"
  else
    marker_eligible="true"
    marker_reason="clean_branch_review_and_clean_worktree"
  fi

  digest_display="$(display_path "$digest_file")"
  stdout_display="$(display_path "$stdout_file")"
  stderr_display="$(display_path "$stderr_file")"
  last_env="$state_dir/last.env"
  write_last_env "$last_env" \
    "post_work_review_version=$VERSION" \
    "reviewed_at=$(now_utc)" \
    "scope=$scope" \
    "base=$base" \
    "head=$head" \
    "diff_hash=$diff_hash" \
    "changed_files=$changed_files" \
    "diff_lines=$diff_lines" \
    "review_cmd=$review_cmd" \
    "review_exit_code=$review_exit" \
    "clean=$clean" \
    "digest=$digest_display" \
    "raw_output=$stdout_display" \
    "stderr=$stderr_display" \
    "rerun_requires_escalation=$escalation" \
    "review_blocked_reason=$blocked_reason" \
    "marker_eligible=$marker_eligible" \
    "marker_reason=$marker_reason"

  printf '%s\n' "post_work_review_version=$VERSION"
  printf '%s\n' "scope=$scope"
  printf '%s\n' "base=$base"
  printf '%s\n' "head=$head"
  printf '%s\n' "diff_hash=$diff_hash"
  printf '%s\n' "changed_files=$changed_files"
  printf '%s\n' "diff_lines=$diff_lines"
  printf '%s\n' "review_cmd=$review_cmd"
  printf '%s\n' "review_exit_code=$review_exit"
  printf '%s\n' "clean=$clean"
  printf '%s\n' "digest=$digest_display"
  if [ "$clean" = "unknown" ]; then
    printf '%s\n' "raw_output=$stdout_display"
    printf '%s\n' "stderr=$stderr_display"
  fi
  printf '%s\n' "rerun_requires_escalation=$escalation"
  if [ -n "$blocked_reason" ]; then
    printf '%s\n' "review_blocked_reason=$blocked_reason"
  fi
  printf '%s\n' "marker_eligible=$marker_eligible"
  printf '%s\n' "marker_reason=$marker_reason"
  if [ -n "$stop_reason" ]; then
    printf '%s\n' "stop_reason=$stop_reason"
  fi
}

cmd_mark() {
  local root gitdir state_dir last_env marker marker_meta current_head last_head
  local last_clean last_diff_hash current_diff_hash diff_file files_file scope base

  ensure_repo
  root="$(repo_root)"
  cd "$root" || die "failed to enter repo root"
  gitdir="$(git_dir_abs)"
  state_dir="$(state_dir_abs)"
  last_env="$state_dir/last.env"
  marker="$gitdir/post-work-review-passed"
  marker_meta="$gitdir/post-work-review-passed.meta"

  [ -f "$last_env" ] || die "last review state not found; run first"

  if has_dirty_tree; then
    printf '%s\n' "marker_written=false"
    printf '%s\n' "marker_reason=working_tree_dirty"
    exit 0
  fi

  last_clean="$(read_last_value clean "$last_env" || true)"
  if [ "$last_clean" != "true" ]; then
    printf '%s\n' "marker_written=false"
    printf '%s\n' "marker_reason=last_review_not_clean"
    exit 0
  fi

  current_head="$(git rev-parse HEAD)"
  last_head="$(read_last_value head "$last_env" || true)"
  if [ "$current_head" != "$last_head" ]; then
    printf '%s\n' "marker_written=false"
    printf '%s\n' "marker_reason=head_changed_since_last_review"
    printf '%s\n' "last_head=$last_head"
    printf '%s\n' "current_head=$current_head"
    exit 0
  fi

  scope="$(read_last_value scope "$last_env" || echo branch)"
  if [ "$scope" != "branch" ]; then
    printf '%s\n' "marker_written=false"
    printf '%s\n' "marker_reason=non_branch_review_scope"
    printf '%s\n' "scope=$scope"
    exit 0
  fi
  base="$(read_last_value base "$last_env" || true)"
  if [ -z "$base" ]; then
    base="$(resolve_base)" || die "default branch not found; set POST_WORK_REVIEW_BASE"
  fi
  diff_file="$state_dir/current-for-marker.diff"
  files_file="$state_dir/files-for-marker.txt"
  write_target "$scope" "$base" "$diff_file" "$files_file"
  current_diff_hash="$(hash_file "$diff_file")"
  last_diff_hash="$(read_last_value diff_hash "$last_env" || true)"
  if [ "$current_diff_hash" != "$last_diff_hash" ]; then
    printf '%s\n' "marker_written=false"
    printf '%s\n' "marker_reason=diff_changed_since_last_review"
    printf '%s\n' "last_diff_hash=$last_diff_hash"
    printf '%s\n' "current_diff_hash=$current_diff_hash"
    exit 0
  fi

  printf '%s\n' "$current_head" >"$marker"
  {
    printf '%s\n' "post_work_review_version=$VERSION"
    printf '%s\n' "marked_at=$(now_utc)"
    printf '%s\n' "head=$current_head"
    printf '%s\n' "scope=$scope"
    printf '%s\n' "base=$base"
    printf '%s\n' "diff_hash=$current_diff_hash"
    read_last_value review_cmd "$last_env" 2>/dev/null | sed 's/^/review_cmd=/'
    read_last_value digest "$last_env" 2>/dev/null | sed 's/^/digest=/'
  } >"$marker_meta"

  printf '%s\n' "marker_written=true"
  printf '%s\n' "marker=$(display_path "$marker")"
  printf '%s\n' "marker_meta=$(display_path "$marker_meta")"
  printf '%s\n' "head=$current_head"
}

cmd_handoff() {
  local root driver scope_pair rest scope base note review_cmd

  ensure_repo
  root="$(repo_root)"
  cd "$root" || die "failed to enter repo root"

  driver="$(script_path)"
  scope_pair="$(detect_scope_soft)"
  scope="${scope_pair%%|*}"
  rest="${scope_pair#*|}"
  base="${rest%%|*}"
  note="${rest#*|}"

  review_cmd=""
  if [ "$scope" != "unknown" ]; then
    review_cmd="$(review_command_label "$scope" "$base")"
  fi

  printf '%s\n' "post_work_review_version=$VERSION"
  printf '%s\n' "handoff_required=true"
  printf '%s\n' "handoff_reason=codex_review_blocked_by_policy"
  printf '%s\n' "repo_root=$root"
  printf '%s\n' "driver=$driver"
  printf '%s\n' "scope=$scope"
  if [ -n "$base" ]; then
    printf '%s\n' "base=$base"
  fi
  if [ -n "$review_cmd" ]; then
    printf '%s\n' "review_cmd=$review_cmd"
  fi
  if [ -n "$note" ]; then
    printf '%s\n' "handoff_note=$note"
  fi
  printf 'run_command='
  print_handoff_command "$root" "$driver" run
  printf 'mark_command='
  print_handoff_command "$root" "$driver" mark
  printf '%s\n' "completion_rule=run run_command in a shell that permits the local codex review command; if it prints clean=true and marker_eligible=true, run mark_command; do not mark clean without that output"
}

case "${1:-run}" in
  run)
    cmd_run
    ;;
  mark)
    cmd_mark
    ;;
  handoff)
    cmd_handoff
    ;;
  *)
    echo "usage:"
    echo "  bash codex/tools/post-work-review.sh run"
    echo "  bash codex/tools/post-work-review.sh mark"
    echo "  bash codex/tools/post-work-review.sh handoff"
    exit 2
    ;;
esac
