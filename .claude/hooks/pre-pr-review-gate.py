#!/usr/bin/env python3
"""fanout PR-review gate — parser for the PreToolUse(Bash) hook.

Reads the tool-call JSON on stdin and decides whether to allow or deny a
`gh pr create` (or its `new` alias). Uses a real shell tokenizer (shlex) so a
command word (`'gh'`) is distinguished from an argument *value* (a `gh pr
create` mention inside a commit message / --title / --body), and shell
structure (operators, if/while/until/case, `!`, wrappers, command substitution)
is understood rather than pattern-matched.

Contract: allow = exit 0 with no stdout; deny = print the hookSpecificOutput
JSON to stdout and exit 0. Verifies LOCAL git refs only (no network). Invoked by
the bash wrapper, which fail-closes when python3 is unavailable.
"""
import sys
import os
import json
import re
import shlex
import subprocess

# `<<WORD` / `<<-WORD` / `<<'WORD'` / `<<"WORD"` heredoc start (not `<<<`
# here-strings). The delimiter may be quoted and contain non-identifier chars
# (e.g. `<<'PR-BODY'`).
_HEREDOC_RE = re.compile(r"""<<-?[ \t]*(?:'([^']*)'|"([^"]*)"|([^\s;&|<>()'"`]+))""")

HATCH = "緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
BYPASS = "FANOUT_SKIP_PR_REVIEW=1"

SEP = {";", ";;", "&&", "||", "|", "|&", "&", "(", ")", "{", "}", "\n"}
REDIR = {"<", ">", ">>", "<<", "<<<", "2>", "2>>", "1>", "1>>", "&>", "&>>",
         ">|", "<&", ">&"}
LEAD = {"!", "if", "then", "elif", "else", "while", "until", "do", "time"}
WRAP = {"env", "command", "builtin", "exec", "nice", "nohup", "setsid", "stdbuf"}

HEAD_FLAGS = ("--head", "-H")
BASE_FLAGS = ("--base", "-B")
REPO_FLAGS = ("--repo", "-R")
# Value-taking flags of `gh pr create` (so a value isn't mis-read as a flag).
VALUE_FLAGS = {"--head", "-H", "--base", "-B", "--repo", "-R", "--title", "-t",
               "--body", "-b", "--body-file", "-F", "--reviewer", "-r",
               "--assignee", "-a", "--label", "-l", "--milestone", "-m",
               "--project", "-p", "--template", "-T"}


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


def strip_heredocs(cmd):
    """Drop heredoc bodies (data fed to a command, not executed) so a
    `gh pr create` mention inside e.g. `git commit -F- <<EOF ... EOF` is not
    parsed as a command. (Feeding a heredoc to a shell is indirect execution,
    already out of scope.)"""
    if "<<" not in cmd:
        return cmd
    lines = cmd.split("\n")
    out = []
    i = 0
    while i < len(lines):
        line = lines[i]
        out.append(line)
        m = _HEREDOC_RE.search(line)
        i += 1
        if m:
            delim = m.group(1) if m.group(1) is not None else (
                m.group(2) if m.group(2) is not None else m.group(3))
            while i < len(lines) and lines[i].strip() != delim:
                i += 1
            i += 1  # also drop the delimiter line
    return "\n".join(out)


def tokenize(cmd):
    cmd = strip_heredocs(cmd)
    cmd = cmd.replace("\\\n", "")
    cmd = cmd.replace("\r\n", "\n").replace("\r", "\n").replace("\n", ";")
    lx = shlex.shlex(cmd, posix=True, punctuation_chars=True)
    lx.whitespace_split = True
    toks = []
    try:
        for t in lx:
            toks.append(t)
    except ValueError:
        pass
    out = []
    i = 0
    while i < len(toks):
        if toks[i] in REDIR:
            i += 2
            continue
        out.append(toks[i])
        i += 1
    return out


def extract_cmdsubs(s):
    """Return the bodies of $( ... ) and `...` command substitutions, honoring
    single quotes (where substitution is literal) and double quotes (where it
    still runs)."""
    out = []
    i = 0
    n = len(s)
    while i < n:
        c = s[i]
        if c == "'":
            j = s.find("'", i + 1)
            i = (j + 1) if j != -1 else n
            continue
        if c == "\\" and i + 1 < n:
            i += 2
            continue
        if c == "$" and i + 1 < n and s[i + 1] == "(":
            depth = 1
            j = i + 2
            while j < n and depth > 0:
                if s[j] == "(":
                    depth += 1
                elif s[j] == ")":
                    depth -= 1
                j += 1
            out.append(s[i + 2:j - 1])
            i = j
            continue
        if c == "`":
            j = s.find("`", i + 1)
            if j == -1:
                break
            out.append(s[i + 1:j])
            i = j + 1
            continue
        i += 1
    return out


