PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CODEX_DIR  ?= $(HOME)/.codex
CLAUDE_CMD_DIR   := $(CLAUDE_DIR)/commands
CLAUDE_SKILL_DIR := $(CLAUDE_DIR)/skills
CODEX_SKILL_DIR  := $(CODEX_DIR)/skills

.PHONY: install link uninstall

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
