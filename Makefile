PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CODEX_DIR  ?= $(HOME)/.codex
CLAUDE_CMD_DIR   := $(CLAUDE_DIR)/commands
CLAUDE_SKILL_DIR := $(CLAUDE_DIR)/skills
CODEX_SKILL_DIR  := $(CODEX_DIR)/skills
CLAUDE_COMMANDS := $(notdir $(wildcard claude/commands/*.md))
CLAUDE_SKILLS   := $(notdir $(wildcard claude/skills/*))
CODEX_SKILLS    := $(notdir $(wildcard codex/skills/*))

BATS       ?= bats

.PHONY: install link uninstall test test-tier1 test-tier2 lint check-bats

install:
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	install -m 0755 fanout "$(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do \
		install -m 0644 "claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CLAUDE_SKILL_DIR)/$$skill"; \
		cp -R "claude/skills/$$skill/." "$(CLAUDE_SKILL_DIR)/$$skill/"; \
	done
	@for skill in $(CODEX_SKILLS); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CODEX_SKILL_DIR)/$$skill"; \
		cp -R "codex/skills/$$skill/." "$(CODEX_SKILL_DIR)/$$skill/"; \
	done
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

link:
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	ln -sf "$(CURDIR)/fanout" "$(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do \
		ln -sf "$(CURDIR)/claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/claude/skills/$$skill" "$(CLAUDE_SKILL_DIR)/$$skill"; \
	done
	@for skill in $(CODEX_SKILLS); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/codex/skills/$$skill" "$(CODEX_SKILL_DIR)/$$skill"; \
	done
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd -> $(CURDIR)/claude/commands/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill -> $(CURDIR)/claude/skills/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill -> $(CURDIR)/codex/skills/$$skill"; done

uninstall:
	rm -f "$(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do rm -f "$(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do rm -rf "$(CODEX_SKILL_DIR)/$$skill"; done
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

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
