PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CODEX_DIR  ?= $(HOME)/.codex
CLAUDE_CMD_DIR   := $(CLAUDE_DIR)/commands
CLAUDE_SKILL_DIR := $(CLAUDE_DIR)/skills
CODEX_SKILL_DIR  := $(CODEX_DIR)/skills

BATS       ?= bats

.PHONY: install link uninstall test test-tier1 test-tier2 lint check-bats

install:
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)/fanout" "$(CODEX_SKILL_DIR)/fanout/agents"
	install -m 0755 fanout "$(BINDIR)/fanout"
	install -m 0644 claude/commands/fanout.md "$(CLAUDE_CMD_DIR)/fanout.md"
	install -m 0644 claude/skills/fanout/SKILL.md "$(CLAUDE_SKILL_DIR)/fanout/SKILL.md"
	install -m 0644 codex/skills/fanout/SKILL.md "$(CODEX_SKILL_DIR)/fanout/SKILL.md"
	install -m 0644 codex/skills/fanout/agents/openai.yaml "$(CODEX_SKILL_DIR)/fanout/agents/openai.yaml"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@echo "  $(CLAUDE_CMD_DIR)/fanout.md"
	@echo "  $(CLAUDE_SKILL_DIR)/fanout/SKILL.md"
	@echo "  $(CODEX_SKILL_DIR)/fanout/SKILL.md"
	@echo "  $(CODEX_SKILL_DIR)/fanout/agents/openai.yaml"

link:
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	ln -sf "$(CURDIR)/fanout" "$(BINDIR)/fanout"
	ln -sf "$(CURDIR)/claude/commands/fanout.md" "$(CLAUDE_CMD_DIR)/fanout.md"
	rm -rf "$(CLAUDE_SKILL_DIR)/fanout"
	ln -sf "$(CURDIR)/claude/skills/fanout" "$(CLAUDE_SKILL_DIR)/fanout"
	rm -rf "$(CODEX_SKILL_DIR)/fanout"
	ln -sf "$(CURDIR)/codex/skills/fanout" "$(CODEX_SKILL_DIR)/fanout"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/fanout"
	@echo "  $(CLAUDE_CMD_DIR)/fanout.md -> $(CURDIR)/claude/commands/fanout.md"
	@echo "  $(CLAUDE_SKILL_DIR)/fanout -> $(CURDIR)/claude/skills/fanout"
	@echo "  $(CODEX_SKILL_DIR)/fanout -> $(CURDIR)/codex/skills/fanout"

uninstall:
	rm -f "$(BINDIR)/fanout"
	rm -f "$(CLAUDE_CMD_DIR)/fanout.md"
	rm -rf "$(CLAUDE_SKILL_DIR)/fanout"
	rm -rf "$(CODEX_SKILL_DIR)/fanout"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@echo "  $(CLAUDE_CMD_DIR)/fanout.md"
	@echo "  $(CLAUDE_SKILL_DIR)/fanout"
	@echo "  $(CODEX_SKILL_DIR)/fanout"

# --- test / lint -------------------------------------------------------------
# `make test`         — run every tier that's implemented (currently just Tier 1).
# `make test-tier1`   — flag / prerequisite black-box tests, no live dmux.
# `make test-tier2`   — --dry-run golden tests (Phase 2; not yet implemented).
# `make lint`         — shellcheck the fanout script and all test shims.
#
# bats-core is required: `brew install bats-core` (macOS) or `apt install bats`
# (Debian/Ubuntu). check-bats prints the install hint before failing.

check-bats:
	@command -v $(BATS) >/dev/null 2>&1 || { \
	  echo "error: bats-core not installed." >&2; \
	  echo "  macOS: brew install bats-core" >&2; \
	  echo "  Linux: apt-get install bats  (or: npm install -g bats)" >&2; \
	  exit 1; \
	}

test: test-tier1

test-tier1: check-bats
	$(BATS) tests/bats/tier1_flags.bats

test-tier2: check-bats
	@if [ ! -f tests/bats/tier2_dry_run.bats ]; then \
	  echo "Tier 2 not yet implemented (issue #20 Phase 2)." >&2; \
	  exit 0; \
	fi
	$(BATS) tests/bats/tier2_dry_run.bats

lint:
	shellcheck fanout tests/bin/gh tests/bin/tmux tests/bats/helpers.bash
