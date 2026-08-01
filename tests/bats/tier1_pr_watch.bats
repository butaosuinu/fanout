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
  unset PR_WATCH_PROBE_COUNTS_STATUS PR_WATCH_PROBE_COMMENTS_STATUS
  unset PR_WATCH_PROBE_REVIEWS_STATUS PR_WATCH_PROBE_THREADS_STATUS
  unset PR_WATCH_PROBE_THREAD_COMMENTS_STATUS

  cat >"$BATS_TEST_TMPDIR/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$PR_WATCH_FIXTURE/calls.log"
case "${1:-} ${2:-}" in
  "pr view")
    cat "$PR_WATCH_FIXTURE/pr.tsv"
    if [ -f "$PR_WATCH_FIXTURE/pr.next.tsv" ]; then
      mv "$PR_WATCH_FIXTURE/pr.next.tsv" "$PR_WATCH_FIXTURE/pr.tsv"
    fi
    exit "${PR_WATCH_PR_STATUS:-0}"
    ;;
  "pr checks")
    case " $* " in
      *" --required "*) ;;
      *)
        printf 'pr checks must use --required: %s\n' "$*" >&2
        exit 2
        ;;
    esac
    cat "$PR_WATCH_FIXTURE/checks.tsv"
    [ -z "${PR_WATCH_CHECKS_ERROR:-}" ] || printf '%s\n' "$PR_WATCH_CHECKS_ERROR" >&2
    exit "${PR_WATCH_CHECKS_STATUS:-0}"
    ;;
  "api graphql")
    case "$*" in
      *"headRefOid"*)
        cat "$PR_WATCH_FIXTURE/probe-counts.tsv"
        if [ -f "$PR_WATCH_FIXTURE/probe-counts.next.tsv" ]; then
          mv "$PR_WATCH_FIXTURE/probe-counts.next.tsv" "$PR_WATCH_FIXTURE/probe-counts.tsv"
        fi
        exit "${PR_WATCH_PROBE_COUNTS_STATUS:-0}"
        ;;
      *'node(id:$threadId)'*)
        thread_id=""
        for arg in "$@"; do
          case "$arg" in threadId=*) thread_id="${arg#threadId=}" ;; esac
        done
        [ -n "$thread_id" ] || exit 2
        cat "$PR_WATCH_FIXTURE/probe-thread-comments-$thread_id.tsv"
        exit "${PR_WATCH_PROBE_THREAD_COMMENTS_STATUS:-0}"
        ;;
      *"comments(first:100,after:"*)
        cat "$PR_WATCH_FIXTURE/probe-comments.tsv"
        exit "${PR_WATCH_PROBE_COMMENTS_STATUS:-0}"
        ;;
      *"latestReviews(first:100,after:"*)
        cat "$PR_WATCH_FIXTURE/probe-reviews.tsv"
        exit "${PR_WATCH_PROBE_REVIEWS_STATUS:-0}"
        ;;
      *"reviewThreads(first:100,after:"*)
        cat "$PR_WATCH_FIXTURE/probe-threads.tsv"
        exit "${PR_WATCH_PROBE_THREADS_STATUS:-0}"
        ;;
    esac
    printf 'unexpected graphql call: %s\n' "$*" >&2
    exit 2
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
  : >"$PR_WATCH_FIXTURE/calls.log"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  : >"$PR_WATCH_FIXTURE/probe-comments.tsv"
  : >"$PR_WATCH_FIXTURE/probe-reviews.tsv"
  : >"$PR_WATCH_FIXTURE/probe-threads.tsv"
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
  [[ "$output" == *"checks_reported=false"* ]] || return 1
  [[ "$output" == *"checks_pass=0"* ]]
}

@test "an empty required-check set is known and does not become pending" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  export PR_WATCH_CHECKS_STATUS=1
  export PR_WATCH_CHECKS_ERROR="no required checks reported on the branch"

  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"checks_reported=true"* ]] || return 1
  [[ "$output" == *"checks_pass=0"* ]] || return 1
  [[ "$output" == *"checks_pending=0"* ]] || return 1
  [[ "$output" == *"checks_fail=0"* ]]
}

@test "wait accepts a known empty required-check set when merge state is ready" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  export PR_WATCH_CHECKS_STATUS=1
  export PR_WATCH_CHECKS_ERROR="no required checks reported on the branch"

  run_watch "$repo" wait --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"merge_state=CLEAN"* ]] || return 1
  [[ "$output" == *"checks_reported=true"* ]] || return 1
  [[ "$output" == *"checks_pending=0"* ]] || return 1
  [[ "$output" == *"checks_fail=0"* ]]
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

