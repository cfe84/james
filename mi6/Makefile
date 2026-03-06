VERSION := $(shell cat ../VERSION 2>/dev/null || type ..\VERSION 2>NUL || echo 0.1.0)
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

# Add .exe extension on Windows
ifeq ($(OS),Windows_NT)
    EXE := .exe
else
    EXE :=
endif

.PHONY: all build server client test clean docker

all: build

build: server client

server:
	go build $(LDFLAGS) -o bin/mi6-server$(EXE) ./cmd/mi6-server/

client:
	go build $(LDFLAGS) -o bin/mi6-client$(EXE) ./cmd/mi6-client/

test:
	go test ./... -timeout 60s

clean:
	rm -rf bin/

docker:
	docker build -t mi6-server .
