#!/usr/bin/env python3
"""fanout PR-review gate — parser for the PreToolUse(Bash) hook.

Reads the tool-call JSON on stdin and decides whether to allow or deny a
`gh pr create` (or its `new` alias). Unlike a regex, this uses a real shell
tokenizer (shlex) so a command word (`'gh'`) is distinguished from an argument
*value* (a `gh pr create` mention inside a commit message / --title / --body),
and shell structure (operators, `if/while/until/case`, `!`, wrappers, command
substitution) is understood rather than pattern-matched.

Contract: allow = exit 0 with no stdout; deny = print the hookSpecificOutput
JSON to stdout and exit 0.

The gate verifies LOCAL git refs only (no network). It is invoked by the bash
wrapper, which fail-closes when python3 is unavailable.
"""
import sys
import os
import json
import shlex
import subprocess

BYPASS = "FANOUT_SKIP_PR_REVIEW=1"
HATCH = "緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."

# Operator tokens that terminate a simple command / start a new one.
SEP = {";", ";;", "&&", "||", "|", "|&", "&", "(", ")", "{", "}", "\n"}
# Redirection operators — part of a command, not a separator. We drop the
# operator and its target token so they don't break command/flag scanning.
REDIR = {"<", ">", ">>", "<<", "<<<", "2>", "2>>", "1>", "1>>", "&>", "&>>",
         ">|", "<&", ">&"}
# Reserved words / negation that precede a command word without being one.
LEAD = {"!", "if", "then", "elif", "else", "while", "until", "do", "time"}
# Command wrappers whose own command word is what follows them.
WRAP = {"env", "command", "builtin", "exec", "nice", "nohup", "setsid", "stdbuf"}


def emit_allow():
    sys.exit(0)


def emit_deny(reason):
    sys.stdout.write(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": reason,
    }}))
    sys.exit(0)


def is_assignment(tok):
    if "=" not in tok:
        return False
    name = tok.split("=", 1)[0]
    return bool(name) and (name[0].isalpha() or name[0] == "_") and all(
        c.isalnum() or c == "_" for c in name)


def tokenize(cmd):
    # A newline is a command separator in the shell, but shlex lumps it into
    # whitespace; normalize so command boundaries survive (after removing
    # backslash-newline line continuations, which the shell joins).
    cmd = cmd.replace("\\\n", "")
    cmd = cmd.replace("\r\n", "\n").replace("\r", "\n").replace("\n", ";")
    lx = shlex.shlex(cmd, posix=True, punctuation_chars=True)
    lx.whitespace_split = True
    toks = []
    try:
        for t in lx:
            toks.append(t)
    except ValueError:
        # Unbalanced quotes etc. — best effort with whatever parsed. A command
        # with a real syntax error won't run anyway.
        pass
    # Drop redirection operators and their targets; they are part of a command,
    # not separators, and must not interrupt flag scanning (e.g. create<in).
    out = []
    i = 0
    while i < len(toks):
        if toks[i] in REDIR:
            i += 2
            continue
        out.append(toks[i])
        i += 1
    return out


def basename(word):
    return word.rsplit("/", 1)[-1]


def collect_create_flags(toks, start, n):
    """From just after a create/new token, read its flags until a separator."""
    head = None
    base = None
    is_help = False
    m = start
    while m < n and toks[m] not in SEP:
        tk = toks[m]
        nxt = toks[m + 1] if (m + 1 < n and toks[m + 1] not in SEP) else None
        if tk in ("--help", "-h"):
            is_help = True
        elif tk in ("--head", "-H"):
            head = nxt if nxt is not None else ""
        elif tk.startswith("--head="):
            head = tk[len("--head="):]
        elif tk.startswith("-H=") :
            head = tk[len("-H="):]
        elif tk.startswith("-H") and len(tk) > 2:
            head = tk[2:]
        elif tk in ("--base", "-B"):
            base = nxt if nxt is not None else ""
        elif tk.startswith("--base="):
            base = tk[len("--base="):]
        elif tk.startswith("-B="):
            base = tk[len("-B="):]
        elif tk.startswith("-B") and len(tk) > 2:
            base = tk[2:]
        m += 1
    return head, base, is_help, m


def scan_commands(toks):
    """Walk the token stream; return (creates, ) where each create is a dict
    {head, base, help, dir_changed, bypass}. dir_changed reflects a cd/pushd or
    env -C/--chdir seen earlier in the command line."""
    n = len(toks)
    creates = []
    dir_pending = False
    i = 0
    expect = True
    while i < n:
        t = toks[i]
        if t in SEP:
            expect = True
            i += 1
            continue
        if not expect:
            i += 1
            continue
        # Command-word position: consume prefixes (assignments / negation /
        # reserved words / wrappers), tracking a bypass assignment and env -C.
        bypass_here = False
        chdir_here = False
        while i < n:
            t = toks[i]
            if t == BYPASS:
                bypass_here = True
                i += 1
                continue
            if is_assignment(t) or t in LEAD:
                i += 1
                continue
            if t in WRAP:
                if t == "env":
                    i += 1
                    while i < n and toks[i] not in SEP:
                        e = toks[i]
                        if e == BYPASS:
                            bypass_here = True
                            i += 1
                            continue
                        if e in ("-C", "--chdir"):
                            chdir_here = True
                            i += 1
                            if i < n and toks[i] not in SEP:
                                i += 1  # its value
                            continue
                        if e.startswith("--chdir="):
                            chdir_here = True
                            i += 1
                            continue
                        if e in ("-u", "-S"):  # value-taking env opts
                            i += 1
                            if i < n and toks[i] not in SEP:
                                i += 1
                            continue
                        if e == "--":
                            i += 1
                            break
                        if e.startswith("-"):
                            i += 1
                            continue
                        if is_assignment(e):
                            i += 1
                            continue
                        break  # env's command word
                    continue
                i += 1  # skip other wrapper word
                continue
            break
        if i >= n:
            break
        t = toks[i]
        if t in SEP:
            expect = True
            continue
        base = basename(t)
        if base in ("cd", "pushd"):
            dir_pending = True
            expect = False
            i += 1
            continue
        if chdir_here:
            dir_pending = True
        if base == "gh":
            j = i + 1
            while j < n and toks[j] not in SEP and toks[j] != "pr":
                j += 1
            if j < n and toks[j] == "pr":
                k = j + 1
                while k < n and toks[k] not in SEP and toks[k] not in ("create", "new"):
                    k += 1
                if k < n and toks[k] in ("create", "new"):
                    head, base_, is_help, m = collect_create_flags(toks, k + 1, n)
                    creates.append({"head": head, "base": base_, "help": is_help,
                                    "dir": dir_pending, "bypass": bypass_here})
                    i = m
                    expect = False
                    continue
        expect = False
        i += 1
    return creates