@test "snapshot emits an updatedAt-only change" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"

  run_watch "$repo" snapshot --repo acme/widget --pr 27
  [ "$status" -eq 0 ]

  export PR_WATCH_CONTINUE=1
  write_pr OPEN false MERGEABLE CLEAN NONE 0 head-one 2026-07-10T00:01:00Z
  run_watch "$repo" snapshot --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=change* ]]
  [[ "$output" == *"head=head-one"* ]]
  [[ "$output" == *"updated=2026-07-10T00:01:00Z"* ]]
}

@test "review probe skips metadata pagination when every review surface is empty" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local state_dir
  setup_repo "$repo"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=review_probe* ]]
  [[ "$output" == *"top_comments=0"* ]]
  [[ "$output" == *"reviews=0"* ]]
  [[ "$output" == *"unresolved_threads=0"* ]]
  [ "$(grep -c '^api graphql' "$PR_WATCH_FIXTURE/calls.log")" -eq 2 ]
  ! grep -q 'first:100' "$PR_WATCH_FIXTURE/calls.log"
  ! grep -Eq 'body(Text)?|diffHunk' "$PR_WATCH_FIXTURE/calls.log"
  state_dir="$(state_dir_for "$repo")"
  [ ! -e "$state_dir" ]
}

@test "review probe emits body-free digests only for non-empty review surfaces" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t1\t1\t2\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\t101\talice\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n' \
    >"$PR_WATCH_FIXTURE/probe-comments.tsv"
  printf 'total\t1\nnode\t201\treviewer\tCOMMENTED\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\thead-one\n' \
    >"$PR_WATCH_FIXTURE/probe-reviews.tsv"
  {
    printf 'total\t2\n'
    printf 'node\tTHREAD-OPEN\tfalse\tmain.go\t12\t10\tRIGHT\t2\n'
    printf 'node\tTHREAD-DONE\ttrue\tmain.go\t8\t8\tRIGHT\t1\n'
  } >"$PR_WATCH_FIXTURE/probe-threads.tsv"
  {
    printf 'total\tTHREAD-OPEN\t2\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\n'
    printf 'node\tTHREAD-OPEN\t302\tagent\t2026-07-10T00:03:00Z\t2026-07-10T00:04:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=review_probe* ]]
  [[ "$output" == *"top_comments=1"* ]]
  [[ "$output" == *"reviews=1"* ]]
  [[ "$output" == *"unresolved_threads=1"* ]]
  [[ "$output" == *"top_digest="* ]]
  [[ "$output" == *"reviews_digest="* ]]
  [[ "$output" == *"threads_digest="* ]]
  [[ "$output" == *"fingerprint="* ]]
  [ "$(grep -c '^api graphql' "$PR_WATCH_FIXTURE/calls.log")" -eq 6 ]
  ! grep -Eq 'body(Text)?|diffHunk' "$PR_WATCH_FIXTURE/calls.log"
}

