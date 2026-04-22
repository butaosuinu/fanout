# Shared bats helpers for fanout tests.
#
# Every `.bats` file should `load helpers` as its first non-comment line.
# This file provides:
#   * setup()     — PATH shim injection, deterministic env, FANOUT_BIN discovery
#   * teardown()  — clean up /tmp/fanout-* briefings so tests can't pollute each other
#   * run_fanout  — thin wrapper that always invokes the repo's ./fanout via bats `run`

# Repo root (one level above tests/bats/).
TESTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$TESTS_DIR/.." && pwd)"
FANOUT_BIN="$REPO_ROOT/fanout"
TEST_BIN_DIR="$TESTS_DIR/bin"

setup() {
  # Shim directory first on PATH so tests/bin/gh and tests/bin/tmux win over
  # the real binaries. jq / shellcheck / awk etc. stay on the system PATH —
  # we shim only what the scenarios need to control.
  export PATH="$TEST_BIN_DIR:$PATH"

  # Neutralize fanout's color output: `[[ "${TERM:-dumb}" != dumb ]]` at
  # fanout:122 short-circuits on TERM=dumb. Belt-and-suspenders with NO_COLOR
  # for future readers even though fanout doesn't consult it today.
  export TERM=dumb
  export NO_COLOR=1

  # Tier 1 tests don't touch dmux discovery, but we still want fanout's
  # agent auto-detect path to be dormant unless a test opts in.
  unset TMUX_PANE

  # Default-off force-missing switch for dummy shims (Phase 1). Tests that
  # want to simulate a missing dependency set this themselves, e.g.
  #   FANOUT_TEST_FORCE_MISSING=gh run_fanout 20
  unset FANOUT_TEST_FORCE_MISSING

  # Per-test scratch dir (auto-cleaned by bats via BATS_TEST_TMPDIR).
  export FANOUT_TEST_TMPDIR="$BATS_TEST_TMPDIR"
}

teardown() {
  # Briefings are written to /tmp with a hardcoded prefix (fanout:791).
  # Tier 1 tests don't reach the briefing path, but scrub defensively so a
  # future test (or a Tier 2 leakage) doesn't confuse the next run.
  rm -f /tmp/fanout-*.md 2>/dev/null || true
}

# Run the repo-local fanout binary under bats' `run`, folding stderr into
# stdout so $output sees both streams. fanout routes all errors (log_err,
# die, usage >&2) through stderr, and bats' plain `run` only captures
# stdout without --merged-stderr, which isn't portable to older bats
# releases on CI. Doing the 2>&1 redirect inside a bash -c keeps the
# helper compatible with bats-core 1.2+ (Ubuntu 22.04 apt ships 1.2.1).
run_fanout() {
  local cmd
  printf -v cmd '%q ' "$FANOUT_BIN" "$@"
  run bash -c "$cmd 2>&1"
}

# Simulate missing dependencies by rebuilding PATH to a curated directory
# that (a) does NOT expose the named commands and (b) does expose the
# utilities fanout itself needs to reach the prerequisite-check block
# (jq, awk, grep, ...). Call before `run_fanout` within a test.
#
# Usage:
#   force_missing gh
#   force_missing gh tmux
force_missing() {
  local tmpbin="$BATS_TEST_TMPDIR/bin-force-missing"
  mkdir -p "$tmpbin"

  # Build an exclusion set so lookups below are O(1)-ish.
  local missing_csv=",${*// /,},"

  # (1) Symlink every shim except the ones being removed.
  local shim base
  for shim in "$TEST_BIN_DIR"/*; do
    base="$(basename "$shim")"
    [[ "$missing_csv" == *",$base,"* ]] && continue
    ln -sf "$shim" "$tmpbin/$base"
  done

  # (2) Symlink the core POSIX / GNU utilities fanout invokes before the
  # prereq-check block finishes. Discovery uses the OUTER PATH (the one
  # setup() left in place), not the sanitized one we're about to export.
  # If a utility isn't found, we still try — fanout's `command -v` calls
  # will just report it missing, which is acceptable for the few tests
  # that intentionally exclude pgrep/jq.
  local u src
  for u in jq awk grep sort tr basename dirname tput head tail cat cut \
           wc find mktemp sed uname date readlink realpath env sh bash \
           pgrep ps chmod mkdir rm cp mv ln ls; do
    # Skip utilities the caller explicitly wants missing.
    [[ "$missing_csv" == *",$u,"* ]] && continue
    src="$(command -v "$u" 2>/dev/null || true)"
    [[ -n "$src" ]] && ln -sf "$src" "$tmpbin/$u"
  done

  export PATH="$tmpbin"
}
