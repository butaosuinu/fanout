---
title: Installation
linkTitle: Installation
description: "One curl line installs fanout and the Claude Code / Codex integrations. Covers where it installs to, uninstalling, macOS Gatekeeper blocks, and how to keep it updated."
weight: 10
kanji: 始
yomi: install
---

## Prerequisites

Fanning a parent issue out into one pane per child needs three tools on your `PATH`.

| Tool | What it's for |
|---|---|
| `gh` | GitHub CLI — fetching issues, querying PR state, running the Project GraphQL query |
| `git` | creating worktrees, branching, merging |
| `tmux 3.3+` | splitting one pane per child and showing TUI popups |

> **Project mode only:** the `gh` CLI must have the `read:project` scope so the GraphQL query that lists Project items can succeed. Add it with `gh auth refresh -s read:project`. Issue mode (`fanout <N>`) does not need this scope.

## Installation

The recommended path is the released Go binary.

```bash
# fanout + Claude/Codex integrations into ~/.local, ~/.claude, ~/.codex
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# Binary only
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# Custom destination or pinned release tag
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.12.0 sh
```

`install.sh` auto-detects your OS/arch, then fetches and installs the `fanout` binary and the Claude/Codex integration files from the latest Release (or the tag given via `FANOUT_VERSION`).

### Installed paths

Each destination has an environment-variable override for the install command: `BIN_DIR` (default `~/.local/bin`), `CLAUDE_DIR` (default `~/.claude`), and `CODEX_DIR` (default `~/.codex`).

- `$BIN_DIR/fanout` — the binary
- `$CLAUDE_DIR/commands/` — the `fanout`, `pr-watch`, and `session-retro` slash commands
- `$CLAUDE_DIR/skills/` — the `fanout`, `fanout-issues`, `fanout-plan`, `post-work-review`, `pr-watch`, and `session-retro` skills
- `$CODEX_DIR/skills/` — the same skills minus `session-retro`; `post-work-review` bundles its marker helper inside the skill

Install and update overwrite all of these.

Confirm `~/.local/bin` is on your `PATH`:

```bash
echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"
```

If not, add `export PATH="$HOME/.local/bin:$PATH"` to your shell rc.

### Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

## If blocked on macOS

The curl/wget install path normally does not attach the `com.apple.quarantine` extended attribute, so Gatekeeper blocks generally don't happen. If you download it through a browser and it gets quarantined, remove the attribute:

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

If a local copy's signature is broken, ad-hoc re-sign it:

```bash
codesign -s - /path/to/fanout
```

## From a checkout

```bash
make install        # builds the Go binary as $(BINDIR)/fanout + copies integrations
make link           # symlinks the Go binary as $(BINDIR)/fanout + symlinks integrations
make uninstall      # removes installed paths
```

Building it needs a Go toolchain (Go 1.26.5+) plus Node.js 24+ and pnpm 11+ (`make install` builds the dashboard web UI first and embeds it). The curl install ships a prebuilt binary, so it needs neither Go nor Node.

## Keeping it updated

`fanout --check-update` is read-only. It only compares the latest release tag with the embedded version and reports whether an update is available — it changes nothing.

`fanout update` calls the same curl install path above, updating the binary and the Claude/Codex integrations together.

- `--version <tag>`: install the given tag
- `--no-skills`: update only the binary. If the retired Codex
  `post-work-review.sh` driver is installed, this stops before replacement;
  rerun without `--no-skills` to migrate the integrations.

> Install and update overwrite the bundled files under `~/.claude` and `~/.codex` — including the `post-work-review` / `pr-watch` skills — so back up customized copies first. Codex CLI loads skills at startup; restart running Codex sessions after an update.

See the [CLI reference]({{< relref "/docs/cli" >}}) for the exit code list.

Next: open your first parent issue in the [Quickstart]({{< relref "/docs/quickstart" >}}).
