PREFIX     ?= $(HOME)/.local
BINDIR     ?= $(PREFIX)/bin
CLAUDE_DIR ?= $(HOME)/.claude
CMD_DIR    := $(CLAUDE_DIR)/commands
SKILL_DIR  := $(CLAUDE_DIR)/skills

.PHONY: install link uninstall

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
