COMPONENTS := mi6 moneypenny hem
ifeq ($(OS),Windows_NT)
    VERSION := $(shell type VERSION 2>NUL || echo unknown)
else
    VERSION := $(shell cat VERSION 2>/dev/null || echo unknown)
endif

# Find first existing bin directory for install.
# On Windows: %LOCALAPPDATA%\Programs or %USERPROFILE%\bin
# On Unix: ~/.local/bin, ~/.bin, ~/bin, etc.
ifeq ($(OS),Windows_NT)
    LOCALAPPDATA_BIN := $(subst \,/,$(LOCALAPPDATA))/Programs
    USERPROFILE_BIN := $(subst \,/,$(USERPROFILE))/bin
    INSTALL_DIR := $(firstword $(wildcard $(LOCALAPPDATA_BIN) $(USERPROFILE_BIN)))
    ifeq ($(INSTALL_DIR),)
        INSTALL_DIR := $(LOCALAPPDATA_BIN)
    endif
else
    INSTALL_DIR := $(firstword $(wildcard $(HOME)/.local/bin $(HOME)/.bin $(HOME)/bin $(HOME)/local/bin))
endif

.PHONY: all build test clean install $(COMPONENTS)

all: build

build: $(COMPONENTS)
	@echo "Built james v$(VERSION): mi6-server mi6-client moneypenny hem"

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
ifeq ($(OS),Windows_NT)
	$(error No install directory found. INSTALL_DIR is not set)
else
	$(error No bin directory found. Create one of: ~/.local/bin, ~/.bin, ~/bin, ~/local/bin)
endif
endif
	@echo "Installing to $(INSTALL_DIR)"
	-@mkdir -p "$(INSTALL_DIR)"
ifeq ($(OS),Windows_NT)
	@install -m 755 mi6/bin/mi6-server.exe "$(INSTALL_DIR)/"
	@install -m 755 mi6/bin/mi6-client.exe "$(INSTALL_DIR)/"
	@install -m 755 moneypenny/bin/moneypenny.exe "$(INSTALL_DIR)/"
	@install -m 755 hem/bin/hem.exe "$(INSTALL_DIR)/"
	@echo "Installed to: $(INSTALL_DIR)"
	@echo "Make sure $(INSTALL_DIR) is in your PATH"
else
	@install -m 755 mi6/bin/mi6-server "$(INSTALL_DIR)/"
	@install -m 755 mi6/bin/mi6-client "$(INSTALL_DIR)/"
	@install -m 755 moneypenny/bin/moneypenny "$(INSTALL_DIR)/"
	@install -m 755 hem/bin/hem "$(INSTALL_DIR)/"
	@echo "Installed: mi6-server mi6-client moneypenny hem"
endif
