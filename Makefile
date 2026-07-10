PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CODEX_DIR  ?= $(HOME)/.codex
CLAUDE_CMD_DIR   := $(CLAUDE_DIR)/commands
CLAUDE_SKILL_DIR := $(CLAUDE_DIR)/skills
CLAUDE_AGENT_DIR := $(CLAUDE_DIR)/agents
CODEX_SKILL_DIR  := $(CODEX_DIR)/skills
CODEX_TOOL_DIR   := $(CODEX_DIR)/tools
CODEX_AGENT_DIR  := $(CODEX_DIR)/agents
CLAUDE_COMMANDS := $(notdir $(wildcard claude/commands/*.md))
CLAUDE_SKILLS   := $(notdir $(wildcard claude/skills/*))
CLAUDE_AGENTS   := $(notdir $(wildcard claude/skills/post-work-review/agents/*.md))
CODEX_SKILLS    := $(notdir $(wildcard codex/skills/*))
CODEX_TOOLS     := $(notdir $(wildcard codex/tools/*))
CODEX_AGENTS    := $(notdir $(wildcard codex/agents/*))

BATS       ?= bats
BATS_JOBS  ?= 4
GO         ?= go
GO_BIN     ?= fanout-go

PNPM       ?= pnpm
WEB_DIR    := web
STATIC_DIR := internal/ui/dashboard/static

# .golangci-lint-version is the single source for the pinned golangci-lint
# version; CI (golangci-lint-action in .github/workflows/test.yml) reads the
# same file via `version-file`.
GOLANGCI_LINT_VERSION ?= $(shell cat .golangci-lint-version)
GOLANGCI_LINT_CACHE   ?= $(CURDIR)/.cache/golangci-lint

# Trusted local worktrees share download/build caches for the same user. Keep
# the default under the POSIX system temp root instead of TMPDIR: local agent
# sandboxes may point TMPDIR inside the worktree. Override FANOUT_DEV_CACHE_DIR
# to relocate it. CI keeps the native caches restored by setup-go/setup-node
# and the lint action.
FANOUT_DEV_CACHE_DIR ?= /tmp/fanout-dev-cache-$(shell id -u)
ifeq ($(strip $(CI)),)
GOCACHE           ?= $(FANOUT_DEV_CACHE_DIR)/go-build
PNPM_STORE_DIR    ?= $(FANOUT_DEV_CACHE_DIR)/pnpm-store
GOLANGCI_LINT_BIN ?= $(FANOUT_DEV_CACHE_DIR)/tools/golangci-lint-$(GOLANGCI_LINT_VERSION)
else
GOLANGCI_LINT_BIN ?= $(CURDIR)/.cache/tools/golangci-lint-$(GOLANGCI_LINT_VERSION)
endif

GO_CACHE_ENV   = $(if $(strip $(GOCACHE)),GOCACHE="$(GOCACHE)")
PNPM_STORE_ARG = $(if $(strip $(PNPM_STORE_DIR)),--store-dir "$(PNPM_STORE_DIR)")

POST_WORK_REVIEW_BATS := tests/bats/tier1_post_work_review.bats
# The post-work-review cases are weighted across more shards than workers so
# the longest git-heavy cases start first and shorter shards fill freed slots.
POST_WORK_REVIEW_SHARDS := 1 2 3 4 5 6 7 8 9 10 11 12
POST_WORK_REVIEW_SHARD_TARGETS := $(addprefix test-tier1-post-work-review-shard-,$(POST_WORK_REVIEW_SHARDS))

.PHONY: install link uninstall build-go build-web clean-go clean-web install-integrations link-integrations uninstall-integrations go-test test test-web test-tier1 test-tier2 check lint lint-go lint-shell lint-web fmt fmt-web fix vuln check-bats review-risk prepare-dev-cache $(POST_WORK_REVIEW_SHARD_TARGETS)

# The local default lives under predictable /tmp on supported macOS and Linux
# hosts. Reject a pre-created symlink or a directory owned by another user
# before trusting cached tools. chmod also keeps later cache contents private
# on multi-user hosts.
prepare-dev-cache:
ifeq ($(strip $(CI)),)
	@set -eu; \
		cache="$(FANOUT_DEV_CACHE_DIR)"; \
		if [ -L "$$cache" ]; then \
			echo "error: fanout dev cache must not be a symlink: $$cache" >&2; \
			exit 1; \
		fi; \
		old_umask=$$(umask); \
		umask 077; \
		mkdir -p "$$cache"; \
		umask "$$old_umask"; \
		if [ -L "$$cache" ]; then \
			echo "error: fanout dev cache must not be a symlink: $$cache" >&2; \
			exit 1; \
		fi; \
		if owner_uid=$$(stat -f '%u' "$$cache" 2>/dev/null); then \
			:; \
		elif owner_uid=$$(stat -c '%u' "$$cache" 2>/dev/null); then \
			:; \
		else \
			echo "error: cannot inspect fanout dev cache owner: $$cache" >&2; \
			exit 1; \
		fi; \
		if [ "$$owner_uid" != "$$(id -u)" ]; then \
			echo "error: fanout dev cache is owned by uid $$owner_uid: $$cache" >&2; \
			exit 1; \
		fi; \
		chmod 700 "$$cache"
endif

# The dashboard web UI (web/, React + Vite) is built into $(STATIC_DIR) and
# embedded via go:embed. The bundle is never committed, so building the binary
# needs Node.js + pnpm (see web/.nvmrc / web/package.json packageManager).
$(WEB_DIR)/node_modules/.installed: $(WEB_DIR)/package.json $(WEB_DIR)/pnpm-lock.yaml | prepare-dev-cache
	cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile $(PNPM_STORE_ARG)
	@touch $@

build-web: $(WEB_DIR)/node_modules/.installed
	@# emptyOutDir:false keeps .gitkeep alive, so sweep stale assets ourselves.
	find $(STATIC_DIR) -type f ! -name '.gitkeep' -delete
	cd $(WEB_DIR) && $(PNPM) run build

build-go: build-web | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) build -o "$(GO_BIN)" ./cmd/fanout

clean-go:
	rm -f "$(GO_BIN)"

clean-web:
	rm -rf $(WEB_DIR)/node_modules
	find $(STATIC_DIR) -type f ! -name '.gitkeep' -delete

install-integrations:
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CLAUDE_AGENT_DIR)" "$(CODEX_SKILL_DIR)" "$(CODEX_TOOL_DIR)" "$(CODEX_AGENT_DIR)"
	@for cmd in $(CLAUDE_COMMANDS); do \
		install -m 0644 "claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CLAUDE_SKILL_DIR)/$$skill"; \
		cp -R "claude/skills/$$skill/." "$(CLAUDE_SKILL_DIR)/$$skill/"; \
	done
	@for agent in $(CLAUDE_AGENTS); do \
		rm -f "$(CLAUDE_AGENT_DIR)/$$agent"; \
		install -m 0644 "claude/skills/post-work-review/agents/$$agent" "$(CLAUDE_AGENT_DIR)/$$agent"; \
	done
	@for skill in $(CODEX_SKILLS); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CODEX_SKILL_DIR)/$$skill"; \
		cp -R "codex/skills/$$skill/." "$(CODEX_SKILL_DIR)/$$skill/"; \
	done
	@for tool in $(CODEX_TOOLS); do \
		install -m 0755 "codex/tools/$$tool" "$(CODEX_TOOL_DIR)/$$tool"; \
	done
	@for agent in $(CODEX_AGENTS); do \
		install -m 0644 "codex/agents/$$agent" "$(CODEX_AGENT_DIR)/$$agent"; \
	done

link-integrations:
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CLAUDE_AGENT_DIR)" "$(CODEX_SKILL_DIR)" "$(CODEX_TOOL_DIR)" "$(CODEX_AGENT_DIR)"
	@for cmd in $(CLAUDE_COMMANDS); do \
		ln -sf "$(CURDIR)/claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/claude/skills/$$skill" "$(CLAUDE_SKILL_DIR)/$$skill"; \
	done
	@for agent in $(CLAUDE_AGENTS); do \
		ln -sf "$(CURDIR)/claude/skills/post-work-review/agents/$$agent" "$(CLAUDE_AGENT_DIR)/$$agent"; \
	done
	@for skill in $(CODEX_SKILLS); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/codex/skills/$$skill" "$(CODEX_SKILL_DIR)/$$skill"; \
	done
	@for tool in $(CODEX_TOOLS); do \
		rm -f "$(CODEX_TOOL_DIR)/$$tool"; \
		ln -sf "$(CURDIR)/codex/tools/$$tool" "$(CODEX_TOOL_DIR)/$$tool"; \
	done
	@for agent in $(CODEX_AGENTS); do \
		rm -f "$(CODEX_AGENT_DIR)/$$agent"; \
		ln -sf "$(CURDIR)/codex/agents/$$agent" "$(CODEX_AGENT_DIR)/$$agent"; \
	done

uninstall-integrations:
	@for cmd in $(CLAUDE_COMMANDS); do rm -f "$(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; done
	@for agent in $(CLAUDE_AGENTS); do rm -f "$(CLAUDE_AGENT_DIR)/$$agent"; done
	@for skill in $(CODEX_SKILLS); do rm -rf "$(CODEX_SKILL_DIR)/$$skill"; done
	@for tool in $(CODEX_TOOLS); do rm -f "$(CODEX_TOOL_DIR)/$$tool"; done
	@for agent in $(CODEX_AGENTS); do rm -f "$(CODEX_AGENT_DIR)/$$agent"; done

install: build-go install-integrations
	@mkdir -p "$(BINDIR)"
	install -m 0755 "$(GO_BIN)" "$(BINDIR)/fanout"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for agent in $(CLAUDE_AGENTS); do echo "  $(CLAUDE_AGENT_DIR)/$$agent"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done
	@for tool in $(CODEX_TOOLS); do echo "  $(CODEX_TOOL_DIR)/$$tool"; done
	@for agent in $(CODEX_AGENTS); do echo "  $(CODEX_AGENT_DIR)/$$agent"; done

link: build-go link-integrations
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/$(GO_BIN)" "$(BINDIR)/fanout"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/$(GO_BIN)"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd -> $(CURDIR)/claude/commands/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill -> $(CURDIR)/claude/skills/$$skill"; done
	@for agent in $(CLAUDE_AGENTS); do echo "  $(CLAUDE_AGENT_DIR)/$$agent -> $(CURDIR)/claude/skills/post-work-review/agents/$$agent"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill -> $(CURDIR)/codex/skills/$$skill"; done
	@for tool in $(CODEX_TOOLS); do echo "  $(CODEX_TOOL_DIR)/$$tool -> $(CURDIR)/codex/tools/$$tool"; done
	@for agent in $(CODEX_AGENTS); do echo "  $(CODEX_AGENT_DIR)/$$agent -> $(CURDIR)/codex/agents/$$agent"; done

uninstall: uninstall-integrations
	rm -f "$(BINDIR)/fanout"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for agent in $(CLAUDE_AGENTS); do echo "  $(CLAUDE_AGENT_DIR)/$$agent"; done
	@for skill in $(CODEX_SKILLS); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done
	@for tool in $(CODEX_TOOLS); do echo "  $(CODEX_TOOL_DIR)/$$tool"; done
	@for agent in $(CODEX_AGENTS); do echo "  $(CODEX_AGENT_DIR)/$$agent"; done

# --- test / lint -------------------------------------------------------------
# `make test`         — build the Go binary, run Go unit tests + web UI tests
#                       (vitest) + Tier 1 + Tier 2 black-box tests against it
#                       via FANOUT_BIN.
# `make check`        — the deterministic local CI gate: `test`, `lint`, and
#                       `lint-web`, with shared prerequisites run once.
# `make test-web`     — dashboard web UI tests (vitest; needs Node + pnpm).
# `make lint-web`     — dashboard web UI lint: oxlint + oxfmt --check + type
#                       check (tsc --noEmit), cheap-first. Needs Node + pnpm;
#                       `make lint` stays Node-free on purpose.
# `make fmt-web`      — dashboard web UI formatting (oxfmt; web/.oxfmtrc.json).
# `make test-tier1`   — flag / prerequisite tests, no live tmux panes.
# `make test-tier2`   — --dry-run golden tests against fixture scenarios.
# `make lint`         — pinned golangci-lint v2 (.golangci.yml) plus shellcheck
#                       of shell shims and repo-local review tools.
# `make fmt`          — gofumpt/goimports formatting via `golangci-lint fmt`.
# `make fix`          — Go 1.26 revamped `go fix` (modernizer + //go:fix
#                       inline); run `make test` after applying.
# `make vuln`         — govulncheck via the go.mod tool directive. Kept out of
#                       `lint` on purpose: it fetches the vulnerability DB over
#                       the network and can fail on new CVEs without any code
#                       change, so it is not a deterministic lint gate.
# `make review-risk`   — PR review risk level for the working diff (see
#                       docs/review-risk.ja.md); CI runs the same tool.
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

# build-web first: go-test embeds whatever is in $(STATIC_DIR), so the asset
# smoke tests must see a fresh bundle (not a skip, not a stale local build).
test: build-web go-test test-web test-tier1 test-tier2

# Serialize even when the caller passes -j: go-test must see build-web's fresh
# go:embed inputs. One submake also de-duplicates the shared phony prerequisites.
check:
	+$(MAKE) -j1 --no-print-directory test lint lint-web

test-tier1: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier1_flags.bats tests/bats/tier1_msg.bats tests/bats/tier1_pr_watch.bats
	@set -eu; \
		all=$$($(BATS) --count "$(POST_WORK_REVIEW_BATS)"); \
		assigned=0; \
		for shard in $(POST_WORK_REVIEW_SHARDS); do \
			count=$$($(BATS) --count --filter "post-work-review shard-$$shard:" "$(POST_WORK_REVIEW_BATS)"); \
			assigned=$$((assigned + count)); \
		done; \
		if [ "$$assigned" -ne "$$all" ]; then \
			echo "error: post-work-review Bats shard coverage is $$assigned/$$all" >&2; \
			exit 1; \
		fi
	+$(MAKE) --no-print-directory -j$(BATS_JOBS) $(POST_WORK_REVIEW_SHARD_TARGETS)

$(POST_WORK_REVIEW_SHARD_TARGETS): test-tier1-post-work-review-shard-%:
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) --filter "post-work-review shard-$*:" "$(POST_WORK_REVIEW_BATS)"

test-tier2: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier2_dry_run.bats tests/bats/tier2_status.bats tests/bats/tier2_msg.bats

# go-test alone stays Node-free: the embedded-asset smoke tests skip themselves
# when the bundle is absent. `make test` and CI's go-unit job order `build-web`
# first so those tests actually run against a fresh bundle.
go-test: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) test ./...

test-web: $(WEB_DIR)/node_modules/.installed
	cd $(WEB_DIR) && $(PNPM) run test

# Cheap-first: oxlint (ms) -> oxfmt --check (ms) -> tsc (s).
lint-web: $(WEB_DIR)/node_modules/.installed
	cd $(WEB_DIR) && $(PNPM) run lint && $(PNPM) run fmt:check && $(PNPM) run typecheck

fmt-web: $(WEB_DIR)/node_modules/.installed
	cd $(WEB_DIR) && $(PNPM) run fmt

# Binary install because upstream does not guarantee `go install`; the URL is
# pinned to the release tag.
$(GOLANGCI_LINT_BIN): | prepare-dev-cache
	@set -eu; \
		mkdir -p "$(dir $@)"; \
		tmp_dir=$$(mktemp -d "$(dir $@).golangci-lint-install.XXXXXX"); \
		trap 'rm -rf "$$tmp_dir"' EXIT HUP INT TERM; \
		curl -sSfL "https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh" \
			| sh -s -- -b "$$tmp_dir" "$(GOLANGCI_LINT_VERSION)"; \
		mv "$$tmp_dir/golangci-lint" "$@"; \
		rm -rf "$$tmp_dir"; \
		trap - EXIT HUP INT TERM

lint: lint-go lint-shell

lint-go: $(GOLANGCI_LINT_BIN)
	$(GO_CACHE_ENV) GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" "$(GOLANGCI_LINT_BIN)" run

lint-shell:
	shellcheck tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash codex/tools/post-work-review.sh codex/skills/pr-watch/scripts/watch-pr.sh

fmt: $(GOLANGCI_LINT_BIN)
	$(GO_CACHE_ENV) GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" "$(GOLANGCI_LINT_BIN)" fmt

fix: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) fix ./...

vuln: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) tool govulncheck ./...

review-risk: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) run ./tools/reviewrisk
