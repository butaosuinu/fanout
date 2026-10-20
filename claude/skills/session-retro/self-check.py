"""Run Step 2 against owned synthetic files only: python3 <this-file>."""

import datetime
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


step = Path(__file__).with_name("SKILL.md").read_text().split("## Step 2:")[1]
step = step.split("## Step 3:")[0]
blocks = re.findall(r"```bash\n(.*?)```", step, re.S)
assert len(blocks) == 3, "Step 2 candidate/filter/count snippets changed"
snippet = "\n".join(blocks)
since = "2026-10-01T00:00:00Z"
until = "2026-10-02T00:00:00Z"
since_epoch = int(datetime.datetime.fromisoformat(since.replace("Z", "+00:00")).timestamp())
until_epoch = int(datetime.datetime.fromisoformat(until.replace("Z", "+00:00")).timestamp())


def write_log(synthetic_root, relative, timestamp=until, count=1, mtime=until_epoch):
    path = synthetic_root / "claude" / "projects" / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    row = {"timestamp": timestamp, "message": {"content": [{"is_error": True}] * count}}
    path.write_text(json.dumps(row, separators=(",", ":")) + "\n")
    os.utime(path, (mtime, mtime))
    return path


def check(synthetic_root, expected, session="self-session", rg_error=False, calls=1):
    tmpdir = synthetic_root / "tmp"
    tmpdir.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        ["bash", "-e", "-c", r'''
synthetic_root=$1
claude_home="$synthetic_root/claude"
tmpdir="$synthetic_root/tmp"
slug=-fixture
SINCE=$2
UNTIL=$3
CLAUDE_CODE_SESSION_ID=$4
rg_error=$5
: > "$tmpdir/rg-calls"
: > "$tmpdir/rg-args"
rg() {
  printf 'call\n' >> "$tmpdir/rg-calls"
  printf '%s\n' "$@" > "$tmpdir/rg-args"
  if [ "$rg_error" = True ]; then return 2; fi
  command rg "$@"
}
''' + snippet, "self-check", str(synthetic_root), since, until, session, str(rg_error)],
        cwd=synthetic_root, capture_output=True, text=True, check=True,
    )
    assert result.stdout.strip() == (
        f"tool_errors.total={expected} tool_errors_truncated={str(rg_error).lower()}"
    ), result
    assert (tmpdir / "rg-calls").read_text().splitlines() == ["call"] * calls
    candidates = (tmpdir / "candidates").read_text().splitlines()
    assert len(candidates) == len(set(candidates)), "duplicate candidate paths"
    args = (tmpdir / "rg-args").read_text().splitlines()
    if calls:
        files = args[args.index("--") + 1:]
        assert files and len(files) == len(set(files))
        assert all(Path(f).is_relative_to(synthetic_root / "claude/projects") for f in files)
        assert not session or all(session not in f for f in files)
    assert ("警告:" in result.stderr) == rg_error, result.stderr


with tempfile.TemporaryDirectory(prefix="session-retro-393-", dir="/tmp") as directory:
    synthetic_root = Path(directory)
    mixed = synthetic_root / "mixed with spaces"
    layouts = ["parent.jsonl", "parent/subagents/agent-direct.jsonl",
               "parent/subagents/workflows/run-1/agent-workflow.jsonl"]
    projects = ["-fixture", "-fixture--dmux-worktrees-task", "-fixture--fanout-worktrees-task"]
    for project in projects:
        for layout in layouts:
            write_log(mixed, f"{project}/{layout}", count=2)
            write_log(mixed, f"{project}/{layout.replace('parent', 'self-session')}", count=99)
            write_log(mixed, f"{project}/{layout.replace('parent', 'stale')}", mtime=since_epoch - 1)
    for project in ["-fixture2", "-fixture--other-worktrees-task", "-fixture-cmd-fanout"]:
        for layout in layouts:
            write_log(mixed, f"{project}/{layout}", count=99)
    for layout in ["parent/elsewhere/agent-no.jsonl", "parent/subagents/other.jsonl",
                   "parent/subagents/workflows/run-1/deeper/agent-no.jsonl"]:
        write_log(mixed, f"-fixture/{layout}", count=99)
    write_log(mixed, "-fixture/old-parent.jsonl", mtime=since_epoch - 1)
    write_log(mixed, "-fixture/old-parent/subagents/agent-direct.jsonl")
    write_log(mixed, "-fixture/old-parent/subagents/workflows/run-1/agent-workflow.jsonl")
    write_log(mixed, "-fixture/no-parent/subagents/agent-orphan.jsonl")
    write_log(mixed, "-fixture/no-parent/subagents/workflows/run-1/agent-orphan.jsonl")
    check(mixed, 22)
    check(mixed, 22)  # Reusing candidates must not append a second copy.
    print("PASS: project/layout boundaries, own session, old/absent parent, dedup, spaces, one rg")

    boundary = synthetic_root / "boundary"
    for index, timestamp in enumerate([since, "2026-09-30T23:59:59Z", until,
                                       "2026-10-02T00:00:01Z", "2026-10-01T12:00:00.123Z"]):
        write_log(boundary, f"-fixture/p/subagents/workflows/run/agent-{index}.jsonl",
                  timestamp=timestamp, mtime=until_epoch + 10)
    write_log(boundary, "-fixture/p/subagents/agent-mtime-equal.jsonl", mtime=since_epoch)
    check(boundary, 3, session="")
    print("PASS: mtime lower bound only, (SINCE, UNTIL], milliseconds, empty session ID")

    empty = synthetic_root / "empty"
    check(empty, 0, calls=0)
    write_log(empty, "-fixture/self-session/subagents/workflows/run/agent-self.jsonl")
    check(empty, 0, calls=0)
    print("PASS: no candidates and self-only candidates skip rg")

    no_match = synthetic_root / "no-match"
    write_log(no_match, "-fixture/p/subagents/agent-ok.jsonl", count=0)
    check(no_match, 0)
    check(no_match, 0, rg_error=True)
    print("PASS: rg no-match versus runtime error/truncated")
