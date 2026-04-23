# Shared bats helpers for fanout tests.
#
# Every `.bats` file should `load helpers` as its first non-comment line.
# This file provides:
#   * setup()          — PATH shim injection, deterministic env, FANOUT_BIN discovery
#   * teardown()       — clean up /tmp/fanout-* briefings so tests can't pollute each other
#   * run_fanout       — thin wrapper that always invokes the repo's ./fanout via bats `run`
#   * run_fanout_dry   — Tier 2 wrapper that adds --dry-run and --agent defaults
#   * assert_golden    — compare captured $output to tests/golden/<name>.dry-run.txt
#   * use_fixture      — point FIXTURE_DIR at tests/fixtures/<name> for shims

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

  # Tier 2 shims (tests/bin/gh, tests/bin/tmux) read FIXTURE_DIR to find
  # per-scenario fixture files. Tier 1 tests must NOT have it leaked across
  # tests — unset here and let each Tier 2 test opt in via use_fixture.
  unset FIXTURE_DIR

  # Supply the tmux shim with a PID that's alive for the full test run.
  # $$ inside bats is the bats process PID; it stays live across setup /
  # test body / teardown. The tmux shim substitutes @@PID@@ in fixture
  # values with this PID so fanout's kill -0 @dmux_controller_pid check
  # succeeds. See tests/bin/tmux for why shim-local $$ / $PPID don't work.
  export FANOUT_TEST_ALIVE_PID=$$

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

# --- Tier 2 helpers ---------------------------------------------------------

# Point the shims at tests/fixtures/<name>. Call once near the top of each
# Tier 2 @test, before run_fanout_dry.
use_fixture() {
  local name="$1"
  local dir="$TESTS_DIR/fixtures/$name"
  [[ -d "$dir" ]] || { echo "use_fixture: missing $dir" >&2; return 1; }
  export FIXTURE_DIR="$dir"
}

# Run fanout in --dry-run mode with a default --agent so the "WOULD FAIL:
# agent is empty" line (fanout:1001) never enters a golden file. Also pass
# --sleep 0 so the inter-issue rate-limit pause (default 4s, fanout:1155)
# doesn't slow multi-child scenarios. Individual tests can override any of
# these via argv because fanout's parser honors the last occurrence.
run_fanout_dry() {
  run_fanout --dry-run --agent claude --sleep 0 "$@"
}

# Scrub machine-local prefixes from captured output so goldens are portable
# across workstations and CI runners. Rewrites:
#   - $REPO_ROOT                       → <REPO>
# so "config: /Users/x/fanout/tests/fixtures/scenario-X/dmux.config.json"
# collapses to "config: <REPO>/tests/fixtures/scenario-X/dmux.config.json".
# The /tmp/fanout-<repo_slug>-<N>.md briefing path stays verbatim because
# repo_slug is deterministic (always "project_root" per fixture layout).
_scrub_output() {
  printf '%s' "$1" | sed -e "s|$REPO_ROOT|<REPO>|g"
}

# Diff $output against tests/golden/<name>.dry-run.txt.
#
# The golden file holds the merged (and path-scrubbed) stdout+stderr that
# run_fanout_dry saw. FANOUT_GOLDEN_UPDATE=1 rewrites the golden instead of
# diffing — use this whenever fanout's on-screen format changes intentionally.
# Without that env var, a mismatch surfaces as a unified diff in bats'
# failure output, which is what CI reports on regression.
assert_golden() {
  local name="$1"
  local golden="$TESTS_DIR/golden/$name.dry-run.txt"
  local scrubbed
  # $output is populated by bats' `run` (via run_fanout_dry) before this
  # helper is called, so the "unassigned" warning from shellcheck is a
  # false positive here.
  # shellcheck disable=SC2154
  scrubbed="$(_scrub_output "$output")"
  if [[ "${FANOUT_GOLDEN_UPDATE:-0}" == "1" ]]; then
    mkdir -p "$(dirname "$golden")"
    printf '%s\n' "$scrubbed" > "$golden"
    return 0
  fi
  if [[ ! -f "$golden" ]]; then
    echo "assert_golden: $golden does not exist. Rerun with FANOUT_GOLDEN_UPDATE=1 to create it." >&2
    return 1
  fi
  local actual="$BATS_TEST_TMPDIR/actual.txt"
  printf '%s\n' "$scrubbed" > "$actual"
  diff -u "$golden" "$actual"
}