@test "review probe digest is stable across pagination and response order" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local first_digest second_digest i
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t101\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  {
    printf 'total\t101\n'
    i=1
    while [ "$i" -le 100 ]; do
      printf 'node\t%s\tuser-%s\t2026-07-10T00:00:00Z\t2026-07-10T00:00:00Z\n' "$i" "$i"
      i=$((i + 1))
    done
    printf 'total\t101\n'
    printf 'node\t101\tuser-101\t2026-07-10T00:00:00Z\t2026-07-10T00:00:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-comments.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  first_digest="${output#*top_digest=}"
  first_digest="${first_digest%% *}"

  {
    printf 'total\t101\n'
    i=101
    while [ "$i" -ge 2 ]; do
      printf 'node\t%s\tuser-%s\t2026-07-10T00:00:00Z\t2026-07-10T00:00:00Z\n' "$i" "$i"
      i=$((i - 1))
    done
    printf 'total\t101\n'
    printf 'node\t1\tuser-1\t2026-07-10T00:00:00Z\t2026-07-10T00:00:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-comments.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  second_digest="${output#*top_digest=}"
  second_digest="${second_digest%% *}"
  [ "$first_digest" = "$second_digest" ]
}

@test "review probe detects an edited top-level thread comment after a reply" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local first_digest second_digest
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t0\t1\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  {
    printf 'total\t1\n'
    printf 'node\tTHREAD-OPEN\tfalse\tmain.go\t12\t10\tRIGHT\t2\n'
  } >"$PR_WATCH_FIXTURE/probe-threads.tsv"
  {
    printf 'total\tTHREAD-OPEN\t2\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n'
    printf 'node\tTHREAD-OPEN\t302\tagent\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  first_digest="${output#*threads_digest=}"
  first_digest="${first_digest%% *}"

  {
    printf 'total\tTHREAD-OPEN\t2\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:01:00Z\t2026-07-10T00:03:00Z\n'
    printf 'node\tTHREAD-OPEN\t302\tagent\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  second_digest="${output#*threads_digest=}"
  second_digest="${second_digest%% *}"
  [ "$first_digest" != "$second_digest" ]
}

@test "review probe detects an edited middle thread comment" {
  local repo="$BATS_TEST_TMPDIR/repo"
  local first_digest second_digest
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t0\t1\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\tTHREAD-OPEN\tfalse\tmain.go\t12\t10\tRIGHT\t3\n' \
    >"$PR_WATCH_FIXTURE/probe-threads.tsv"
  {
    printf 'total\tTHREAD-OPEN\t3\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n'
    printf 'node\tTHREAD-OPEN\t302\tagent\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\n'
    printf 'node\tTHREAD-OPEN\t303\treviewer\t2026-07-10T00:03:00Z\t2026-07-10T00:03:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  first_digest="${output#*threads_digest=}"
  first_digest="${first_digest%% *}"

  {
    printf 'total\tTHREAD-OPEN\t3\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n'
    printf 'node\tTHREAD-OPEN\t302\tagent\t2026-07-10T00:02:00Z\t2026-07-10T00:04:00Z\n'
    printf 'node\tTHREAD-OPEN\t303\treviewer\t2026-07-10T00:03:00Z\t2026-07-10T00:03:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27
  [ "$status" -eq 0 ]
  second_digest="${output#*threads_digest=}"
  second_digest="${second_digest%% *}"
  [ "$first_digest" != "$second_digest" ]
}

@test "review probe blocks on incomplete metadata pagination" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t2\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t2\nnode\t101\talice\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n' \
    >"$PR_WATCH_FIXTURE/probe-comments.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_incomplete status=1" ]]
}

@test "review probe blocks on empty or duplicate metadata node IDs" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t1\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\t\t\t\t\n' >"$PR_WATCH_FIXTURE/probe-comments.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_incomplete status=1" ]]

  printf 'head-one\t2026-07-10T00:00:00Z\t0\t2\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  {
    printf 'total\t2\n'
    printf 'node\t201\treviewer\tCOMMENTED\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\thead-one\n'
    printf 'node\t201\treviewer\tCOMMENTED\t2026-07-10T00:02:00Z\t2026-07-10T00:02:00Z\thead-one\n'
  } >"$PR_WATCH_FIXTURE/probe-reviews.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_incomplete status=1" ]]
}

@test "review probe blocks on missing required metadata fields" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t1\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\t101\talice\t\t2026-07-10T00:01:00Z\n' \
    >"$PR_WATCH_FIXTURE/probe-comments.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_incomplete status=1" ]]
}

@test "review probe allows null submittedAt only for pending reviews" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t1\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\t201\treviewer\tPENDING\t\t2026-07-10T00:01:00Z\thead-one\n' \
    >"$PR_WATCH_FIXTURE/probe-reviews.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == event=review_probe* ]]
  [[ "$output" == *"reviews=1"* ]]

  printf 'total\t1\nnode\t201\treviewer\tCOMMENTED\t\t2026-07-10T00:01:00Z\thead-one\n' \
    >"$PR_WATCH_FIXTURE/probe-reviews.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_incomplete status=1" ]]
}

@test "review probe reports an inconsistent all-thread count" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t0\t2\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\tTHREAD-DONE\ttrue\tmain.go\t8\t8\tRIGHT\t1\n' \
    >"$PR_WATCH_FIXTURE/probe-threads.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == "event=change repo=acme/widget pr=27 reason=review_probe_changed" ]]
}

@test "review probe blocks on incomplete thread-comment pagination" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-one\t2026-07-10T00:00:00Z\t0\t0\t1\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.tsv"
  printf 'total\t1\nnode\tTHREAD-OPEN\tfalse\tmain.go\t12\t10\tRIGHT\t2\n' \
    >"$PR_WATCH_FIXTURE/probe-threads.tsv"
  {
    printf 'total\tTHREAD-OPEN\t2\n'
    printf 'node\tTHREAD-OPEN\t301\treviewer\t2026-07-10T00:01:00Z\t2026-07-10T00:01:00Z\n'
  } >"$PR_WATCH_FIXTURE/probe-thread-comments-THREAD-OPEN.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 1 ]
  [[ "$output" == "event=blocked repo=acme/widget pr=27 reason=review_probe_thread_comments_incomplete status=1" ]]
}

@test "review probe reports a race instead of classifying stale counts" {
  local repo="$BATS_TEST_TMPDIR/repo"
  setup_repo "$repo"
  printf 'head-two\t2026-07-10T00:01:00Z\t1\t0\t0\n' \
    >"$PR_WATCH_FIXTURE/probe-counts.next.tsv"

  run_watch "$repo" review-probe --repo acme/widget --pr 27

  [ "$status" -eq 0 ]
  [[ "$output" == "event=change repo=acme/widget pr=27 reason=review_probe_changed" ]]
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
  [[ "$output" == *"checks_reported=true"* ]] || return 1
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
