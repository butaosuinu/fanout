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
# \uXXXX escapes decode to a "_" placeholder: the character identity is
# irrelevant to the gates, but dropping it entirely could merge adjacent
# tokens into new command words.
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
          else if (e == "u") { i += 4; out = out "_" }
          else out = out e
        } else if (c == "\"") break
        else out = out c
      }
      printf "%s", out
    }' <<<"$1"
}

# resolve_project_dir JSON — the directory the gated command runs in. The
# payload cwd wins: it is where the tool call actually executes. The session
# env CLAUDE_PROJECT_DIR is only a fallback — a session rooted in repo A can
# run commands inside repo B (fanout worktrees), and gating A while pushing B
# checks the wrong marker.
resolve_project_dir() {
  local dir
  dir="$(json_field "$1" cwd)"
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    dir="${CLAUDE_PROJECT_DIR:-}"
  fi
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    dir="$PWD"
  fi
  printf '%s' "$dir"
}

# strip_shell_noise CMD — normalize a shell command string for word scanning:
# single-/double-quoted spans become spaces (double quotes honor backslash
# escapes, so an apostrophe inside "..." cannot mis-pair), heredoc bodies are
# dropped up to their terminator, backslash escapes outside quotes become
# spaces, and command separators (; | & ( ) { } ` and newlines) become
# newlines — one simple command per output line. This is a gate heuristic,
# not a shell: unknown constructs must degrade toward keeping words visible
# (fail closed), never toward hiding an executable `git push`.
strip_shell_noise() {
  awk '
    BEGIN { hd = 0; hdword = "" }
    {
      line = $0
      if (hd) {
        stripped = line
        sub(/^\t+/, "", stripped)
        if (stripped == hdword) hd = 0
        next
      }
      out = ""
      n = length(line)
      i = 1
      sq = 0
      dq = 0
      while (i <= n) {
        c = substr(line, i, 1)
        if (sq) {
          if (c == "\047") sq = 0
          i++
          continue
        }
        if (dq) {
          if (c == "\\") { i += 2; continue }
          if (c == "\"") dq = 0
          i++
          continue
        }
        if (c == "\047") { sq = 1; out = out " "; i++; continue }
        if (c == "\"") { dq = 1; out = out " "; i++; continue }
        if (c == "\\") { out = out " "; i += 2; continue }
        if (c == "<" && substr(line, i + 1, 1) == "<" && substr(line, i + 2, 1) != "<") {
          j = i + 2
          if (substr(line, j, 1) == "-") j++
          if (substr(line, j, 1) == "\047" || substr(line, j, 1) == "\"") j++
          w = ""
          while (j <= n && substr(line, j, 1) ~ /[A-Za-z0-9_]/) {
            w = w substr(line, j, 1)
            j++
          }
          if (w != "") { hd = 1; hdword = w; out = out " "; i = j; continue }
          out = out c
          i++
          continue
        }
        if (c ~ /[;|&(){}`]/) { out = out "\n"; i++; continue }
        out = out c
        i++
      }
      print out
    }
  ' <<<"$1"
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
