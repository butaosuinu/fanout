#!/usr/bin/env bash
set -u

VERSION="3"
BACKEND="bounded-isolated-reviewer"
REVIEWER_AGENT="post-work-reviewer"
VERIFIER_AGENT="post-work-verifier"
BROAD_REVIEW_MAX=1
VERIFY_REVIEW_MAX=2
MAX_TOTAL_REVIEWER_CALLS=3
MAX_FIX_ROUNDS=2
MAX_FINDINGS_PER_ROUND=20
BUDGET="broad_review_max=1,verify_review_max=2,max_total_reviewer_calls=3,max_fix_rounds=2,max_findings_per_round=20"
REVIEW_DIFF_OPTS=(--no-ext-diff --no-textconv --ignore-submodules=none)

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
  printf '%s/post-work-review\n' "$(git_dir_abs)"
}

results_dir() {
  printf '%s/results\n' "$(state_dir_abs)"
}

review_env_path() {
  printf '%s/review.env\n' "$(state_dir_abs)"
}

review_bundle_path() {
  printf '%s/review-bundle.md\n' "$(state_dir_abs)"
}

verify_bundle_path() {
  printf '%s/verify-bundle.md\n' "$(state_dir_abs)"
}

changed_files_path() {
  printf '%s/changed-files.txt\n' "$(state_dir_abs)"
}

findings_tsv_path() {
  printf '%s/findings.tsv\n' "$(state_dir_abs)"
}

pending_reviewed_diff_path() {
  printf '%s/pending-reviewed.diff\n' "$(state_dir_abs)"
}

marker_path() {
  printf '%s/post-work-review-passed\n' "$(git_dir_abs)"
}

marker_meta_path() {
  printf '%s/post-work-review-passed.meta\n' "$(git_dir_abs)"
}