def main():
    raw = sys.stdin.read()
    try:
        data = json.loads(raw)
    except Exception:
        emit_allow()
    if data.get("tool_name") != "Bash":
        emit_allow()
    ti = data.get("tool_input") or {}
    cmd = ti.get("command") or ""
    cwd = data.get("cwd") or ""

    creates = scan_commands(tokenize(cmd))
    if not creates:
        emit_allow()
    real = [c for c in creates if not c["help"]]
    if not real:
        emit_allow()  # help-only invocation(s)

    # Explicit operator override: exported, or a bypass assignment prefixing a
    # real create command.
    if os.environ.get("FANOUT_SKIP_PR_REVIEW") == "1":
        emit_allow()
    if any(c["bypass"] for c in real):
        emit_allow()

    if not cwd or not os.path.isdir(cwd):
        emit_allow()

    def git(args):
        try:
            return subprocess.run(["git"] + args, cwd=cwd, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, text=True)
        except Exception:
            class R:
                returncode = 1
                stdout = ""
            return R()

    if git(["rev-parse", "--is-inside-work-tree"]).stdout.strip() != "true":
        emit_allow()
    head = git(["rev-parse", "HEAD"]).stdout.strip()
    if not head:
        emit_allow()
    gitdir = git(["rev-parse", "--git-dir"]).stdout.strip()
    if not gitdir:
        emit_allow()
    marker = gitdir if os.path.isabs(gitdir) else os.path.join(cwd, gitdir)
    marker = os.path.join(marker, "post-work-review-passed")

    def read_marker():
        try:
            with open(marker) as f:
                return f.read().strip()
        except Exception:
            return None

    defbr = git(["symbolic-ref", "--short", "refs/remotes/origin/HEAD"]).stdout.strip()
    if defbr.startswith("origin/"):
        defbr = defbr[len("origin/"):]
    cur = git(["rev-parse", "--abbrev-ref", "HEAD"]).stdout.strip()

    dirmsg = ("ディレクトリ変更を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。\n"
              "対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。\n" + HATCH)

    for c in real:
        if c["dir"]:
            emit_deny(dirmsg)
        base = c["base"]
        if not base and cur:
            mb = git(["config", "--get", "branch.%s.gh-merge-base" % cur]).stdout.strip()
            if mb:
                base = mb
        if base and defbr and base != defbr:
            emit_deny("gh pr create の base (%s) が既定ブランチ (%s) と異なり、レビューした diff 範囲と PR の diff 範囲がずれます。\n"
                      "既定ブランチを base にするか、対象 base に対して /post-work-review し直してください。\n%s" % (base, defbr, HATCH))
        hr = c["head"]
        if hr:
            if ":" in hr:
                emit_deny("gh pr create --head (%s) は別フォーク (owner:branch) を指しており、ローカルではレビュー状態を確認できません。\n"
                          "対象ブランチを checkout して /post-work-review するか、緊急回避してください。\n%s" % (hr, HATCH))
            target = git(["rev-parse", "--verify", hr + "^{commit}"]).stdout.strip()
            if not target:
                emit_deny("gh pr create --head (%s) のブランチをローカルで解決できません。\n"
                          "対象ブランチを checkout/fetch してから実行してください。\n%s" % (hr, HATCH))
            origintip = git(["rev-parse", "--verify", "refs/remotes/origin/" + hr + "^{commit}"]).stdout.strip()
            if origintip and origintip != target:
                emit_deny("gh pr create --head (%s) のローカル ref が origin/%s と一致しません (push 前 / fetch 前)。\n"
                          "PR は push 済みのリモートブランチから作成されるため、同期してから実行してください。\n%s" % (hr, hr, HATCH))
        else:
            target = head
        if read_marker() != target:
            emit_deny("post-work-review が未実施です。先に /post-work-review を実行してください。\n"
                      "完了時に skill が現在の HEAD(%s)を %s に記録します。\n"
                      "(codex companion 未検出の場合は Pass 2 はスキップされ、Pass 1 通過で marker が書かれます)\n"
                      "/post-work-review が使えない場合は repo で make install (または make link) を実行してください。\n"
                      "完了後に gh pr create を再実行してください。\n%s" % (head, marker, HATCH))

    emit_allow()


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception:
        # A parser bug must not block normal work; fall back to allow (the gate
        # is best-effort and other safeguards remain).
        emit_allow()
