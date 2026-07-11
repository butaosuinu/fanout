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
# heredoc bodies are dropped up to their terminator and command separators
# (; | & ( ) { } ` and unquoted newlines) become newlines — one simple
# command per output line. Quoted spans (and backslash-escaped characters)
# are NOT dropped: their content is kept as part of the surrounding word,
# with embedded whitespace/newlines replaced by the \001 sentinel, so a
# quoted path stays one traceable token (`git -C "$repo" push` keeps its -C
# value) while quoted prose can never form new command words (`"git push"`
# stays one word). unsentinel() restores the spaces when a word is used as a
# path. This is a gate heuristic, not a shell: unknown constructs must
# degrade toward keeping words visible (fail closed), never toward hiding an
# executable `git push`.
strip_shell_noise() {
  awk '
    # subst_span: consume a $(…) / `…` span starting at `start`; queue the
    # raw inner text as its own segment (via the global `extra`) and return
    # the index just past the closer. The outer word gets a placeholder, so
    # a push whose path/refspec depends on a substitution fails closed.
    function subst_span(line, start, n, kind,    depth, k, ch, inner) {
      inner = ""
      if (kind == "`") {
        k = start
        while (k <= n && substr(line, k, 1) != "`") {
          inner = inner substr(line, k, 1)
          k++
        }
        extra = extra "\n" inner "\n"
        return k + 1
      }
      depth = 1
      k = start
      while (k <= n) {
        ch = substr(line, k, 1)
        if (ch == "(") depth++
        else if (ch == ")") {
          depth--
          if (depth == 0) break
        }
        inner = inner ch
        k++
      }
      extra = extra "\n" inner "\n"
      return k + 1
    }
    BEGIN {
      hd = 0; hdword = ""; hdq = 0
      sq = 0; dq = 0
      buf = ""; extra = ""; S = sprintf("%c", 1)
    }
    {
      line = $0
      prev = ""
      if (hd) {
        stripped = line
        sub(/^\t+/, "", stripped)
        if (stripped == hdword) { hd = 0; next }
        # Expansions still run inside an unquoted heredoc body: scan lines
        # that contain a substitution as code; drop everything else.
        if (hdq || (index(line, "$(") == 0 && index(line, "`") == 0)) next
      }
      n = length(line)
      i = 1
      joinnext = 0
      while (i <= n) {
        c = substr(line, i, 1)
        if (sq) {
          if (c == "\047") { sq = 0 }
          else if (c == " " || c == "\t") buf = buf S
          else buf = buf c
          i++
          continue
        }
        if (dq) {
          if (c == "\\") {
            e = substr(line, i + 1, 1)
            if (e == " " || e == "\t") buf = buf S
            else buf = buf e
            i += 2
            continue
          }
          # $(…) and `…` inside "…" still execute: substitute a placeholder
          # into the outer word (so its structure survives — an untraceable
          # placeholder path later fails closed) and queue the inner text as
          # its own segment for scanning.
          if (c == "$" && substr(line, i + 1, 1) == "(") {
            i = subst_span(line, i + 2, n, "(")
            buf = buf "__FANOUT_SUBST__"
            continue
          }
          if (c == "`") {
            i = subst_span(line, i + 1, n, "`")
            buf = buf "__FANOUT_SUBST__"
            continue
          }
          if (c == "\"") { dq = 0 }
          else if (c == " " || c == "\t") buf = buf S
          else buf = buf c
          i++
          continue
        }
        if (c == "\047") { sq = 1; i++; continue }
        if (c == "\"") { dq = 1; i++; continue }
        # A word-boundary # starts a comment: the shell ignores the rest of
        # the line, including any apostrophes in it.
        if (c == "#" && (prev == "" || prev == " " || prev == "\t")) break
        if (c == "\\") {
          if (i == n) { joinnext = 1; i++; continue } # line continuation
          e = substr(line, i + 1, 1)
          if (e == " " || e == "\t") buf = buf S
          else buf = buf e
          i += 2
          continue
        }
        if (c == "$" && substr(line, i + 1, 1) == "(") {
          i = subst_span(line, i + 2, n, "(")
          buf = buf "__FANOUT_SUBST__"
          prev = "_"
          continue
        }
        if (c == "<" && substr(line, i + 1, 1) == "<" && substr(line, i + 2, 1) != "<") {
          j = i + 2
          if (substr(line, j, 1) == "-") j++
          qc = ""
          if (substr(line, j, 1) == "\047" || substr(line, j, 1) == "\"") {
            qc = substr(line, j, 1)
            j++
          }
          w = ""
          while (j <= n && substr(line, j, 1) ~ /[A-Za-z0-9_.-]/) {
            w = w substr(line, j, 1)
            j++
          }
          if (qc != "" && substr(line, j, 1) == qc) j++ # closing quote of <<"EOF"
          if (w != "") { hd = 1; hdword = w; hdq = (qc != ""); buf = buf " "; i = j; continue }
          buf = buf c
          i++
          continue
        }
        if (c == "`") {
          i = subst_span(line, i + 1, n, "`")
          buf = buf "__FANOUT_SUBST__"
          prev = "_"
          continue
        }
        if (c ~ /[;|&(){}]/) { buf = buf "\n"; prev = "\n"; i++; continue }
        buf = buf c
        prev = c
        i++
      }
      # A quoted span continuing past the line end keeps the word joined; a
      # backslash continuation splices the next line into this segment.
      if (sq || dq) buf = buf S
      else if (!joinnext) buf = buf "\n"
    }
    END { printf "%s%s", buf, extra }
  ' <<<"$1"
}

# unsentinel WORD — restore the whitespace strip_shell_noise encoded as \001.
unsentinel() {
  printf '%s' "${1//$'\001'/ }"
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
