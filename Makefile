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
GO         ?= go
BUILD_BIN  ?= ./fanout

.PHONY: install link uninstall test test-tier1 test-tier2 lint check-bats build vet fmt-check clean

# --- build -------------------------------------------------------------------
# `make build` produces $(BUILD_BIN) (./fanout) from cmd/fanout. install / link
# / test all depend on it so the binary is always up-to-date with the source.

build:
	$(GO) build -o $(BUILD_BIN) ./cmd/fanout

clean:
	rm -f $(BUILD_BIN)

# --- install / link / uninstall ---------------------------------------------
# `make install` builds the Go binary and copies it + the agent integration
# files into ~/.local, ~/.claude, ~/.codex. `make link` does the same but
# symlinks each path back to the checkout for development. Both create the
# parent directories.

install: build
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	install -m 0755 $(BUILD_BIN) "$(BINDIR)/fanout"
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

link: build
	@mkdir -p "$(BINDIR)" "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	ln -sf "$(CURDIR)/$(BUILD_BIN)" "$(BINDIR)/fanout"
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
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/$(BUILD_BIN)"
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
# `make test`         — build + Tier 1 + Tier 2 black-box tests.
# `make test-tier1`   — flag / prerequisite tests, no live dmux.
# `make test-tier2`   — --dry-run golden tests against fixture scenarios.
# `make lint`         — go vet + gofmt + shellcheck the test shims.
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

test: build test-tier1 test-tier2

test-tier1: build check-bats
	FANOUT_BIN=$(CURDIR)/$(BUILD_BIN) $(BATS) tests/bats/tier1_flags.bats

test-tier2: build check-bats
	FANOUT_BIN=$(CURDIR)/$(BUILD_BIN) $(BATS) tests/bats/tier2_dry_run.bats

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l cmd internal 2>/dev/null); \
	if [ -n "$$out" ]; then echo "gofmt diff in:"; echo "$$out"; exit 1; fi

lint: vet fmt-check
	shellcheck tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash
