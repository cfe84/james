COMPONENTS := mi6 moneypenny hem

# Find first existing bin directory for install.
INSTALL_DIR := $(firstword $(wildcard $(HOME)/.bin $(HOME)/bin $(HOME)/.local/bin $(HOME)/local/bin))

.PHONY: all build test clean install $(COMPONENTS)

all: build

build: $(COMPONENTS)

mi6:
	$(MAKE) -C mi6 build

moneypenny:
	$(MAKE) -C moneypenny build

hem:
	$(MAKE) -C hem build

test:
	$(MAKE) -C mi6 test
	$(MAKE) -C moneypenny test
	$(MAKE) -C hem test

clean:
	$(MAKE) -C mi6 clean
	$(MAKE) -C moneypenny clean
	$(MAKE) -C hem clean

install: build
ifndef INSTALL_DIR
	$(error No bin directory found. Create one of: ~/.bin, ~/bin, ~/.local/bin, ~/local/bin)
endif
	@echo "Installing to $(INSTALL_DIR)"
	cp mi6/bin/mi6-server $(INSTALL_DIR)/
	cp mi6/bin/mi6-client $(INSTALL_DIR)/
	cp moneypenny/bin/moneypenny $(INSTALL_DIR)/
	cp hem/bin/hem $(INSTALL_DIR)/
	@echo "Installed: mi6-server mi6-client moneypenny hem"
