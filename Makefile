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

# .golangci-lint-version is the single source for the pinned golangci-lint
# version; CI (golangci-lint-action in .github/workflows/test.yml) reads the
# same file via `version-file`.
GOLANGCI_LINT_VERSION ?= $(shell cat .golangci-lint-version)
GOLANGCI_LINT_BIN     := $(CURDIR)/.cache/tools/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT_CACHE   ?= $(CURDIR)/.cache/golangci-lint

.PHONY: install link uninstall build-go clean-go install-integrations link-integrations uninstall-integrations go-test test test-tier1 test-tier2 lint lint-go lint-shell fmt fix vuln check-bats

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
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

link: build-go link-integrations
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/$(GO_BIN)" "$(BINDIR)/fanout"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/$(GO_BIN)"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd -> $(CURDIR)/claude/commands/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill -> $(CURDIR)/claude/skills/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill -> $(CURDIR)/codex/skills/$$skill"; done

uninstall: uninstall-integrations
	rm -f "$(BINDIR)/fanout"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

# --- test / lint -------------------------------------------------------------
# `make test`         — build the Go binary, run Go unit tests + Tier 1 + Tier 2
#                       black-box tests against it via FANOUT_BIN.
# `make test-tier1`   — flag / prerequisite tests, no live tmux panes.
# `make test-tier2`   — --dry-run golden tests against fixture scenarios.
# `make lint`         — pinned golangci-lint v2 (.golangci.yml) plus shellcheck
#                       of the test shims.
# `make fmt`          — gofumpt/goimports formatting via `golangci-lint fmt`.
# `make fix`          — Go 1.26 revamped `go fix` (modernizer + //go:fix
#                       inline); run `make test` after applying.
# `make vuln`         — govulncheck via the go.mod tool directive. Kept out of
#                       `lint` on purpose: it fetches the vulnerability DB over
#                       the network and can fail on new CVEs without any code
#                       change, so it is not a deterministic lint gate.
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

test: go-test test-tier1 test-tier2

test-tier1: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier1_flags.bats tests/bats/tier1_briefing.bats

test-tier2: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier2_dry_run.bats tests/bats/tier2_status.bats

go-test:
	GOCACHE="$(GOCACHE)" $(GO) test ./...

# Binary install because upstream does not guarantee `go install`; the URL is
# pinned to the release tag.
$(GOLANGCI_LINT_BIN):
	@mkdir -p "$(dir $@)"
	curl -sSfL "https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh" \
		| sh -s -- -b "$(dir $@)" "$(GOLANGCI_LINT_VERSION)"
	@mv "$(dir $@)golangci-lint" "$@"

lint: lint-go lint-shell

lint-go: $(GOLANGCI_LINT_BIN)
	GOCACHE="$(GOCACHE)" GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" "$(GOLANGCI_LINT_BIN)" run

lint-shell:
	shellcheck tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash

fmt: $(GOLANGCI_LINT_BIN)
	GOCACHE="$(GOCACHE)" GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" "$(GOLANGCI_LINT_BIN)" fmt

fix:
	GOCACHE="$(GOCACHE)" $(GO) fix ./...

vuln:
	GOCACHE="$(GOCACHE)" $(GO) tool govulncheck ./...