display_path() {
  local root path
  root="$(repo_root)"
  path="$1"
  case "$path" in
    "$root"/*) printf '%s\n' "${path#"$root"/}" ;;
    *) printf '%s\n' "$path" ;;
  esac
}

agent_path() {
  printf '%s\n' "$1"
}

now_utc() {
  date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date
}

has_dirty_tree() {
  [ -n "$(git status --porcelain -uall)" ]
}

hash_file() {
  git hash-object "$1" 2>/dev/null || wc -c "$1" | awk '{print $1}'
}

count_lines() {
  local file
  file="$1"
  [ -f "$file" ] || {
    echo 0
    return 0
  }
  awk 'END { print NR + 0 }' "$file"
}

is_number() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
    *) return 0 ;;
  esac
}

resolve_base() {
  local remote_head candidate gh_default
  if [ -n "${POST_WORK_REVIEW_BASE:-}" ]; then
    printf '%s\n' "$POST_WORK_REVIEW_BASE"
    return 0
  fi

  remote_head="$(git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  if [ -n "$remote_head" ]; then
    printf '%s\n' "$remote_head"
    return 0
  fi

  if command -v gh >/dev/null 2>&1; then
    gh_default="$(gh repo view --json defaultBranchRef --jq '.defaultBranchRef.name' 2>/dev/null || true)"
    if [ -n "$gh_default" ]; then
      for candidate in "origin/$gh_default" "$gh_default"; do
        if git rev-parse --verify "$candidate" >/dev/null 2>&1; then
          printf '%s\n' "$candidate"
          return 0
        fi
      done
    fi
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

verify_branch_base() {
  local base
  base="$1"
  git rev-parse --verify "$base^{commit}" >/dev/null 2>&1 || die "branch base is not a commit: $base"
  git diff "${REVIEW_DIFF_OPTS[@]}" --name-only "$base"...HEAD -- >/dev/null 2>&1 || \
    git diff "${REVIEW_DIFF_OPTS[@]}" --name-only "$base" HEAD -- >/dev/null 2>&1 || \
    die "failed to compute branch diff for base=$base"
}

write_uncommitted_files() {
  local files_file untracked_file
  files_file="$1"
  untracked_file="$2"
  : >"$files_file"
  git diff "${REVIEW_DIFF_OPTS[@]}" --cached --name-only HEAD -- >>"$files_file" 2>/dev/null || true
  git diff "${REVIEW_DIFF_OPTS[@]}" --name-only -- >>"$files_file" 2>/dev/null || true
  git ls-files --others --exclude-standard >"$untracked_file" 2>/dev/null || true
  cat "$untracked_file" >>"$files_file"
  sort -u "$files_file" -o "$files_file"
}

write_branch_files() {
  local base files_file
  base="$1"
  files_file="$2"
  if git diff "${REVIEW_DIFF_OPTS[@]}" --name-only "$base"...HEAD -- >"$files_file" 2>/dev/null; then
    :
  elif git diff "${REVIEW_DIFF_OPTS[@]}" --name-only "$base" HEAD -- >"$files_file" 2>/dev/null; then
    :
  else
    rm -f "$files_file"
    die "failed to compute branch changed files for base=$base"
  fi
  sort -u "$files_file" -o "$files_file"
}

append_worktree_files() {
  local files_file untracked_file
  files_file="$1"
  untracked_file="$2"
  git diff "${REVIEW_DIFF_OPTS[@]}" --cached --name-only HEAD -- >>"$files_file" 2>/dev/null || true
  git diff "${REVIEW_DIFF_OPTS[@]}" --name-only -- >>"$files_file" 2>/dev/null || true
  git ls-files --others --exclude-standard >"$untracked_file" 2>/dev/null || true
  cat "$untracked_file" >>"$files_file"
  sort -u "$files_file" -o "$files_file"
}

write_commit_files() {
  local files_file
  files_file="$1"
  git diff-tree --no-commit-id --name-only -r HEAD >"$files_file" 2>/dev/null || : >"$files_file"
  sort -u "$files_file" -o "$files_file"
}

write_files_for_scope() {
  local scope base files_file untracked_file
  scope="$1"
  base="$2"
  files_file="$3"
  untracked_file="$4"
  : >"$untracked_file"
  case "$scope" in
    uncommitted) write_uncommitted_files "$files_file" "$untracked_file" ;;
    branch)
      write_branch_files "$base" "$files_file"
      append_worktree_files "$files_file" "$untracked_file"
      ;;
    commit) write_commit_files "$files_file" ;;
    *) die "invalid scope=$scope" ;;
  esac
}

append_untracked_diffs() {
  local untracked_file diff_file file
  untracked_file="$1"
  diff_file="$2"
  [ -s "$untracked_file" ] || return 0
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    [ -f "$file" ] || continue
    {
      printf '\n'
      git diff "${REVIEW_DIFF_OPTS[@]}" --no-index --binary -- /dev/null "$file" 2>/dev/null || true
    } >>"$diff_file"
  done <"$untracked_file"
}

append_uncommitted_tracked_diffs() {
  local diff_file
  diff_file="$1"
  git diff "${REVIEW_DIFF_OPTS[@]}" --cached --binary HEAD -- >>"$diff_file" 2>/dev/null || true
  git diff "${REVIEW_DIFF_OPTS[@]}" --binary -- >>"$diff_file" 2>/dev/null || true
}

write_diff_for_scope() {
  local scope base diff_file untracked_file
  scope="$1"
  base="$2"
  diff_file="$3"
  untracked_file="$4"
  case "$scope" in
    uncommitted)
      : >"$diff_file" || die "failed to write diff file"
      append_uncommitted_tracked_diffs "$diff_file"
      append_untracked_diffs "$untracked_file" "$diff_file"
      ;;
    branch)
      if git diff "${REVIEW_DIFF_OPTS[@]}" --binary "$base"...HEAD -- >"$diff_file" 2>/dev/null; then
        :
      elif git diff "${REVIEW_DIFF_OPTS[@]}" --binary "$base" HEAD -- >"$diff_file" 2>/dev/null; then
        :
      else
        rm -f "$diff_file"
        die "failed to compute branch diff for base=$base"
      fi
      append_uncommitted_tracked_diffs "$diff_file"
      append_untracked_diffs "$untracked_file" "$diff_file"
      ;;
    commit)
      git show "${REVIEW_DIFF_OPTS[@]}" --format= --binary HEAD >"$diff_file" 2>/dev/null || : >"$diff_file"
      ;;
    *)
      die "invalid scope=$scope"
      ;;
  esac
}

diffstat_for_file() {
  local diff_file
  diff_file="$1"
  git apply --stat <"$diff_file" 2>/dev/null || true
}

env_get() {
  local key file
  key="$1"
  file="$(review_env_path)"
  [ -f "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; found=1; exit } END { exit(found ? 0 : 1) }' "$file"
}

env_set() {
  local key value file tmp
  key="$1"
  value="$2"
  file="$(review_env_path)"
  tmp="$file.tmp.$$"
  awk -F= -v key="$key" -v value="$value" '
    $1 == key { print key "=" value; seen=1; next }
    { print }
    END { if (!seen) print key "=" value }
  ' "$file" >"$tmp" || die "failed to update review env"
  mv "$tmp" "$file" || die "failed to update review env"
}

json_ruby() {
  ruby -rjson -rdigest/sha1 -e "$1" "$2" "${3:-}" "${4:-}"
}

json_python() {
  python3 -c "$1" "$2" "${3:-}" "${4:-}"
}

json_eval() {
  local ruby_code python_code file arg
  ruby_code="$1"
  python_code="$2"
  file="$3"
  arg="${4:-}"
  if command -v ruby >/dev/null 2>&1; then
    json_ruby "$ruby_code" "$file" "$arg"
  elif command -v python3 >/dev/null 2>&1; then
    json_python "$python_code" "$file" "$arg"
  else
    die "ruby or python3 is required to parse reviewer JSON"
  fi
}

json_validate() {
  local file
  file="$1"
  json_eval \
    'JSON.parse(File.read(ARGV[0])); exit 0' \
    'import json,sys; json.load(open(sys.argv[1])); sys.exit(0)' \
    "$file"
}

json_scalar() {
  local file key
  file="$1"
  key="$2"
  json_eval \
    'data=JSON.parse(File.read(ARGV[0])); key=ARGV[1]; exit 1 unless data.key?(key); v=data[key]; if v == true then puts "true"; elsif v == false then puts "false"; elsif v.nil? then puts "null"; elsif v.is_a?(Array) || v.is_a?(Hash) then puts JSON.generate(v); else puts v.to_s; end' \
    'import json,sys; data=json.load(open(sys.argv[1])); key=sys.argv[2]; sys.exit(1) if key not in data else None; v=data[key]; print("true" if v is True else "false" if v is False else "null" if v is None else json.dumps(v,separators=(",",":")) if isinstance(v,(list,dict)) else str(v))' \
    "$file" "$key"
}

json_findings_count() {
  local file
  file="$1"
  json_eval \
    'data=JSON.parse(File.read(ARGV[0])); f=data["findings"]; exit 1 unless f.is_a?(Array); puts f.length' \
    'import json,sys; data=json.load(open(sys.argv[1])); f=data.get("findings"); sys.exit(1) if not isinstance(f,list) else print(len(f))' \
    "$file"
}

json_findings_missing_required_count() {
  local file
  file="$1"
  json_eval \
    'data=JSON.parse(File.read(ARGV[0])); findings=data["findings"]; exit 1 unless findings.is_a?(Array); required=%w[severity file line title description recommendation]; missing=0; findings.each do |f|; if !f.is_a?(Hash) || required.any? { |k| !f.key?(k) || f[k].nil? || f[k].to_s.strip.empty? }; missing += 1; end; end; puts missing' \
    'import json,sys; data=json.load(open(sys.argv[1])); findings=data.get("findings"); sys.exit(1) if not isinstance(findings,list) else None; required=["severity","file","line","title","description","recommendation"]; missing=0
for f in findings:
    if not isinstance(f,dict) or any(k not in f or f[k] is None or str(f[k]).strip()=="" for k in required):
        missing += 1
print(missing)' \
    "$file"
}

json_findings_tsv() {
  local file result_name
  file="$1"
  result_name="$2"
  json_eval \
    'def clean(v); v.to_s.gsub(/[[:space:]]+/, " ").strip; end; data=JSON.parse(File.read(ARGV[0])); result=ARGV[1]; findings=data["findings"]; exit 1 unless findings.is_a?(Array); findings.each_with_index do |f,i|; f={} unless f.is_a?(Hash); severity=clean(f["severity"]); path=clean(f["file"]); line=clean(f["line"]); title=clean(f["title"]); desc=clean(f["description"]); rec=clean(f["recommendation"]); fp_src=[path,line,title,desc].join("\0"); fp=Digest::SHA1.hexdigest(fp_src); puts [result,i+1,severity,path,line,title,fp,desc,rec].join("\t"); end' \
    'import hashlib,json,re,sys; data=json.load(open(sys.argv[1])); result=sys.argv[2]; findings=data.get("findings"); sys.exit(1) if not isinstance(findings,list) else None
def clean(v): return re.sub(r"\s+"," ", "" if v is None else str(v)).strip()
for i,f in enumerate(findings,1):
    f=f if isinstance(f,dict) else {}
    severity=clean(f.get("severity")); path=clean(f.get("file")); line=clean(f.get("line")); title=clean(f.get("title")); desc=clean(f.get("description")); rec=clean(f.get("recommendation"))
    fp=hashlib.sha1("\0".join([path,line,title,desc]).encode()).hexdigest()
    print("\t".join([result,str(i),severity,path,line,title,fp,desc,rec]))' \
    "$file" "$result_name"
}

result_path() {
  local kind index
  kind="$1"
  index="$2"
  case "$kind" in
    broad) printf '%s/broad-%03d.json\n' "$(results_dir)" "$index" ;;
    verify) printf '%s/verify-%03d.json\n' "$(results_dir)" "$index" ;;
    *) die "invalid result kind: $kind" ;;
  esac
}

broad_review_calls() {
  [ -f "$(result_path broad 1)" ] && echo 1 || echo 0
}

verify_review_calls() {
  local count i
  count=0
  for i in 1 2; do
    [ -f "$(result_path verify "$i")" ] && count=$((count + 1))
  done
  echo "$count"
}

total_reviewer_calls() {
  echo $(( $(broad_review_calls) + $(verify_review_calls) ))
}

latest_verify_result() {
  local i path
  for i in 2 1; do
    path="$(result_path verify "$i")"
    [ -f "$path" ] && {
      printf '%s\n' "$path"
      return 0
    }
  done
  return 1
}

latest_result() {
  local verify
  verify="$(latest_verify_result || true)"
  if [ -n "$verify" ]; then
    printf '%s\n' "$verify"
  else
    result_path broad 1
  fi
}

latest_finding_count() {
  local path count
  path="$(latest_result)"
  [ -f "$path" ] || {
    echo 0
    return 0
  }
  count="$(json_scalar "$path" finding_count 2>/dev/null || echo 0)"
  is_number "$count" || count=0
  echo "$count"
}

reviewer_session_used() {
  local candidate path i stored
  candidate="$1"
  path="$(result_path broad 1)"
  if [ -f "$path" ]; then
    stored="$(json_scalar "$path" reviewer_session_id 2>/dev/null || true)"
    [ "$stored" = "$candidate" ] && return 0
  fi
  for i in 1 2; do
    path="$(result_path verify "$i")"
    if [ -f "$path" ]; then
      stored="$(json_scalar "$path" reviewer_session_id 2>/dev/null || true)"
      [ "$stored" = "$candidate" ] && return 0
    fi
  done
  return 1
}

duplicate_reviewer_session_count() {
  local path i stored tmp
  tmp="$(state_dir_abs)/reviewer-sessions.tmp.$$"
  : >"$tmp" || die "failed to inspect reviewer sessions"
  path="$(result_path broad 1)"
  if [ -f "$path" ]; then
    stored="$(json_scalar "$path" reviewer_session_id 2>/dev/null || true)"
    [ -n "$stored" ] && [ "$stored" != "null" ] && printf '%s\n' "$stored" >>"$tmp"
  fi
  for i in 1 2; do
    path="$(result_path verify "$i")"
    if [ -f "$path" ]; then
      stored="$(json_scalar "$path" reviewer_session_id 2>/dev/null || true)"
      [ -n "$stored" ] && [ "$stored" != "null" ] && printf '%s\n' "$stored" >>"$tmp"
    fi
  done
  awk '
    { seen[$0]++ }
    END {
      repeated = 0
      for (session in seen) {
        if (seen[session] > 1) {
          repeated++
        }
      }
      print repeated + 0
    }
  ' "$tmp"
  rm -f "$tmp"
}

markdown_fence_for_file() {
  local file
  file="$1"
  awk '
    BEGIN { max = 2 }
    {
      line = $0
      while (match(line, /`+/)) {
        if (RLENGTH > max) {
          max = RLENGTH
        }
        line = substr(line, RSTART + RLENGTH)
      }
    }
    END {
      for (i = 0; i < max + 1; i++) {
        printf "`"
      }
      printf "\n"
    }
  ' "$file"
}

write_markdown_fenced_file() {
  local lang file fence
  lang="$1"
  file="$2"
  fence="$(markdown_fence_for_file "$file")"
  printf '%s%s\n' "$fence" "$lang"
  cat "$file"
  printf '%s\n' "$fence"
}

validate_result() {
  local kind file check_target expected_agent backend review_type provenance session same_agent isolated hooks_only
  local head diff_hash result_head result_diff finding_count actual_count missing_required truncated all_fixed new_regressions
  kind="$1"
  file="$2"
  check_target="${3:-target}"
  [ -f "$file" ] || die "review JSON not found: $file"
  json_validate "$file" >/dev/null 2>&1 || die "invalid reviewer JSON"

  backend="$(json_scalar "$file" backend 2>/dev/null || true)"
  [ "$backend" = "$BACKEND" ] || die "review backend mismatch"
  review_type="$(json_scalar "$file" review_type 2>/dev/null || true)"
  [ "$review_type" = "$kind" ] || die "review_type mismatch"

  if [ "$kind" = "broad" ]; then
    expected_agent="$REVIEWER_AGENT"
  else
    expected_agent="$VERIFIER_AGENT"
  fi
  [ "$(json_scalar "$file" reviewer_agent 2>/dev/null || true)" = "$expected_agent" ] || die "reviewer_agent mismatch"
  provenance="$(json_scalar "$file" reviewer_provenance 2>/dev/null || true)"
  [ "$provenance" = "native-subagent-tool" ] || die "reviewer provenance rejected"
  session="$(json_scalar "$file" reviewer_session_id 2>/dev/null || true)"
  if [ -z "$session" ] || [ "$session" = "null" ]; then
    die "reviewer_session_id is required"
  fi

  same_agent="$(json_scalar "$file" same_agent_review 2>/dev/null || true)"
  [ "$same_agent" = "false" ] || die "same-agent review is rejected"
  isolated="$(json_scalar "$file" reviewer_isolated 2>/dev/null || true)"
  [ "$isolated" = "true" ] || die "non-isolated reviewer is rejected"
  hooks_only="$(json_scalar "$file" hooks_only_success 2>/dev/null || true)"
  [ "$hooks_only" = "false" ] || die "hooks-only success is rejected"

  if [ "$check_target" = "target" ]; then
    head="$(env_get head || true)"
    diff_hash="$(env_get diff_hash || true)"
    result_head="$(json_scalar "$file" head 2>/dev/null || true)"
    result_diff="$(json_scalar "$file" diff_hash 2>/dev/null || true)"
    [ "$result_head" = "$head" ] || die "review head mismatch"
    [ "$result_diff" = "$diff_hash" ] || die "review diff_hash mismatch"
  else
    result_head="$(json_scalar "$file" head 2>/dev/null || true)"
    result_diff="$(json_scalar "$file" diff_hash 2>/dev/null || true)"
    if [ -z "$result_head" ] || [ "$result_head" = "null" ]; then
      die "review head is required"
    fi
    if [ -z "$result_diff" ] || [ "$result_diff" = "null" ]; then
      die "review diff_hash is required"
    fi
  fi

  finding_count="$(json_scalar "$file" finding_count 2>/dev/null || true)"
  is_number "$finding_count" || die "finding_count must be a number"
  actual_count="$(json_findings_count "$file" 2>/dev/null || true)"
  is_number "$actual_count" || die "findings must be an array"
  [ "$finding_count" = "$actual_count" ] || die "finding_count does not match findings array"
  [ "$finding_count" -le "$MAX_FINDINGS_PER_ROUND" ] || die "finding_count exceeds max_findings_per_round"
  missing_required="$(json_findings_missing_required_count "$file" 2>/dev/null || true)"
  is_number "$missing_required" || die "findings must be an array"
  [ "$missing_required" -eq 0 ] || die "finding missing required fields"

  truncated="$(json_scalar "$file" truncated 2>/dev/null || true)"
  [ "$truncated" = "true" ] || [ "$truncated" = "false" ] || die "truncated must be true or false"

  if [ "$kind" = "verify" ]; then
    all_fixed="$(json_scalar "$file" all_previous_findings_fixed 2>/dev/null || true)"
    new_regressions="$(json_scalar "$file" new_regressions 2>/dev/null || true)"
    [ "$all_fixed" = "true" ] || [ "$all_fixed" = "false" ] || die "all_previous_findings_fixed must be true or false"
    [ "$new_regressions" = "true" ] || [ "$new_regressions" = "false" ] || die "new_regressions must be true or false"
    if { [ "$all_fixed" != "true" ] || [ "$new_regressions" = "true" ]; } && [ "$finding_count" -eq 0 ]; then
      die "failed verifier result requires findings"
    fi
  fi
}

rewrite_findings_tsv() {
  local out path name i
  out="$(findings_tsv_path)"
  mkdir -p "$(dirname "$out")"
  printf 'result\tindex\tseverity\tfile\tline\ttitle\tfingerprint\tdescription\trecommendation\n' >"$out"
  path="$(result_path broad 1)"
  if [ -f "$path" ]; then
    json_findings_tsv "$path" "broad-001" >>"$out" 2>/dev/null || true
  fi
  for i in 1 2; do
    path="$(result_path verify "$i")"
    name="$(printf 'verify-%03d' "$i")"
    if [ -f "$path" ]; then
      json_findings_tsv "$path" "$name" >>"$out" 2>/dev/null || true
    fi
  done
}

repeated_fingerprint_count() {
  local file
  file="$(findings_tsv_path)"
  [ -f "$file" ] || {
    echo 0
    return 0
  }
  awk -F'\t' '
    NR > 1 && $7 != "" {
      key = $1 SUBSEP $7
      if (!seen_result[key]++) {
        seen[$7]++
      }
    }
    END {
      repeated = 0
      for (fp in seen) {
        if (seen[fp] > 1) {
          repeated++
        }
      }
      print repeated + 0
    }
  ' "$file"
}

any_result_truncated() {
  local path i value
  path="$(result_path broad 1)"
  if [ -f "$path" ]; then
    value="$(json_scalar "$path" truncated 2>/dev/null || echo false)"
    [ "$value" = "true" ] && return 0
  fi
  for i in 1 2; do
    path="$(result_path verify "$i")"
    if [ -f "$path" ]; then
      value="$(json_scalar "$path" truncated 2>/dev/null || echo false)"
      [ "$value" = "true" ] && return 0
    fi
  done
  return 1
}

review_target_changed_reason() {
  local reviewed_head reviewed_diff_hash
  reviewed_head="$(env_get head 2>/dev/null || true)"
  reviewed_diff_hash="$(env_get diff_hash 2>/dev/null || true)"
  [ -n "$reviewed_head" ] && [ -n "$reviewed_diff_hash" ] || return 1
  compute_current_target
  if [ "$HEAD_SHA" != "$reviewed_head" ]; then
    printf 'head\n'
    return 0
  fi
  if [ "$DIFF_HASH" != "$reviewed_diff_hash" ]; then
    printf 'diff_hash\n'
    return 0
  fi
  return 1
}

summary_values() {
  local broad_calls verify_calls total_calls latest_path latest_count clean findings stop marker
  local repeated all_fixed new_regressions truncated fix_rounds scope changed_files pending_verify invalid_count duplicate_sessions i path
  local target_changed_reason
  broad_calls="$(broad_review_calls)"
  verify_calls="$(verify_review_calls)"
  total_calls="$(total_reviewer_calls)"
  clean="unknown"
  findings=0
  stop=""
  marker="false"
  invalid_count=0

  for i in 1 2; do
    path="$(result_path verify "$i")"
    [ -f "$path" ] || continue
    validate_result verify "$path" static >/dev/null 2>&1 || invalid_count=$((invalid_count + 1))
  done
  path="$(result_path broad 1)"
  if [ -f "$path" ]; then
    validate_result broad "$path" static >/dev/null 2>&1 || invalid_count=$((invalid_count + 1))
  fi
  duplicate_sessions="$(duplicate_reviewer_session_count)"
  if [ "${SUMMARY_SKIP_TARGET_CHECK:-}" = "1" ]; then
    target_changed_reason=""
  else
    target_changed_reason="$(review_target_changed_reason || true)"
  fi

  if [ "$invalid_count" -gt 0 ] || [ "$duplicate_sessions" -gt 0 ]; then
    clean="false"
    stop="invalid_review_result"
  elif [ -n "$target_changed_reason" ]; then
    clean="false"
    findings=0
    stop="review_target_changed"
  elif [ "$broad_calls" -gt "$BROAD_REVIEW_MAX" ] || [ "$verify_calls" -gt "$VERIFY_REVIEW_MAX" ] || [ "$total_calls" -gt "$MAX_TOTAL_REVIEWER_CALLS" ]; then
    clean="false"
    stop="review_budget_exhausted"
  elif any_result_truncated; then
    clean="unknown"
    stop="review_truncated"
  elif [ "$(env_get pending_verify 2>/dev/null || echo 0)" = "1" ]; then
    clean="false"
    findings=0
  elif [ "$broad_calls" -eq 0 ]; then
    clean="unknown"
  else
    latest_path="$(latest_result)"
    latest_count="$(json_scalar "$latest_path" finding_count 2>/dev/null || echo 0)"
    findings="$latest_count"
    if [ "$verify_calls" -eq 0 ]; then
      if [ "$latest_count" -eq 0 ]; then
        clean="true"
      else
        clean="false"
      fi
    else
      repeated="$(repeated_fingerprint_count)"
      if [ "$repeated" -gt 0 ]; then
        clean="false"
        stop="same_finding_repeated"
      else
        all_fixed="$(json_scalar "$latest_path" all_previous_findings_fixed 2>/dev/null || echo false)"
        new_regressions="$(json_scalar "$latest_path" new_regressions 2>/dev/null || echo true)"
        if [ "$all_fixed" = "true" ] && [ "$new_regressions" = "false" ] && [ "$latest_count" -eq 0 ]; then
          clean="true"
          findings=0
        else
          clean="false"
          findings="$latest_count"
          if [ "$verify_calls" -ge "$VERIFY_REVIEW_MAX" ] || [ "$total_calls" -ge "$MAX_TOTAL_REVIEWER_CALLS" ]; then
            stop="review_budget_exhausted"
          fi
        fi
      fi
    fi
  fi

  fix_rounds="$(env_get fix_rounds 2>/dev/null || echo 0)"
  pending_verify="$(env_get pending_verify 2>/dev/null || echo 0)"
  if [ -z "$stop" ] && [ "$clean" != "true" ] && [ "$broad_calls" -eq 1 ] && [ "$verify_calls" -ge "$VERIFY_REVIEW_MAX" ]; then
    stop="review_budget_exhausted"
  fi
  if [ -z "$stop" ] && [ "$clean" != "true" ] && [ "$fix_rounds" -gt "$MAX_FIX_ROUNDS" ]; then
    stop="review_budget_exhausted"
  fi

  scope="$(env_get scope 2>/dev/null || echo unknown)"
  changed_files="$(env_get changed_files 2>/dev/null || echo 0)"
  if [ "$clean" = "true" ] && [ -z "$stop" ] && [ "$broad_calls" -eq 1 ] && \
    [ "$total_calls" -le "$MAX_TOTAL_REVIEWER_CALLS" ] && [ "$scope" = "branch" ] && \
    [ "$changed_files" != "0" ] && [ "$pending_verify" != "1" ] && ! has_dirty_tree; then
    marker="true"
  fi

  SUMMARY_CLEAN="$clean"
  SUMMARY_FINDINGS="$findings"
  SUMMARY_STOP_REASON="$stop"
  SUMMARY_MARKER_ELIGIBLE="$marker"
  SUMMARY_BROAD_CALLS="$broad_calls"
  SUMMARY_VERIFY_CALLS="$verify_calls"
  SUMMARY_TOTAL_CALLS="$total_calls"
}

print_budget_lines() {
  printf 'budget=%s\n' "$BUDGET"
  printf 'broad_review_max=%s\n' "$BROAD_REVIEW_MAX"
  printf 'verify_review_max=%s\n' "$VERIFY_REVIEW_MAX"
  printf 'max_total_reviewer_calls=%s\n' "$MAX_TOTAL_REVIEWER_CALLS"
  printf 'max_fix_rounds=%s\n' "$MAX_FIX_ROUNDS"
  printf 'max_findings_per_round=%s\n' "$MAX_FINDINGS_PER_ROUND"
}

print_state_lines() {
  printf 'post_work_review_version=%s\n' "$VERSION"
  printf 'backend=%s\n' "$(env_get backend 2>/dev/null || echo "$BACKEND")"
  printf 'scope=%s\n' "$(env_get scope 2>/dev/null || echo unknown)"
  printf 'base=%s\n' "$(env_get base 2>/dev/null || echo unknown)"
  printf 'head=%s\n' "$(env_get head 2>/dev/null || echo unknown)"
  printf 'diff_hash=%s\n' "$(env_get diff_hash 2>/dev/null || echo unknown)"
  printf 'changed_files=%s\n' "$(env_get changed_files 2>/dev/null || echo 0)"
  printf 'fix_rounds=%s\n' "$(env_get fix_rounds 2>/dev/null || echo 0)"
  printf 'pending_verify=%s\n' "$(env_get pending_verify 2>/dev/null || echo 0)"
  printf 'review_bundle=%s\n' "$(agent_path "$(review_bundle_path)")"
  if [ -f "$(verify_bundle_path)" ]; then
    printf 'verify_bundle=%s\n' "$(agent_path "$(verify_bundle_path)")"
  fi
  printf 'findings_tsv=%s\n' "$(agent_path "$(findings_tsv_path)")"
  print_budget_lines
  printf 'broad_review_calls=%s\n' "${SUMMARY_BROAD_CALLS:-$(broad_review_calls)}"
  printf 'verify_review_calls=%s\n' "${SUMMARY_VERIFY_CALLS:-$(verify_review_calls)}"
  printf 'total_reviewer_calls=%s\n' "${SUMMARY_TOTAL_CALLS:-$(total_reviewer_calls)}"
}

write_review_env() {
  local file
  file="$(review_env_path)"
  {
    printf 'post_work_review_version=%s\n' "$VERSION"
    printf 'backend=%s\n' "$BACKEND"
    printf 'prepared_at=%s\n' "$PREPARED_AT"
    printf 'scope=%s\n' "$SCOPE"
    printf 'base=%s\n' "$BASE"
    printf 'head=%s\n' "$HEAD_SHA"
    printf 'diff_hash=%s\n' "$DIFF_HASH"
    printf 'changed_files=%s\n' "$CHANGED_FILE_COUNT"
    printf 'fix_rounds=0\n'
    printf 'pending_verify=0\n'
    printf 'review_bundle=%s\n' "$(agent_path "$(review_bundle_path)")"
    printf 'findings_tsv=%s\n' "$(agent_path "$(findings_tsv_path)")"
  } >"$file"
}

write_review_bundle() {
  local bundle diff_file diffstat_file
  bundle="$(review_bundle_path)"
  diff_file="$(state_dir_abs)/current.diff"
  diffstat_file="$(state_dir_abs)/current.diffstat"
  diffstat_for_file "$diff_file" >"$diffstat_file"
  {
    printf '# post-work-review broad review bundle\n\n'
    printf 'This bundle is for exactly one fresh isolated broad reviewer call.\n'
    printf 'The reviewer must be %s, read-only, and JSON-only.\n\n' "$REVIEWER_AGENT"
    printf '## Review contract\n\n'
    printf -- '- backend: %s\n' "$BACKEND"
    printf -- '- review_type: broad\n'
    printf -- '- scope: %s\n' "$SCOPE"
    printf -- '- base: %s\n' "$BASE"
    printf -- '- head: %s\n' "$HEAD_SHA"
    printf -- '- diff_hash: %s\n' "$DIFF_HASH"
    printf -- '- max_findings: %s\n' "$MAX_FINDINGS_PER_ROUND"
    printf -- '- Return at most blocker/major actionable findings.\n'
    printf -- '- Ignore style, formatting, lint, and speculative improvements.\n'
    printf -- '- Set truncated=true if more than %s blocking/major findings are present.\n' "$MAX_FINDINGS_PER_ROUND"
    printf -- '- Do not run tests, linters, formatters, typecheck, project checks, local LLMs, or codex review.\n\n'
    printf '## Required JSON shape\n\n'
    printf '```json\n'
    printf '{"backend":"%s","review_type":"broad","reviewer_agent":"%s","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"<fresh subagent id>","same_agent_review":false,"reviewer_isolated":true,"hooks_only_success":false,"head":"%s","diff_hash":"%s","truncated":false,"finding_count":0,"findings":[]}\n' "$BACKEND" "$REVIEWER_AGENT" "$HEAD_SHA" "$DIFF_HASH"
    printf '```\n\n'
    printf 'Each finding must include severity, file, line, title, description, and recommendation.\n\n'
    printf '## Changed files\n\n'
    if [ -s "$(changed_files_path)" ]; then
      sed 's/^/- /' "$(changed_files_path)"
    else
      printf -- '- none\n'
    fi
    printf '\n## Diffstat\n\n'
    write_markdown_fenced_file text "$diffstat_file"
    printf '\n## Diff\n\n'
    write_markdown_fenced_file diff "$diff_file"
  } >"$bundle"
}

write_verify_bundle() {
  local bundle current_diff fix_diff
  bundle="$(verify_bundle_path)"
  current_diff="$(state_dir_abs)/current.diff"
  fix_diff="$(state_dir_abs)/fix.diff"
  {
    printf '# post-work-review verification bundle\n\n'
    printf 'This bundle is for a fresh isolated verifier call, not a broad review.\n'
    printf 'The verifier must check only prior findings and obvious regressions introduced by the fix.\n'
    printf 'Do not hunt for unrelated new issues.\n\n'
    printf '## Verification contract\n\n'
    printf -- '- backend: %s\n' "$BACKEND"
    printf -- '- review_type: verify\n'
    printf -- '- verifier_agent: %s\n' "$VERIFIER_AGENT"
    printf -- '- scope: %s\n' "$(env_get scope)"
    printf -- '- base: %s\n' "$(env_get base)"
    printf -- '- head: %s\n' "$(env_get head)"
    printf -- '- diff_hash: %s\n' "$(env_get diff_hash)"
    printf -- '- max_findings: %s\n' "$MAX_FINDINGS_PER_ROUND"
    printf -- '- Return findings only for still-unfixed prior findings or obvious fix-introduced regressions.\n'
    printf -- '- Set all_previous_findings_fixed=true only if every prior finding is resolved.\n'
    printf -- '- Set new_regressions=true if the fix obviously introduced a blocker/major regression.\n'
    printf -- '- Set truncated=true if more than %s in-scope findings are present.\n\n' "$MAX_FINDINGS_PER_ROUND"
    printf '## Required JSON shape\n\n'
    printf '```json\n'
    printf '{"backend":"%s","review_type":"verify","reviewer_agent":"%s","reviewer_provenance":"native-subagent-tool","reviewer_session_id":"<fresh subagent id>","same_agent_review":false,"reviewer_isolated":true,"hooks_only_success":false,"head":"%s","diff_hash":"%s","all_previous_findings_fixed":true,"new_regressions":false,"truncated":false,"finding_count":0,"findings":[]}\n' "$BACKEND" "$VERIFIER_AGENT" "$(env_get head)" "$(env_get diff_hash)"
    printf '```\n\n'
    printf '## Prior findings\n\n'
    write_markdown_fenced_file tsv "$(findings_tsv_path)"
    printf '\n## Current fix diff\n\n'
    write_markdown_fenced_file diff "$fix_diff"
    printf '\n## Current scoped diff\n\n'
    write_markdown_fenced_file diff "$current_diff"
  } >"$bundle"
}

compute_current_target() {
  local state scope base files_file untracked_file diff_file
  state="$(state_dir_abs)"
  scope="$(env_get scope)"
  base="$(env_get base)"
  files_file="$(changed_files_path)"
  untracked_file="$state/untracked-files.txt"
  diff_file="$state/current.diff"
  write_files_for_scope "$scope" "$base" "$files_file" "$untracked_file"
  write_diff_for_scope "$scope" "$base" "$diff_file" "$untracked_file"
  HEAD_SHA="$(git rev-parse HEAD)"
  DIFF_HASH="$(hash_file "$diff_file")"
  CHANGED_FILE_COUNT="$(count_lines "$files_file")"
}

ensure_current_target_matches_review() {
  local reviewed_head reviewed_diff_hash
  reviewed_head="$(env_get head || true)"
  reviewed_diff_hash="$(env_get diff_hash || true)"
  compute_current_target
  if [ "$HEAD_SHA" != "$reviewed_head" ]; then
    die "review target changed since prepare: head"
  fi
  if [ "$DIFF_HASH" != "$reviewed_diff_hash" ]; then
    die "review target changed since prepare: diff_hash"
  fi
}

commit_pending_reviewed_diff() {
  local pending old
  pending="$(pending_reviewed_diff_path)"
  old="$(state_dir_abs)/last-reviewed.diff"
  [ -f "$pending" ] || die "pending verify diff not found"
  cp "$pending" "$old" || die "failed to store review diff"
  rm -f "$pending"
}

write_initial_findings_tsv() {
  printf 'result\tindex\tseverity\tfile\tline\ttitle\tfingerprint\tdescription\trecommendation\n' >"$(findings_tsv_path)"
}

cmd_prepare() {
  local root state results scope_pair untracked_file diff_file
  ensure_repo
  root="$(repo_root)"
  cd "$root" || die "failed to enter repo root"
  state="$(state_dir_abs)"
  results="$(results_dir)"
  rm -rf "$state"
  mkdir -p "$results" || die "failed to create post-work-review state"

  PREPARED_AT="$(now_utc)"
  scope_pair="$(detect_scope)"
  SCOPE="${scope_pair%%|*}"
  BASE="${scope_pair#*|}"
  if [ "$SCOPE" = "branch" ]; then
    verify_branch_base "$BASE"
  fi
  HEAD_SHA="$(git rev-parse HEAD)"
  diff_file="$state/current.diff"
  untracked_file="$state/untracked-files.txt"

  write_files_for_scope "$SCOPE" "$BASE" "$(changed_files_path)" "$untracked_file"
  write_diff_for_scope "$SCOPE" "$BASE" "$diff_file" "$untracked_file"
  DIFF_HASH="$(hash_file "$diff_file")"
  CHANGED_FILE_COUNT="$(count_lines "$(changed_files_path)")"
  cp "$diff_file" "$state/last-reviewed.diff" || die "failed to store review diff"
  write_initial_findings_tsv
  write_review_env
  write_review_bundle

  SUMMARY_BROAD_CALLS=0
  SUMMARY_VERIFY_CALLS=0
  SUMMARY_TOTAL_CALLS=0
  print_state_lines
}

cmd_prepare_verify() {
  local root state broad_calls verify_calls total_calls fix_rounds latest_findings old_diff current_diff fix_diff pending_diff
  ensure_repo
  root="$(repo_root)"
  cd "$root" || die "failed to enter repo root"
  [ -f "$(review_env_path)" ] || die "review state not found; run prepare first"
  broad_calls="$(broad_review_calls)"
  [ "$broad_calls" -eq 1 ] || die "broad review result not found"
  verify_calls="$(verify_review_calls)"
  total_calls="$(total_reviewer_calls)"
  if [ "$verify_calls" -ge "$VERIFY_REVIEW_MAX" ] || [ "$total_calls" -ge "$MAX_TOTAL_REVIEWER_CALLS" ]; then
    summary_values
    printf 'clean=false\n'
    printf 'findings=%s\n' "${SUMMARY_FINDINGS:-0}"
    printf 'stop_reason=review_budget_exhausted\n'
    printf 'marker_eligible=false\n'
    exit 1
  fi
  fix_rounds="$(env_get fix_rounds 2>/dev/null || echo 0)"
  if [ "$fix_rounds" -ge "$MAX_FIX_ROUNDS" ]; then
    summary_values
    printf 'clean=false\n'
    printf 'findings=%s\n' "${SUMMARY_FINDINGS:-0}"
    printf 'stop_reason=review_budget_exhausted\n'
    printf 'marker_eligible=false\n'
    exit 1
  fi

  rewrite_findings_tsv
  latest_findings="$(latest_finding_count)"
  if [ "$latest_findings" -eq 0 ]; then
    printf 'clean=false\n'
    printf 'findings=0\n'
    printf 'stop_reason=no_prior_findings_to_verify\n'
    printf 'marker_eligible=false\n'
    exit 1
  fi

  state="$(state_dir_abs)"
  old_diff="$state/last-reviewed.diff"
  current_diff="$state/current.diff"
  fix_diff="$state/fix.diff"
  pending_diff="$(pending_reviewed_diff_path)"
  compute_current_target
  if [ -f "$old_diff" ]; then
    diff -u "$old_diff" "$current_diff" >"$fix_diff" 2>/dev/null || true
  else
    cp "$current_diff" "$fix_diff" || die "failed to store fix diff"
  fi
  cp "$current_diff" "$pending_diff" || die "failed to store pending review diff"

  fix_rounds=$((fix_rounds + 1))
  env_set head "$HEAD_SHA"
  env_set diff_hash "$DIFF_HASH"
  env_set changed_files "$CHANGED_FILE_COUNT"
  env_set fix_rounds "$fix_rounds"
  env_set pending_verify 1
  env_set verify_bundle "$(agent_path "$(verify_bundle_path)")"
  write_verify_bundle

  summary_values
  print_state_lines
}

cmd_record() {
  local kind review_json broad_calls verify_calls total_calls dest index session fix_rounds
  kind="${1:-}"
  review_json="${2:-}"
  [ "$kind" = "broad" ] || [ "$kind" = "verify" ] || die "record kind must be broad or verify"
  [ -n "$review_json" ] || die "review-json-file is required"
  ensure_repo
  cd "$(repo_root)" || die "failed to enter repo root"
  [ -f "$(review_env_path)" ] || die "review state not found; run prepare first"
  mkdir -p "$(results_dir)" || die "failed to create results dir"

  broad_calls="$(broad_review_calls)"
  verify_calls="$(verify_review_calls)"
  total_calls="$(total_reviewer_calls)"
  [ "$total_calls" -lt "$MAX_TOTAL_REVIEWER_CALLS" ] || die "review budget exhausted"

  case "$kind" in
    broad)
      [ "$broad_calls" -lt "$BROAD_REVIEW_MAX" ] || die "broad review budget exhausted"
      index=1
      ;;
    verify)
      [ "$broad_calls" -eq 1 ] || die "broad review must be recorded before verify"
      [ "$verify_calls" -lt "$VERIFY_REVIEW_MAX" ] || die "verify review budget exhausted"
      [ -f "$(verify_bundle_path)" ] || die "verify bundle not prepared"
      [ -f "$(pending_reviewed_diff_path)" ] || die "pending verify diff not found"
      fix_rounds="$(env_get fix_rounds 2>/dev/null || echo 0)"
      [ "$fix_rounds" -gt "$verify_calls" ] || die "verify result has no prepared fix round"
      index=$((verify_calls + 1))
      ;;
  esac

  ensure_current_target_matches_review
  validate_result "$kind" "$review_json"
  session="$(json_scalar "$review_json" reviewer_session_id)"
  if reviewer_session_used "$session"; then
    die "reviewer_session_id already recorded"
  fi
  dest="$(result_path "$kind" "$index")"
  cp "$review_json" "$dest" || die "failed to store review result"
  if [ "$kind" = "verify" ]; then
    commit_pending_reviewed_diff
    env_set pending_verify 0
  fi
  rewrite_findings_tsv
  summary_values

  printf 'recorded=true\n'
  printf 'kind=%s\n' "$kind"
  printf 'result=%s\n' "$(display_path "$dest")"
  printf 'finding_count=%s\n' "$(json_scalar "$dest" finding_count)"
  printf 'truncated=%s\n' "$(json_scalar "$dest" truncated)"
  printf 'broad_review_calls=%s\n' "$SUMMARY_BROAD_CALLS"
  printf 'verify_review_calls=%s\n' "$SUMMARY_VERIFY_CALLS"
  printf 'total_reviewer_calls=%s\n' "$SUMMARY_TOTAL_CALLS"
}

cmd_summarize() {
  ensure_repo
  cd "$(repo_root)" || die "failed to enter repo root"
  [ -f "$(review_env_path)" ] || die "review state not found; run prepare first"
  rewrite_findings_tsv
  summary_values
  print_state_lines
  printf 'clean=%s\n' "$SUMMARY_CLEAN"
  printf 'findings=%s\n' "$SUMMARY_FINDINGS"
  printf 'stop_reason=%s\n' "$SUMMARY_STOP_REASON"
  printf 'marker_eligible=%s\n' "$SUMMARY_MARKER_ELIGIBLE"
}

cmd_status() {
  ensure_repo
  cd "$(repo_root)" || die "failed to enter repo root"
  if [ ! -f "$(review_env_path)" ]; then
    printf 'prepared=false\n'
    printf 'backend=%s\n' "$BACKEND"
    print_budget_lines
    printf 'broad_review_calls=0\n'
    printf 'verify_review_calls=0\n'
    printf 'total_reviewer_calls=0\n'
    printf 'clean=unknown\n'
    printf 'findings=0\n'
    printf 'stop_reason=\n'
    printf 'marker_eligible=false\n'
    exit 0
  fi
  rewrite_findings_tsv
  summary_values
  printf 'prepared=true\n'
  print_state_lines
  printf 'clean=%s\n' "$SUMMARY_CLEAN"
  printf 'findings=%s\n' "$SUMMARY_FINDINGS"
  printf 'stop_reason=%s\n' "$SUMMARY_STOP_REASON"
  printf 'marker_eligible=%s\n' "$SUMMARY_MARKER_ELIGIBLE"
}

current_diff_hash_for_mark() {
  local state scope base files_file untracked_file diff_file
  state="$(state_dir_abs)"
  scope="$(env_get scope)"
  base="$(env_get base)"
  files_file="$state/mark-current-files.txt"
  untracked_file="$state/mark-untracked-files.txt"
  diff_file="$state/mark-current.diff"
  write_files_for_scope "$scope" "$base" "$files_file" "$untracked_file"
  write_diff_for_scope "$scope" "$base" "$diff_file" "$untracked_file"
  hash_file "$diff_file"
}

mark_reject() {
  printf 'marker_written=false\n'
  printf 'marker_reason=%s\n' "$1"
  exit 1
}

cmd_mark() {
  local backend scope current_head reviewed_head current_hash reviewed_hash marker marker_meta
  ensure_repo
  cd "$(repo_root)" || die "failed to enter repo root"
  [ -f "$(review_env_path)" ] || mark_reject "review_state_missing"
  backend="$(env_get backend || true)"
  [ "$backend" = "$BACKEND" ] || mark_reject "backend_not_bounded_isolated_reviewer"

  rewrite_findings_tsv
  SUMMARY_SKIP_TARGET_CHECK=1
  summary_values
  unset SUMMARY_SKIP_TARGET_CHECK
  [ "$SUMMARY_BROAD_CALLS" -eq 1 ] || mark_reject "broad_review_count_invalid"
  [ "$SUMMARY_TOTAL_CALLS" -le "$MAX_TOTAL_REVIEWER_CALLS" ] || mark_reject "review_budget_exhausted"
  [ "$SUMMARY_CLEAN" = "true" ] || mark_reject "last_review_not_clean"
  [ -z "$SUMMARY_STOP_REASON" ] || mark_reject "$SUMMARY_STOP_REASON"

  scope="$(env_get scope || true)"
  [ "$scope" = "branch" ] || mark_reject "non_branch_review_scope"
  [ "$(env_get changed_files || echo 0)" != "0" ] || mark_reject "empty_branch_review_scope"
  if has_dirty_tree; then
    mark_reject "working_tree_dirty"
  fi

  current_head="$(git rev-parse HEAD)"
  reviewed_head="$(env_get head || true)"
  [ "$current_head" = "$reviewed_head" ] || {
    printf 'marker_written=false\n'
    printf 'marker_reason=head_changed_since_review\n'
    printf 'reviewed_head=%s\n' "$reviewed_head"
    printf 'current_head=%s\n' "$current_head"
    exit 1
  }

  reviewed_hash="$(env_get diff_hash || true)"
  current_hash="$(current_diff_hash_for_mark)"
  [ "$current_hash" = "$reviewed_hash" ] || {
    printf 'marker_written=false\n'
    printf 'marker_reason=diff_changed_since_review\n'
    printf 'reviewed_diff_hash=%s\n' "$reviewed_hash"
    printf 'current_diff_hash=%s\n' "$current_hash"
    exit 1
  }

  marker="$(marker_path)"
  marker_meta="$(marker_meta_path)"
  printf '%s\n' "$current_head" >"$marker" || mark_reject "marker_write_failed"
  {
    printf 'post_work_review_version=%s\n' "$VERSION"
    printf 'marked_at=%s\n' "$(now_utc)"
    printf 'backend=%s\n' "$BACKEND"
    printf 'head=%s\n' "$current_head"
    printf 'scope=%s\n' "$scope"
    printf 'base=%s\n' "$(env_get base)"
    printf 'diff_hash=%s\n' "$current_hash"
    printf 'broad_review_calls=%s\n' "$SUMMARY_BROAD_CALLS"
    printf 'verify_review_calls=%s\n' "$SUMMARY_VERIFY_CALLS"
    printf 'total_reviewer_calls=%s\n' "$SUMMARY_TOTAL_CALLS"
    printf 'clean=true\n'
    printf 'stop_reason=\n'
    printf 'findings_tsv=%s\n' "$(env_get findings_tsv)"
  } >"$marker_meta" || {
    rm -f "$marker"
    mark_reject "marker_write_failed"
  }

  printf 'marker_written=true\n'
  printf 'marker=%s\n' "$(display_path "$marker")"
  printf 'marker_meta=%s\n' "$(display_path "$marker_meta")"
  printf 'head=%s\n' "$current_head"
}

cmd_reset() {
  ensure_repo
  cd "$(repo_root)" || die "failed to enter repo root"
  rm -rf "$(state_dir_abs)"
  rm -f "$(marker_path)" "$(marker_meta_path)"
  printf 'reset=true\n'
}

usage() {
  cat <<'EOF'
usage:
  bash codex/tools/post-work-review.sh prepare
  bash codex/tools/post-work-review.sh prepare-verify
  bash codex/tools/post-work-review.sh record broad <review-json-file>
  bash codex/tools/post-work-review.sh record verify <review-json-file>
  bash codex/tools/post-work-review.sh summarize
  bash codex/tools/post-work-review.sh status
  bash codex/tools/post-work-review.sh mark
  bash codex/tools/post-work-review.sh reset
EOF
}

case "${1:-}" in
  prepare)
    cmd_prepare
    ;;
  prepare-verify)
    cmd_prepare_verify
    ;;
  record)
    shift
    cmd_record "$@"
    ;;
  summarize)
    cmd_summarize
    ;;
  status)
    cmd_status
    ;;
  mark)
    cmd_mark
    ;;
  reset)
    cmd_reset
    ;;
  *)
    usage
    exit 2
    ;;
esac
