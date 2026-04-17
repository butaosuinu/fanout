PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: install link uninstall

install:
	@mkdir -p "$(BINDIR)"
	install -m 0755 fanout "$(BINDIR)/fanout"
	@echo "Installed $(BINDIR)/fanout"

link:
	@mkdir -p "$(BINDIR)"
	ln -sf "$(CURDIR)/fanout" "$(BINDIR)/fanout"
	@echo "Linked $(BINDIR)/fanout -> $(CURDIR)/fanout"

uninstall:
	rm -f "$(BINDIR)/fanout"
	@echo "Removed $(BINDIR)/fanout"