def basename(word):
    return word.rsplit("/", 1)[-1]


def normalize_repo(spec):
    """Reduce a repo spec (owner/repo, host/owner/repo, URL, git@host:owner/repo)
    to a lowercase `owner/repo`, or None."""
    if not spec:
        return None
    s = spec.strip().rstrip("/")
    if s.endswith(".git"):
        s = s[:-4]
    parts = [p for p in re.split(r"[/:]", s) if p]
    if len(parts) < 2:
        return None
    return (parts[-2] + "/" + parts[-1]).lower()


def find_repo_flag(toks, start, end):
    """Return the -R/--repo value among toks[start:end], or None."""
    m = start
    while m < end:
        tk = toks[m]
        if tk in REPO_FLAGS:
            return toks[m + 1] if m + 1 < end else ""
        if tk.startswith("--repo="):
            return tk[len("--repo="):]
        if tk.startswith("-R=") :
            return tk[len("-R="):]
        if tk.startswith("-R") and len(tk) > 2:
            return tk[2:]
        m += 1
    return None


def collect_create_flags(toks, start, n):
    """From just after the create/new token, read the create's flags until a
    separator, skipping value-taking flags' values so they are not mis-read."""
    head = base = repo = None
    is_help = False
    m = start
    while m < n and toks[m] not in SEP:
        tk = toks[m]
        if tk.startswith("-") and "=" in tk:
            key, val = tk.split("=", 1)
            if key in HEAD_FLAGS:
                head = val
            elif key in BASE_FLAGS:
                base = val
            elif key in REPO_FLAGS:
                repo = val
            m += 1
            continue
        if len(tk) > 2 and tk[0] == "-" and tk[1] != "-":
            short, val = tk[:2], tk[2:]
            if short in HEAD_FLAGS:
                head = val
            elif short in BASE_FLAGS:
                base = val
            elif short in REPO_FLAGS:
                repo = val
            m += 1
            continue
        if tk in ("--help", "-h"):
            is_help = True
            m += 1
            continue
        if tk in VALUE_FLAGS:
            val = toks[m + 1] if (m + 1 < n and toks[m + 1] not in SEP) else None
            if tk in HEAD_FLAGS:
                head = val if val is not None else ""
            elif tk in BASE_FLAGS:
                base = val if val is not None else ""
            elif tk in REPO_FLAGS:
                repo = val if val is not None else ""
            m += 2 if val is not None else 1
            continue
        m += 1
    return head, base, repo, is_help, m


def scan_commands(toks):
    n = len(toks)
    creates = []
    dir_pending = False
    export_bypass = False
    export_gh_repo = ""
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
        bypass_here = False
        chdir_here = False
        ghrepo_here = ""
        while i < n:
            t = toks[i]
            if t == BYPASS:
                bypass_here = True
                i += 1
                continue
            if is_assignment(t):
                if t.startswith("GH_REPO="):
                    ghrepo_here = t[len("GH_REPO="):]
                i += 1
                continue
            if t in LEAD:
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
                                i += 1
                            continue
                        if e.startswith("--chdir="):
                            chdir_here = True
                            i += 1
                            continue
                        if e in ("-u", "-S"):
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
                            if e.startswith("GH_REPO="):
                                ghrepo_here = e[len("GH_REPO="):]
                            i += 1
                            continue
                        break
                    continue
                i += 1
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
        if base == "export":
            # `export FANOUT_SKIP_PR_REVIEW=1` bypasses, and `export GH_REPO=...`
            # retargets, the rest of the line.
            p = i + 1
            while p < n and toks[p] not in SEP:
                if toks[p] == BYPASS:
                    export_bypass = True
                elif toks[p].startswith("GH_REPO="):
                    export_gh_repo = toks[p][len("GH_REPO="):]
                p += 1
            expect = False
            i = p
            continue
        if chdir_here:
            dir_pending = True
        if base == "gh":
            gi = i
            # locate the `pr` token, skipping gh global flags + their values.
            j = i + 1
            while j < n and toks[j] not in SEP and toks[j] != "pr":
                if toks[j] in REPO_FLAGS:
                    j += 2
                    continue
                j += 1
            if j < n and toks[j] == "pr":
                # the subcommand is the first non-flag token after `pr`.
                k = j + 1
                while k < n and toks[k] not in SEP and toks[k].startswith("-"):
                    if toks[k] in REPO_FLAGS:
                        k += 2
                        continue
                    k += 1
                if k < n and toks[k] in ("create", "new"):
                    head, base_, repo_, is_help, m = collect_create_flags(toks, k + 1, n)
                    # -R/--repo before the subcommand, or a GH_REPO assignment,
                    # also retargets the repo.
                    if not repo_:
                        repo_ = find_repo_flag(toks, gi, k)
                    if not repo_:
                        repo_ = ghrepo_here or export_gh_repo or None
                    creates.append({"head": head, "base": base_, "repo": repo_,
                                    "help": is_help, "dir": dir_pending,
                                    "bypass": bypass_here or export_bypass})
                    i = m
                    expect = False
                    continue
        expect = False
        i += 1
    return creates


