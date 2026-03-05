VERSION := $(shell cat ../VERSION)
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build server client test clean docker

all: build

build: server client

server:
	go build $(LDFLAGS) -o bin/mi6-server ./cmd/mi6-server/

client:
	go build $(LDFLAGS) -o bin/mi6-client ./cmd/mi6-client/

test:
	go test ./... -timeout 60s

clean:
	rm -rf bin/

docker:
	docker build -t mi6-server .
