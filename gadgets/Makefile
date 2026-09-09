ifeq ($(OS),Windows_NT)
    VERSION := $(shell type ..\VERSION 2>NUL || echo unknown)
    EXE := .exe
else
    VERSION := $(shell cat ../VERSION 2>/dev/null || echo unknown)
    EXE :=
endif
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

export CGO_ENABLED=0

.PHONY: all build test clean
all: build

build:
	go build $(LDFLAGS) -o bin/gadgets$(EXE) ./cmd/gadgets/

test:
	go test ./... -timeout 60s

clean:
ifeq ($(OS),Windows_NT)
	if exist bin rmdir /s /q bin
else
	rm -rf bin/
endif