def gather_creates(cmd, depth=0):
    creates = scan_commands(tokenize(cmd))
    if depth < 6:
        for sub in extract_cmdsubs(cmd):
            creates += gather_creates(sub, depth + 1)
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

    creates = gather_creates(cmd)
    if not creates:
        emit_allow()
    real = [c for c in creates if not c["help"]]
    if not real:
        emit_allow()

    # A session-level export is a global operator override.
    if os.environ.get("FANOUT_SKIP_PR_REVIEW") == "1":
        emit_allow()
    # An inline/export bypass is scoped to its own create command (below). If
    # every real create carries its own bypass, allow.
    if all(c["bypass"] for c in real):
        emit_allow()

    dirmsg = ("ディレクトリ変更を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。\n"
              "対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。\n" + HATCH)

    def git(args):
        if not cwd or not os.path.isdir(cwd):
            class R:
                returncode = 1
                stdout = ""
            return R()
        try:
            return subprocess.run(["git"] + args, cwd=cwd, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, text=True)
        except Exception:
            class R:
                returncode = 1
                stdout = ""
            return R()

    # Structural denials, checked BEFORE the non-git-cwd fallback so a create
    # that cd's into a repo, or targets a *different* repository, can't slip
    # through when the payload cwd isn't a repo.
    cur_repo = normalize_repo(git(["config", "--get", "remote.origin.url"]).stdout.strip())
    gh_repo_env = os.environ.get("GH_REPO")
    for c in real:
        if c["bypass"]:
            continue
        if c["dir"]:
            emit_deny(dirmsg)
        rt = c["repo"] or gh_repo_env
        if rt:
            # Allow only when the explicit target resolvably IS this repo.
            tn = normalize_repo(rt)
            if not (cur_repo and tn and tn == cur_repo):
                emit_deny("gh pr create が別リポジトリ (%s) を対象にしています (-R/--repo / GH_REPO)。\n"
                          "ローカルの marker は現在のリポジトリのものなので照合できません%s。\n"
                          "対象リポジトリで /post-work-review してから実行してください。\n%s"
                          % (rt, ("" if cur_repo else " (現在のリポジトリを解決できませんでした)"), HATCH))

    # Local git context (only needed for marker / head / base verification).
    if not cwd or not os.path.isdir(cwd):
        emit_allow()

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
    marker_meta = marker + ".meta"

    def read_marker():
        try:
            with open(marker) as f:
                return f.read().strip()
        except Exception:
            return None

    def read_review_metadata():
        metadata = {}
        try:
            with open(marker_meta) as f:
                for line in f:
                    key, sep, value = line.rstrip("\n").partition("=")
                    if sep and key:
                        metadata[key] = value.strip()
        except FileNotFoundError:
            return {}, os.path.lexists(marker_meta)
        except Exception:
            return {}, True
        return metadata, True

    def normalize_reviewed_base(value):
        if not value:
            return None
        value = value.strip()
        for prefix in ("refs/remotes/origin/", "origin/", "refs/heads/"):
            if value.startswith(prefix):
                value = value[len(prefix):]
                break
        return value or None

    def branch_diff_hash(base_ref, target):
        if not base_ref or not target:
            return None
        diff_options = ["--no-ext-diff", "--no-textconv",
                        "--ignore-submodules=none", "--no-color", "--binary"]
        for revisions in ([base_ref + "..." + target], [base_ref, target]):
            try:
                diff = subprocess.run(
                    ["git", "diff"] + diff_options + revisions + ["--"],
                    cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            except Exception:
                return None
            if diff.returncode != 0:
                continue
            try:
                hashed = subprocess.run(
                    ["git", "hash-object", "--stdin"], cwd=cwd,
                    input=diff.stdout, stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL)
            except Exception:
                return None
            if hashed.returncode == 0:
                return hashed.stdout.decode("ascii", errors="ignore").strip() or None
            return None
        return None

    review_metadata, review_metadata_present = read_review_metadata()

    defbr = git(["symbolic-ref", "--short", "refs/remotes/origin/HEAD"]).stdout.strip()
    if defbr.startswith("origin/"):
        defbr = defbr[len("origin/"):]
    if not defbr:
        defbr = git(["config", "--get", "init.defaultBranch"]).stdout.strip()
    cur = git(["rev-parse", "--abbrev-ref", "HEAD"]).stdout.strip()

    for c in real:
        if c["bypass"]:
            continue  # this specific create is bypassed; others still checked
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
            emit_deny("post-work-review が未実施です。先に /post-work-review または $post-work-review を実行してください。\n"
                      "完了時に skill が現在の HEAD(%s)を %s に記録します。\n"
                      "/post-work-review が使えない場合は trusted origin/main checkout または release から連携を更新してください。\n"
                      "完了後に gh pr create を再実行してください。\n%s" % (head, marker, HATCH))

        base = c["base"]
        if not base and cur:
            mb = git(["config", "--get", "branch.%s.gh-merge-base" % cur]).stdout.strip()
            if mb:
                base = mb

        # Codex records the target and trusted bootstrap base in marker
        # metadata. The marker itself means the parent accepted the native
        # subagent review. Claude's legacy skill removes the metadata, so
        # marker-only mode stays default-base-only.
        if review_metadata_present:
            metadata_valid = (
                review_metadata.get("post_work_review_version") == "12" and
                review_metadata.get("head") == target and
                bool(review_metadata.get("base")) and
                bool(review_metadata.get("base_head")) and
                bool(review_metadata.get("bootstrap_base")) and
                bool(review_metadata.get("diff_hash"))
            )
            if not metadata_valid:
                emit_deny("post-work-review の marker metadata が不完全か PR head と一致しません。\n"
                          "対象 HEAD で /post-work-review をやり直してください。\n%s" % HATCH)
            reviewed_base = review_metadata.get("base")
            requested_base = (base or defbr or "").strip() or None
            normalized_reviewed_base = normalize_reviewed_base(reviewed_base)
            if not (requested_base and normalized_reviewed_base and
                    requested_base == normalized_reviewed_base):
                emit_deny("gh pr create の base (%s) がレビュー済み base (%s) と異なる/確認できないため、レビューした diff 範囲と PR の diff 範囲がずれる可能性があります。\n"
                          "marker meta の base に合わせて --base を指定するか、対象 base に対して /post-work-review し直してください。\n%s"
                          % (base or "未指定・既定ブランチを解決不可",
                             reviewed_base or "marker meta から解決不可", HATCH))
            pr_base_ref = "refs/remotes/origin/" + requested_base
            current_base_head = git(["rev-parse", "--verify", pr_base_ref + "^{commit}"]).stdout.strip()
            if current_base_head != review_metadata.get("base_head"):
                emit_deny("marker_reason=review_base_changed\n"
                          "remote base が post-work-review 後に移動したか、ローカルで解決できません。\n"
                          "対象 base に対して /post-work-review をやり直してください。\n%s" % HATCH)
            current_bootstrap_base = git(["merge-base", pr_base_ref, target]).stdout.strip()
            if current_bootstrap_base != review_metadata.get("bootstrap_base"):
                emit_deny("marker_reason=review_bootstrap_base_changed\n"
                          "trusted bootstrap base が marker metadata と一致しません。\n"
                          "対象 base に対して /post-work-review をやり直してください。\n%s" % HATCH)
            current_diff_hash = branch_diff_hash(pr_base_ref, target)
            if current_diff_hash != review_metadata.get("diff_hash"):
                emit_deny("marker_reason=review_diff_changed\n"
                          "PR head と remote base の diff が post-work-review 後に変わっているか、remote base をローカルで解決できません。\n"
                          "対象 base に対して /post-work-review をやり直してください。\n%s" % HATCH)
        elif not (defbr and (base or defbr) == defbr):
            shown = ("既定ブランチ (%s)" % defbr) if defbr else "既定ブランチ(ローカルで解決不可)"
            emit_deny("gh pr create の base (%s) が%sと異なる/確認できないため、Claude の marker-only review と PR の diff 範囲がずれる可能性があります。\n"
                      "既定ブランチを base にするか、Codex の reviewed-base metadata を伴う /post-work-review を実行してください。\n%s"
                      % (base or "未指定", shown, HATCH))

    emit_allow()


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception:
        emit_allow()
