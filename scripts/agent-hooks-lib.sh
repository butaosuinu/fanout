# shellcheck shell=bash
# fanout agent hooks — shared helpers for the repo-local Claude Code / Codex
# hook scripts (scripts/agent-*.sh). Sourced, not executed.
#
# Pure bash + git + POSIX awk/grep/sed on purpose: no python3 / node / jq, so
# the hooks run on minimal hosts. The hook payload key names are identical for
# Claude Code and Codex (verified against Codex CLI 0.144.0: tool_name "Bash",
# tool_input.command, cwd, stop_hook_active), so one set of helpers serves
# both agents.

# json_field JSON NAME — print the first "NAME":"..." string value with JSON
# backslash escapes decoded. Handles double-quoted string values only, which
# covers every field the hooks read (command / cwd / file_path). First match
# wins: real keys precede embedded escaped copies (e.g. inside tool_response,
# where quotes arrive as \" and therefore never match the unescaped pattern).
json_field() {
  awk -v name="$2" '
    { buf = buf $0 "\n" }
    END {
      pat = "\"" name "\"[[:space:]]*:[[:space:]]*\""
      if (!match(buf, pat)) exit
      rest = substr(buf, RSTART + RLENGTH)
      out = ""
      n = length(rest)
      for (i = 1; i <= n; i++) {
        c = substr(rest, i, 1)
        if (c == "\\") {
          i++
          e = substr(rest, i, 1)
          if (e == "n") out = out "\n"
          else if (e == "t") out = out "\t"
          else if (e == "r") out = out "\r"
          else if (e == "b" || e == "f") out = out " "
          else if (e == "u") i += 4
          else out = out e
        } else if (c == "\"") break
        else out = out c
      }
      printf "%s", out
    }' <<<"$1"
}

# resolve_project_dir JSON — the directory the gated command runs in:
# CLAUDE_PROJECT_DIR (Claude sets it), else the payload cwd, else $PWD.
resolve_project_dir() {
  local dir="${CLAUDE_PROJECT_DIR:-}"
  if [ -z "$dir" ]; then
    dir="$(json_field "$1" cwd)"
  fi
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    dir="$PWD"
  fi
  printf '%s' "$dir"
}

# marker_path DIR — the per-worktree `make check` marker. `git rev-parse
# --git-dir` resolves to .git/worktrees/<name> inside a linked worktree, so
# parallel fanout panes never share a marker.
marker_path() {
  local gitdir
  gitdir="$(git -C "$1" rev-parse --git-dir 2>/dev/null)" || return 1
  case "$gitdir" in
    /*) printf '%s/fanout-check-passed' "$gitdir" ;;
    *) printf '%s/%s/fanout-check-passed' "$1" "$gitdir" ;;
  esac
}

# marker_sha DIR — the recorded validated commit, empty when absent.
marker_sha() {
  local marker
  marker="$(marker_path "$1")" || return 0
  [ -f "$marker" ] || return 0
  tr -d '[:space:]' <"$marker"
}

head_sha() {
  git -C "$1" rev-parse HEAD 2>/dev/null
}
