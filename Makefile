PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
# Claude Code reads its config root from CLAUDE_CONFIG_DIR when that is set, so
# honor it here too. Defaulting to ~/.claude regardless would install the gate
# somewhere Claude never loads, and the "rerun make link" remediation would keep
# reporting the same BLOCKED.
CLAUDE_DIR ?= $(if $(CLAUDE_CONFIG_DIR),$(CLAUDE_CONFIG_DIR),$(HOME)/.claude)
CODEX_HOME ?= $(HOME)/.codex
CODEX_HOME_EFFECTIVE := $(if $(strip $(CODEX_HOME)),$(CODEX_HOME),$(HOME)/.codex)
CODEX_DIR  ?= $(CODEX_HOME_EFFECTIVE)
CLAUDE_CMD_DIR   := $(CLAUDE_DIR)/commands
CLAUDE_SKILL_DIR := $(CLAUDE_DIR)/skills
CODEX_SKILL_DIR  := $(CODEX_DIR)/skills
CLAUDE_COMMANDS := $(notdir $(wildcard claude/commands/*.md))
CLAUDE_SKILLS   := $(notdir $(wildcard claude/skills/*))
CODEX_SKILLS    := $(notdir $(wildcard codex/skills/*))
# The Claude post-work-review gate reviews the very checkout it would be
# installed from, so a branch that edits the skill would install the gate that
# then judges that branch — symlink or copy alike. Checkout make targets
# therefore never touch it, exactly as they never touch the Codex gate; the
# checksum-verified release installer owns both.
CLAUDE_MAKE_SKILLS := $(filter-out post-work-review,$(CLAUDE_SKILLS))

BATS       ?= bats
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

.PHONY: install link uninstall build-go build-go-for-install build-web clean-go clean-web guard-retired-codex-review install-integrations link-integrations uninstall-integrations go-test test test-web test-tier1 test-tier2 check check-marker lint lint-go lint-shell lint-web fmt fmt-web fix vuln check-bats review-risk prepare-dev-cache

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

# The Codex post-work-review gate is installed only by the checksum-verified
# release installer. A target Makefile must never replace that boundary.
guard-retired-codex-review:
	@set -eu; \
		for review_root in "$(CODEX_DIR)" "$(CODEX_HOME_EFFECTIVE)"; do \
			[ -n "$$review_root" ] || { echo "error: CODEX_DIR and effective CODEX_HOME must not be empty" >&2; exit 1; }; \
			driver="$$review_root/tools/post-work-review.sh"; \
			if [ -e "$$driver" ] || [ -L "$$driver" ]; then \
				echo "error: retired Codex post-work-review driver remains at $$driver" >&2; \
				echo "Set CODEX_DIR and CODEX_HOME to that root, then run fanout update without --no-skills." >&2; \
				exit 1; \
			fi; \
		done

build-go-for-install: guard-retired-codex-review
	@$(MAKE) --no-print-directory build-go

install-integrations: guard-retired-codex-review
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	@for cmd in $(CLAUDE_COMMANDS); do \
		install -m 0644 "claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_MAKE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CLAUDE_SKILL_DIR)/$$skill"; \
		cp -R "claude/skills/$$skill/." "$(CLAUDE_SKILL_DIR)/$$skill/"; \
	done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		mkdir -p "$(CODEX_SKILL_DIR)/$$skill"; \
		cp -R "codex/skills/$$skill/." "$(CODEX_SKILL_DIR)/$$skill/"; \
	done

link-integrations: guard-retired-codex-review
	@mkdir -p "$(CLAUDE_CMD_DIR)" "$(CLAUDE_SKILL_DIR)" "$(CODEX_SKILL_DIR)"
	@for cmd in $(CLAUDE_COMMANDS); do \
		ln -sf "$(CURDIR)/claude/commands/$$cmd" "$(CLAUDE_CMD_DIR)/$$cmd"; \
	done
	@for skill in $(CLAUDE_MAKE_SKILLS); do \
		rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/claude/skills/$$skill" "$(CLAUDE_SKILL_DIR)/$$skill"; \
	done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do \
		rm -rf "$(CODEX_SKILL_DIR)/$$skill"; \
		ln -sf "$(CURDIR)/codex/skills/$$skill" "$(CODEX_SKILL_DIR)/$$skill"; \
	done

uninstall-integrations:
	@for cmd in $(CLAUDE_COMMANDS); do rm -f "$(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_MAKE_SKILLS); do rm -rf "$(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do rm -rf "$(CODEX_SKILL_DIR)/$$skill"; done

install: build-go-for-install install-integrations
	@mkdir -p "$(BINDIR)"
	install -m 0755 "$(GO_BIN)" "$(BINDIR)/fanout"
	@echo "Installed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_MAKE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

link: build-go-for-install link-integrations
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/$(GO_BIN)" "$(BINDIR)/fanout"
	@echo "Linked:"
	@echo "  $(BINDIR)/fanout -> $(CURDIR)/$(GO_BIN)"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd -> $(CURDIR)/claude/commands/$$cmd"; done
	@for skill in $(CLAUDE_MAKE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill -> $(CURDIR)/claude/skills/$$skill"; done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do \
		echo "  $(CODEX_SKILL_DIR)/$$skill -> $(CURDIR)/codex/skills/$$skill"; \
	done

uninstall: uninstall-integrations
	rm -f "$(BINDIR)/fanout"
	@echo "Removed:"
	@echo "  $(BINDIR)/fanout"
	@for cmd in $(CLAUDE_COMMANDS); do echo "  $(CLAUDE_CMD_DIR)/$$cmd"; done
	@for skill in $(CLAUDE_MAKE_SKILLS); do echo "  $(CLAUDE_SKILL_DIR)/$$skill"; done
	@for skill in $(filter-out post-work-review,$(CODEX_SKILLS)); do echo "  $(CODEX_SKILL_DIR)/$$skill"; done

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
# check-marker is a separate recipe line, not a goal of the same submake: a
# failed gate line stops the recipe even under `make -k`, so the push-gate
# marker is only written when every gate goal passed.
check:
	+$(MAKE) -j1 --no-print-directory test lint lint-web
	+$(MAKE) --no-print-directory check-marker

# Internal: records the validated HEAD into the per-worktree marker
# $(git rev-parse --git-dir)/fanout-check-passed, which scripts/agent-push-gate.sh
# compares against the pushed tip. Clean tree only: a dirty tree means the
# validated content is not the commit that would be pushed. Running this
# target without `check` defeats the push gate — for a deliberate bypass use
# FANOUT_SKIP_PUSH_CHECK=1 instead.
check-marker:
	@set -eu; \
		git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0; \
		status_out="$$(git status --porcelain -uall)" || { \
			echo "error: git status failed; push-gate marker not written." >&2; \
			exit 1; \
		}; \
		if [ -n "$$status_out" ]; then \
			echo "warning: working tree is dirty; push-gate marker not written." >&2; \
			echo "         commit the candidate and rerun make check to unlock git push." >&2; \
			exit 0; \
		fi; \
		git rev-parse HEAD >"$$(git rev-parse --git-dir)/fanout-check-passed"

test-tier1: build-go check-bats
	FANOUT_BIN="$(CURDIR)/$(GO_BIN)" $(BATS) tests/bats/tier1_flags.bats tests/bats/tier1_msg.bats tests/bats/tier1_pr_watch.bats tests/bats/tier1_agent_hooks.bats tests/bats/tier1_post_work_review.bats

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
	shellcheck tests/bin/gh tests/bin/tmux tests/bin/git tests/bats/helpers.bash codex/skills/post-work-review/scripts/mark-reviewed-head.sh codex/skills/pr-watch/scripts/watch-pr.sh scripts/agent-hooks-lib.sh scripts/agent-push-gate.sh scripts/agent-stop-gate.sh scripts/agent-format-on-edit.sh

fmt: $(GOLANGCI_LINT_BIN)
	$(GO_CACHE_ENV) GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" "$(GOLANGCI_LINT_BIN)" fmt

fix: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) fix ./...

vuln: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) tool govulncheck ./...

review-risk: | prepare-dev-cache
	$(GO_CACHE_ENV) $(GO) run ./tools/reviewrisk
