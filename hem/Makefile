VERSION := $(shell cat ../VERSION)
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build test clean

all: build

build:
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/hem ./cmd/hem/

test:
	CGO_ENABLED=1 go test ./... -timeout 60s

clean:
	rm -rf bin/
