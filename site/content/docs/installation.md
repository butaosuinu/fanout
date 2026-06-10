---
title: Installation
linkTitle: Installation
description: "One curl line installs the Go binary and the Claude Code / Codex integrations. Everything it places, verifies and overwrites — and how to keep it updated."
weight: 10
kanji: 始
yomi: install
---

## Prerequisites

The default pane-creation flow needs these tools on your `PATH`:

| Tool | Used for |
|---|---|
| `gh` | GitHub CLI — issues, PR state, GraphQL |
| `jq` | JSON processing |
| `git` | worktrees, branches, merges |
| `tmux` | child panes |
| `gh-sub-issue` | Sub-issues enumeration — `gh extension install yahsan2/gh-sub-issue` |

fanout checks the dependencies needed for the selected mode at startup and prints install hints on failure. `--status` and `--cleanup` use `gh`/`jq`/`git`; `--merge` and `--close` use `git`.

> **Project mode only:** the `gh` CLI must have the `read:project` scope so the GraphQL query that lists Project items can succeed. Add it with `gh auth refresh -s read:project`. Issue mode (`fanout <N>`) does not need this scope.

## Recommended: the install script

The recommended install path is the released Go binary. It installs the stable `fanout` command plus the bundled Claude / Codex integration files:

```bash
# fanout + Claude/Codex integrations into ~/.local, ~/.claude, ~/.codex
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# Binary only
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# Custom destination or pinned release tag
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.2.0 sh
```

`install.sh` detects macOS/Linux and amd64/arm64, downloads `fanout_<os>_<arch>.tar.gz` from the latest GitHub Release (or `FANOUT_VERSION`), verifies `SHA256SUMS` when `sha256sum` or `shasum` exists, and overwrites the same paths on rerun. It never edits shell rc files.

### Installed paths

- `$BIN_DIR/fanout` (default `~/.local/bin/fanout`)
- `$CLAUDE_DIR/commands/fanout.md` (default `~/.claude/commands/fanout.md`)
- `$CLAUDE_DIR/skills/fanout/` (default `~/.claude/skills/fanout/`)
- `$CLAUDE_DIR/skills/fanout-issues/` (default `~/.claude/skills/fanout-issues/`)
- `$CODEX_DIR/skills/fanout/` (default `~/.codex/skills/fanout/`)
- `$CODEX_DIR/skills/fanout-issues/` (default `~/.codex/skills/fanout-issues/`)

Confirm `~/.local/bin` is on your `PATH`:

```bash
echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"
```

If not, add `export PATH="$HOME/.local/bin:$PATH"` to your shell rc. Restart any running Codex CLI session after installing or updating skills so it picks up the new skill files.

### Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

## macOS security notes

The curl/wget install path normally does not attach the `com.apple.quarantine` extended attribute, so the Gatekeeper "cannot verify developer" GUI block should not appear for the installed binary. If you download the archive through a browser and macOS quarantines it, remove the attribute after extracting:

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

Apple Silicon requires every executable to carry at least an ad-hoc signature. The release workflow builds darwin binaries on macOS with Go 1.23, so the Go linker signs the binary as part of the build. Do not run an external `strip` after release packaging; it can invalidate the signature. If a local copy is damaged, ad-hoc re-sign it with:

```bash
codesign -s - /path/to/fanout
```

Developer ID signing and notarization are intentionally out of scope for the curl distribution path.

## From a checkout

Local Makefile targets install or symlink the Go binary as the stable `fanout` command plus the bundled integrations:

```bash
make install            # builds Go as $(BINDIR)/fanout + installs integrations
make link               # symlinks Go as $(BINDIR)/fanout + links integrations
make uninstall          # removes installed paths

PREFIX=/usr/local sudo make install      # system-wide Go CLI; overrides BINDIR
CLAUDE_DIR=/path/to/.claude make install # non-default Claude data dir
CODEX_DIR=/path/to/.codex make install   # non-default Codex data dir
```

Building from a checkout needs a **Go toolchain** (Go 1.23+): `make install`, `make link`, and `make build-go` all run `go build ./cmd/fanout`. The curl install above ships a prebuilt binary and needs no Go.

## Keeping it updated

### `fanout --check-update`

`fanout --check-update` is read-only. It fetches the latest release tag from `butaosuinu/fanout`, compares it with the binary's embedded version, and prints whether an update is available. `fanout check-update` is accepted as the subcommand form. Local dev builds (`version == "dev"`, including plain `make build-go`) do not call `gh`; they print a dev-build message and exit 0.

| Exit code | Meaning |
|---|---|
| `0` | comparison completed, or this is a dev build |
| `2` | the current version or latest tag is not `MAJOR.MINOR.PATCH` (optionally `v`-prefixed) |
| `3` | `gh release view -R butaosuinu/fanout` failed |

### `fanout update`

`fanout update` replaces the running release binary by invoking the same `install.sh` path documented above, so OS/arch detection, release downloads, checksum verification, archive extraction, and Claude/Codex skill installation stay centralized in one script.

By default it resolves the latest release, compares it with the embedded version, reports the current binary path (after `EvalSymlinks`), and then runs the installer immediately. Local dev builds refuse replacement.

- `--version <tag>` — install a pinned release tag by passing `FANOUT_VERSION=<tag>` to `install.sh`.
- `--no-skills` — pass `--no-skills` through to `install.sh`, updating only the binary.

| Exit code | Meaning |
|---|---|
| `0` | update completed, or already up to date |
| `1` | environment/preflight failure: dev build, no `curl`/`wget`, unwritable binary directory, missing option value |
| `2` | unknown option, unexpected argument, or incomparable version strings |
| `3` | latest release lookup failed |

Next: fan out your first parent issue in the [Quickstart]({{< relref "/docs/quickstart" >}}).
