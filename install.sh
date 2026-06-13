#!/bin/sh
set -eu

repo="butaosuinu/fanout"
install_skills=1
uninstall=0
tmp=""

usage() {
  cat <<'EOF'
Usage: install.sh [--no-skills] [--uninstall] [-h|--help]

Installs the latest fanout Go binary and bundled Claude/Codex integrations.

Environment:
  BIN_DIR         Binary destination (default: $HOME/.local/bin)
  FANOUT_VERSION Git tag to install, e.g. v0.1.0 (default: latest)
  CLAUDE_DIR      Claude data directory (default: $HOME/.claude)
  CODEX_DIR       Codex data directory (default: $HOME/.codex)

Options:
  --no-skills   Install or uninstall only the fanout binary.
  --uninstall   Remove fanout and bundled integrations.
  -h, --help    Show this help.
EOF
}

info() {
  printf '[info] %s\n' "$*"
}

warn() {
  printf '[warn] %s\n' "$*" >&2
}

die() {
  printf '[err ] %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$tmp" ]; then
    rm -rf "$tmp"
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-skills)
      install_skills=0
      ;;
    --uninstall)
      uninstall=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
  shift
done

: "${HOME:?HOME must be set}"
bin_dir="${BIN_DIR:-$HOME/.local/bin}"
claude_dir="${CLAUDE_DIR:-$HOME/.claude}"
codex_dir="${CODEX_DIR:-$HOME/.codex}"
version="${FANOUT_VERSION:-latest}"

download() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
  else
    die "curl or wget is required to download fanout"
  fi
}

install_exec() {
  src="$1"
  dest="$2"
  if command -v install >/dev/null 2>&1; then
    install -m 0755 "$src" "$dest"
  else
    cp "$src" "$dest"
    chmod 0755 "$dest"
  fi
}

install_data() {
  src="$1"
  dest="$2"
  if command -v install >/dev/null 2>&1; then
    install -m 0644 "$src" "$dest"
  else
    cp "$src" "$dest"
    chmod 0644 "$dest"
  fi
}

# Uninstall runs before the release tarball is fetched/extracted, so the list of
# integrations to remove cannot be derived from the source the way
# install_integrations does. Keep this enumeration in sync with whatever
# claude/commands, claude/skills, and codex/skills the repo ships.
remove_integrations() {
  rm -f "$claude_dir/commands/fanout.md" "$claude_dir/commands/pr-watch.md"
  rm -rf "$claude_dir/skills/fanout" "$claude_dir/skills/fanout-issues" \
    "$claude_dir/skills/post-work-review" "$claude_dir/skills/pr-watch"
  rm -rf "$codex_dir/skills/fanout" "$codex_dir/skills/fanout-issues"
}

copy_skill_dirs() {
  src_root="$1"
  dest_root="$2"
  [ -d "$src_root" ] || return 0
  mkdir -p "$dest_root"
  for src in "$src_root"/*; do
    [ -d "$src" ] || continue
    name=$(basename "$src")
    dest="$dest_root/$name"
    rm -rf "$dest"
    mkdir -p "$dest"
    cp -R "$src/." "$dest/"
  done
}

install_integrations() {
  [ "$install_skills" -eq 1 ] || return 0

  if [ -d "$tmp/extract/claude/commands" ]; then
    mkdir -p "$claude_dir/commands"
    for src in "$tmp"/extract/claude/commands/*.md; do
      [ -f "$src" ] || continue
      install_data "$src" "$claude_dir/commands/$(basename "$src")"
    done
  fi

  copy_skill_dirs "$tmp/extract/claude/skills" "$claude_dir/skills"
  copy_skill_dirs "$tmp/extract/codex/skills" "$codex_dir/skills"
}

normalize_os() {
  case "$(uname -s)" in
    Darwin) printf 'darwin' ;;
    Linux) printf 'linux' ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

normalize_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    arm64|aarch64) printf 'arm64' ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

checksum_tool() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf 'sha256sum'
  elif command -v shasum >/dev/null 2>&1; then
    printf 'shasum'
  else
    return 1
  fi
}

file_sha256() {
  tool="$1"
  file="$2"
  if [ "$tool" = "sha256sum" ]; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

verify_checksum() {
  archive="$1"
  sums="$2"
  asset="$3"

  if ! tool=$(checksum_tool); then
    warn "sha256sum/shasum not found; skipping checksum verification"
    return 0
  fi

  expected=$(awk -v file="$asset" '$2 == file || $2 == "./" file { print $1; found = 1 } END { if (!found) exit 1 }' "$sums") \
    || die "SHA256SUMS does not contain $asset"
  actual=$(file_sha256 "$tool" "$archive")

  if [ "$expected" != "$actual" ]; then
    die "checksum mismatch for $asset"
  fi
  info "checksum verified: $asset"
}

warn_path() {
  case ":${PATH:-}:" in
    *":$bin_dir:"*) ;;
    *)
      warn "$bin_dir is not on PATH"
      warn "add this to your shell rc: export PATH=\"$bin_dir:\$PATH\""
      ;;
  esac
}

if [ "$uninstall" -eq 1 ]; then
  rm -f "$bin_dir/fanout"
  if [ "$install_skills" -eq 1 ]; then
    remove_integrations
  fi
  info "removed $bin_dir/fanout"
  exit 0
fi

os=$(normalize_os)
arch=$(normalize_arch)
asset="fanout_${os}_${arch}.tar.gz"

if [ "$version" = "latest" ]; then
  base_url="https://github.com/$repo/releases/latest/download"
else
  base_url="https://github.com/$repo/releases/download/$version"
fi

tmp=$(mktemp -d)
trap cleanup EXIT HUP INT TERM

archive="$tmp/$asset"
sums="$tmp/SHA256SUMS"
mkdir -p "$tmp/extract"

info "downloading $asset from $version"
download "$base_url/$asset" "$archive"
download "$base_url/SHA256SUMS" "$sums"
verify_checksum "$archive" "$sums" "$asset"

tar -xzf "$archive" -C "$tmp/extract"
[ -f "$tmp/extract/fanout" ] || die "archive does not contain fanout"

mkdir -p "$bin_dir"
install_exec "$tmp/extract/fanout" "$bin_dir/fanout"
install_integrations

info "installed $bin_dir/fanout"
if [ "$install_skills" -eq 1 ]; then
  info "installed Claude/Codex integrations"
fi
warn_path
"$bin_dir/fanout" --version
