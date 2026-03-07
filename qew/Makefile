ifeq ($(OS),Windows_NT)
    VERSION := $(shell type ..\VERSION 2>NUL || echo unknown)
else
    VERSION := $(shell cat ../VERSION 2>/dev/null || echo unknown)
endif
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

ifeq ($(OS),Windows_NT)
    EXE := .exe
else
    EXE :=
endif

.PHONY: all build test clean

all: build

build:
	go build $(LDFLAGS) -o bin/qew$(EXE) ./cmd/qew/

test:
	go test ./... -timeout 60s

clean:
	rm -rf bin/
