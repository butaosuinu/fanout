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
GO_BIN     ?= fanout-go
GOCACHE    ?= $(CURDIR)/.cache/go-build

.PHONY: install link uninstall install-go link-go install-go-default link-go-default uninstall-go build-go clean-go install-integrations link-integrations uninstall-integrations go-test go-vet go-fmt-check test test-tier1 test-tier2 test-go test-go-tier1 test-go-tier2 lint check-bats

build-go:
	GOCACHE="$(GOCACHE)" $(GO) build -o "$(GO_BIN)" ./cmd/fanout

clean-go:
	rm -f "$(GO_BIN)"

install-integrations:
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
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

link-integrations:
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
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

uninstall-integrations:
	@for cmd in $(CLAUDE_COMMANDS); do rm -f "$(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do rm -rf "$(CODEX_SKILL_DIR)/$$skill"; done

install: build-go install-integrations
	@mkdir -p "$(BINDIR)"
	install -m 0755 "$(GO_BIN)" "$(BINDIR)/fanout"
	install -m 0755 fanout "$(BINDIR)/fanout-bash"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout (Go)"
	@echo "  $(BINDIR)/fanout-bash (Bash)"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

install-go: build-go
	@mkdir -p "$(BINDIR)"
	install -m 0755 "$(GO_BIN)" "$(BINDIR)/fanout-go"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout-go"

# install-go-default / link-go-default are now aliases: the default install / link
# targets already place the Go binary at $(BINDIR)/fanout and demote Bash to
# $(BINDIR)/fanout-bash. Kept for call-site compatibility (CI, docs, agent skills).
install-go-default: install

link: build-go link-integrations
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/$(GO_BIN)" "$(BINDIR)/fanout"
	ln -sf "$(CURDIR)/fanout" "$(BINDIR)/fanout-bash"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/$(GO_BIN)"
	@echo "  $(BINDIR)/fanout-bash -> $(CURDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd -> $(CURDIR)/claude/commands/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill -> $(CURDIR)/claude/skills/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill -> $(CURDIR)/codex/skills/$$skill"; done

link-go: build-go
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/$(GO_BIN)" "$(BINDIR)/fanout-go"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout-go -> $(CURDIR)/$(GO_BIN)"

link-go-default: link

uninstall: uninstall-integrations
	rm -f "$(BINDIR)/fanout" "$(BINDIR)/fanout-bash"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@echo "  $(BINDIR)/fanout-bash"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

uninstall-go:
	rm -f "$(BINDIR)/fanout-go"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout-go"

# --- test / lint -------------------------------------------------------------
# `make test`         — run Tier 1 + Tier 2 black-box tests against ./fanout.
# `make test-go`      — build ./fanout-go and run the same tests via FANOUT_BIN.
# `make test-tier1`   — flag / prerequisite tests, no live dmux.
# `make test-tier2`   — --dry-run golden tests against fixture scenarios.
# `make lint`         — shellcheck Bash files plus Go vet/gofmt checks.
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
	$(BATS) tests/bats/tier1_flags.bats tests/bats/tier1_briefing.bats

test-tier2: check-bats
	$(BATS) tests/bats/tier2_dry_run.bats tests/bats/tier2_status.bats

test-go: go-test test-go-tier1 test-go-tier2

test-go-tier1: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier1_flags.bats tests/bats/tier1_briefing.bats

test-go-tier2: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier2_dry_run.bats tests/bats/tier2_status.bats

go-test:
	GOCACHE="$(GOCACHE)" $(GO) test ./...

go-vet:
	GOCACHE="$(GOCACHE)" $(GO) vet ./...

go-fmt-check:
	@out="$$(gofmt -l cmd internal 2>/dev/null)"; \
	if [ -n "$$out" ]; then echo "gofmt diff in:"; echo "$$out"; exit 1; fi

lint: go-vet go-fmt-check
	shellcheck fanout tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash
