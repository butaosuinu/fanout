#!/usr/bin/env bash
#
# seed-dashboard-fixture.sh — build a throwaway local environment that
# exercises every fanout dashboard feature in a real browser.
#
# Usage:
#   hack/seed-dashboard-fixture.sh [TARGET_DIR]
#
# TARGET_DIR defaults to /tmp/fanout-dashboard-fixture. The directory is
# recreated from scratch on every run. As a safety check the script only
# deletes a pre-existing TARGET_DIR when it contains the fixture marker file
# (.fanout-fixture-marker); anything else is refused.
#
# What it builds:
#   - a scratch git repo whose `origin` points at github.com/butaosuinu/fanout,
#     so the dashboard's gh enrichment (issue state / PRs / CI / waves)
#     returns live data for the real issue numbers referenced below;
#   - four git worktrees under .fanout/worktrees/: one ahead of base by a
#     commit, one dirty with uncommitted changes (+N/-M), two clean;
#   - a detached tmux session `fanout-fixture` with three panes, each cd'd
#     into its worktree (sessionview counts a pane alive only when its tmux
#     pane id exists AND its current path is at/under the recorded
#     worktreePath) and kept alive via `exec sleep 100000`;
#   - .fanout/state.json rows covering: live panes, a dead pane (%999 →
#     stale + peek unavailable), XSS probes in displayName/prompt, a broken
#     worktree (worktreeErr), wave labels, and two parents — #142 (closed-out
#     tree with wave1..wave5 task-list structure) and #211 (open tree with
#     open children and blockers).
#
# Run the dashboard FROM the fixture repo afterwards:
#   (cd TARGET_DIR && /path/to/fanout-go dashboard --web --open)
#
# Teardown:
#   tmux kill-session -t fanout-fixture; rm -rf TARGET_DIR
#
# Requires: bash, git, tmux. gh (authenticated) is only needed later, by the
# dashboard itself. Never point TARGET_DIR at a real checkout: the script
# refuses the fanout repo, $HOME, and /.

set -euo pipefail

SESSION=fanout-fixture
MARKER=.fanout-fixture-marker
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(dirname -- "$SCRIPT_DIR")

die() {
	echo "seed-dashboard-fixture: error: $*" >&2
	exit 1
}

usage() {
	sed -n '2,38p' "${BASH_SOURCE[0]}" | sed -e 's/^# \{0,1\}//'
}

# json_str escapes backslashes and double quotes for safe embedding in JSON.
# Paths with control characters are out of scope for a fixture script.
json_str() {
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	printf '%s' "$s"
}

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
esac

TARGET_DIR=${1:-/tmp/fanout-dashboard-fixture}
TARGET_DIR=${TARGET_DIR%/}
case "$TARGET_DIR" in
"") die "TARGET_DIR must not be empty or /" ;;
/*) ;;
*) TARGET_DIR="$PWD/$TARGET_DIR" ;;
esac

command -v git >/dev/null 2>&1 || die "git is required"
command -v tmux >/dev/null 2>&1 || die "tmux is required"

# Safety: never operate on the development repo, $HOME, or anything that does
# not look like a previous fixture.
[ "$TARGET_DIR" = "$HOME" ] && die "refusing to use \$HOME as TARGET_DIR"
case "$TARGET_DIR" in
"$REPO_ROOT" | "$REPO_ROOT"/*) die "refusing TARGET_DIR inside the fanout repo: $TARGET_DIR" ;;
esac
if [ -e "$TARGET_DIR" ] && [ ! -f "$TARGET_DIR/$MARKER" ]; then
	die "$TARGET_DIR exists but has no $MARKER marker; refusing to delete it"
fi

# Idempotency: drop a previous fixture session and tree before rebuilding.
tmux kill-session -t "$SESSION" 2>/dev/null || true
rm -rf "$TARGET_DIR"
mkdir -p "$TARGET_DIR"
# Canonicalize to the physical path: tmux reports #{pane_current_path}
# symlink-resolved (/tmp -> /private/tmp on macOS) and sessionview's paneAlive
# does a literal prefix match against the recorded worktreePath, so state.json
# must record physical paths or every pane would read as dead.
TARGET_DIR=$(cd -- "$TARGET_DIR" && pwd -P)

echo "==> scratch repo: $TARGET_DIR"
git -C "$TARGET_DIR" init -q -b main
git -C "$TARGET_DIR" config user.name "fanout fixture"
git -C "$TARGET_DIR" config user.email "fixture@example.invalid"
printf 'line one\nline two\nline three\n' >"$TARGET_DIR/README.md"
printf 'alpha\nbeta\ngamma\n' >"$TARGET_DIR/notes.md"
: >"$TARGET_DIR/$MARKER"
git -C "$TARGET_DIR" add README.md notes.md "$MARKER"
git -C "$TARGET_DIR" commit -q -m "fixture: base commit"
# Pointing origin at the real repo makes `gh repo view` (and therefore all
# issue/PR/CI/wave enrichment) resolve to github.com/butaosuinu/fanout.
git -C "$TARGET_DIR" remote add origin https://github.com/butaosuinu/fanout.git

echo "==> worktrees under .fanout/worktrees/"
add_worktree() {
	local slug=$1 branch=$2
	git -C "$TARGET_DIR" worktree add -q -b "$branch" ".fanout/worktrees/$slug" main
}
add_worktree sessionview-135 feat-fixture-135
add_worktree createpane-136 feat-fixture-136
add_worktree dashboard-118 feat-fixture-118
add_worktree planspec-212 feat-fixture-212
WT135="$TARGET_DIR/.fanout/worktrees/sessionview-135"
WT136="$TARGET_DIR/.fanout/worktrees/createpane-136"
WT118="$TARGET_DIR/.fanout/worktrees/dashboard-118"
WT212="$TARGET_DIR/.fanout/worktrees/planspec-212"

# #135: a committed change vs base. The dashboard's diffSummary diffs against
# HEAD (not base), so this row demonstrates that committed work reads as
# clean +0/-0 while still being ahead of main.
printf 'fixture notes for #135\n1\n2\n3\n4\n' >"$WT135/notes-135.md"
git -C "$WT135" add notes-135.md
git -C "$WT135" commit -q -m "fixture: committed change vs base (#135)"

# #136: dirty uncommitted changes → dirtyState=dirty and a non-zero
# diffSummary with both insertions and deletions (+2/-1).
echo "dirty uncommitted fixture line" >>"$WT136/README.md"
printf 'alpha\nBETA changed by fixture\ngamma\n' >"$WT136/notes.md"

# worktreeErr row: a directory that git refuses to treat as a repository. A
# plain subdirectory would silently resolve to the enclosing scratch repo, so
# give it a .git file pointing at a gitdir that does not exist.
BROKEN_DIR="$TARGET_DIR/broken-worktree"
mkdir -p "$BROKEN_DIR"
printf 'gitdir: %s\n' "$BROKEN_DIR/missing-gitdir" >"$BROKEN_DIR/.git"

echo "==> tmux session: $SESSION"
# Each pane prints a fake agent banner plus a few lines (so /api/peek has
# content to capture) and then keeps the pane alive. `-P -F '#{pane_id}'`
# returns the real pane id synchronously.
pane_cmd() {
	local agent=$1 issue=$2 slug=$3
	printf "echo '[%s] fanout fixture agent - issue #%s (%s)'; seq 1 8; exec sleep 100000" \
		"$agent" "$issue" "$slug"
}
PANE_135=$(tmux new-session -d -P -F '#{pane_id}' -s "$SESSION" -x 220 -y 50 \
	-c "$WT135" "$(pane_cmd claude 135 sessionview-135)")
PANE_136=$(tmux split-window -d -P -F '#{pane_id}' -t "$SESSION" \
	-c "$WT136" "$(pane_cmd codex 136 createpane-136)")
PANE_212=$(tmux split-window -d -P -F '#{pane_id}' -t "$SESSION" \
	-c "$WT212" "$(pane_cmd claude 212 planspec-212)")
tmux select-layout -t "$PANE_135" tiled
# Real fanout titles panes with the slug (cmd/fanout/pane.go paneTitle).
tmux select-pane -t "$PANE_135" -T sessionview-135
tmux select-pane -t "$PANE_136" -T createpane-136
tmux select-pane -t "$PANE_212" -T planspec-212

echo "==> .fanout/state.json"
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
J_WT135=$(json_str "$WT135")
J_WT136=$(json_str "$WT136")
J_WT118=$(json_str "$WT118")
J_WT212=$(json_str "$WT212")
J_BROKEN=$(json_str "$BROKEN_DIR")

# Fixture rows against real butaosuinu/fanout issues:
#   142/135  CLOSED issue, MERGED PR (#159), live pane, ahead-of-base worktree
#   142/136  CLOSED issue, MERGED PR, live pane, dirty worktree (+2/-1)
#   142/118  CLOSED issue, dead pane %999 → stale + peek unavailable; XSS
#            probes in displayName and prompt (must render inert)
#   142/117  CLOSED issue, dead pane %998, broken worktree → worktreeErr
#   211/212  OPEN issue in an open wave/blocker tree, live pane
STATE_TMP="$TARGET_DIR/.fanout/state.json.tmp.$$"
cat >"$STATE_TMP" <<EOF
{
  "schemaVersion": 1,
  "panes": [
    {
      "parent": "142",
      "issueNum": 135,
      "slug": "sessionview-135",
      "branchName": "feat-fixture-135",
      "paneId": "$PANE_135",
      "agent": "claude",
      "wave": "wave1",
      "displayName": "sessionview-135",
      "worktreePath": "$J_WT135",
      "prompt": "Fixture briefing for #135: extend internal/tmuxrun with pane list / capture / focus.",
      "createdAt": "$NOW"
    },
    {
      "parent": "142",
      "issueNum": 136,
      "slug": "createpane-136",
      "branchName": "feat-fixture-136",
      "paneId": "$PANE_136",
      "agent": "codex",
      "wave": "wave1",
      "displayName": "createPane 分解",
      "worktreePath": "$J_WT136",
      "prompt": "Fixture briefing for #136: split createPaneForIssue into a generic createPane.",
      "createdAt": "$NOW"
    },
    {
      "parent": "142",
      "issueNum": 118,
      "slug": "dashboard-118",
      "branchName": "feat-fixture-118",
      "paneId": "%999",
      "agent": "claude",
      "wave": "wave2",
      "displayName": "<script>alert(1)</script>",
      "worktreePath": "$J_WT118",
      "prompt": "<img src=x onerror=alert(1)> <script>alert(1)</script> XSS probe: must render as inert text.",
      "createdAt": "$NOW"
    },
    {
      "parent": "142",
      "issueNum": 117,
      "slug": "broken-117",
      "branchName": "feat-fixture-117",
      "paneId": "%998",
      "agent": "claude",
      "displayName": "broken worktree fixture",
      "worktreePath": "$J_BROKEN",
      "prompt": "Fixture briefing for #117: worktreePath points at a directory git rejects (worktreeErr).",
      "createdAt": "$NOW"
    },
    {
      "parent": "211",
      "issueNum": 212,
      "slug": "planspec-212",
      "branchName": "feat-fixture-212",
      "paneId": "$PANE_212",
      "agent": "claude",
      "wave": "wave1",
      "displayName": "planspec パッケージ",
      "worktreePath": "$J_WT212",
      "prompt": "Fixture briefing for #212: add the internal/planspec package defining the plan spec JSON.",
      "createdAt": "$NOW"
    }
  ]
}
EOF
mv "$STATE_TMP" "$TARGET_DIR/.fanout/state.json"

# Self-checks: the state file must parse and the tmux session must exist.
if command -v python3 >/dev/null 2>&1; then
	python3 -m json.tool <"$TARGET_DIR/.fanout/state.json" >/dev/null
fi
tmux has-session -t "$SESSION"

FANOUT_BIN="$REPO_ROOT/fanout-go"
echo
echo "fixture ready: $TARGET_DIR"
echo "tmux session:  $SESSION   (attach: tmux attach -t $SESSION)"
echo "panes:         135=$PANE_135 (live)  136=$PANE_136 (live)  212=$PANE_212 (live)"
echo "               118=%999 (dead -> stale, peek unavailable)  117=%998 (dead, worktreeErr)"
echo
echo "run the dashboard:"
echo "  (cd $TARGET_DIR && $FANOUT_BIN dashboard --web --open)"
if [ ! -x "$FANOUT_BIN" ]; then
	echo "  (fanout-go not built yet: run 'make -C $REPO_ROOT build-go' first)"
fi
echo
echo "teardown:"
echo "  tmux kill-session -t $SESSION; rm -rf $TARGET_DIR"
