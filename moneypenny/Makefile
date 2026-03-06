VERSION := $(shell cat ../VERSION)
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

export CGO_ENABLED=1

.PHONY: all build test clean

all: build

build:
	go build $(LDFLAGS) -o bin/moneypenny ./cmd/moneypenny/

test:
	go test ./... -timeout 60s

clean:
	rm -rf bin/
