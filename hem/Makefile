VERSION := $(shell type ..\VERSION 2>nul || cat ../VERSION 2>/dev/null || echo 0.1.0)
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

export CGO_ENABLED=1

# Add .exe extension on Windows
ifeq ($(OS),Windows_NT)
    EXE := .exe
else
    EXE :=
endif

.PHONY: all build test clean

all: build

build:
	go build $(LDFLAGS) -o bin/hem$(EXE) ./cmd/hem/

test:
	go test ./... -timeout 60s

clean:
	rm -rf bin/
