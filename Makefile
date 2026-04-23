PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CMD_DIR    := $(CLAUDE_DIR)/commands
SKILL_DIR  := $(CLAUDE_DIR)/skills

BATS       ?= bats

.PHONY: install link uninstall test test-tier1 test-tier2 lint check-bats

install:
	@mkdir -p "$(BINDIR)" "$(CMD_DIR)" "$(SKILL_DIR)/fanout"
	install -m 0755 fanout "$(BINDIR)/fanout"
	install -m 0644 claude/commands/fanout.md "$(CMD_DIR)/fanout.md"
	install -m 0644 claude/skills/fanout/SKILL.md "$(SKILL_DIR)/fanout/SKILL.md"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@echo "  $(CMD_DIR)/fanout.md"
	@echo "  $(SKILL_DIR)/fanout/SKILL.md"

link:
	@mkdir -p "$(BINDIR)" "$(CMD_DIR)" "$(SKILL_DIR)"
	ln -sf "$(CURDIR)/fanout" "$(BINDIR)/fanout"
	ln -sf "$(CURDIR)/claude/commands/fanout.md" "$(CMD_DIR)/fanout.md"
	rm -rf "$(SKILL_DIR)/fanout"
	ln -sf "$(CURDIR)/claude/skills/fanout" "$(SKILL_DIR)/fanout"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/fanout"
	@echo "  $(CMD_DIR)/fanout.md -> $(CURDIR)/claude/commands/fanout.md"
	@echo "  $(SKILL_DIR)/fanout -> $(CURDIR)/claude/skills/fanout"

uninstall:
	rm -f "$(BINDIR)/fanout"
	rm -f "$(CMD_DIR)/fanout.md"
	rm -rf "$(SKILL_DIR)/fanout"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@echo "  $(CMD_DIR)/fanout.md"
	@echo "  $(SKILL_DIR)/fanout"

# --- test / lint -------------------------------------------------------------
# `make test`         — run Tier 1 + Tier 2 black-box tests.
# `make test-tier1`   — flag / prerequisite tests, no live dmux.
# `make test-tier2`   — --dry-run golden tests against fixture scenarios.
# `make lint`         — shellcheck the fanout script and all test shims.
#
# bats-core is required: `brew install bats-core` (macOS) or `apt install bats`
# (Debian/Ubuntu). check-bats prints the install hint before failing.
#
# Tier 2 goldens can be regenerated with:
#   FANOUT_GOLDEN_UPDATE=1 make test-tier2
# Review the diff in git before committing.

check-bats:
	@command -v $(BATS) >/dev/null 2>&1 || { \
	  echo "error: bats-core not installed." >&2; \
	  echo "  macOS: brew install bats-core" >&2; \
	  echo "  Linux: apt-get install bats  (or: npm install -g bats)" >&2; \
	  exit 1; \
	}

test: test-tier1 test-tier2

test-tier1: check-bats
	$(BATS) tests/bats/tier1_flags.bats

test-tier2: check-bats
	$(BATS) tests/bats/tier2_dry_run.bats

lint:
	shellcheck fanout tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash
