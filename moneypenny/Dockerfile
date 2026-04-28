# Build context must be the project root (so we can access mi6/ as well).
# Build with: docker build -f moneypenny/Dockerfile -t moneypenny .

# Stage 1: Build moneypenny binary (CGO required for go-sqlite3)
FROM golang:1.25-alpine AS moneypenny-builder
RUN apk add --no-cache gcc musl-dev
ARG VERSION=dev
WORKDIR /build
COPY moneypenny/go.mod moneypenny/go.sum ./
RUN go mod download
COPY moneypenny/ .
RUN CGO_ENABLED=1 go build -ldflags "-X main.Version=${VERSION}" -o moneypenny ./cmd/moneypenny/

# Stage 2: Build mi6-client (needed for MI6 transport)
FROM golang:1.25-alpine AS mi6-builder
ARG VERSION=dev
WORKDIR /build
COPY mi6/go.mod mi6/go.sum ./
RUN go mod download
COPY mi6/ .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o mi6-client ./cmd/mi6-client/

# Stage 3: Final image with Node.js (for Claude Code) + moneypenny + mi6-client
FROM node:20-alpine

# System dependencies: ca-certificates for HTTPS, tzdata for time zones,
# git/openssh for agent operations, bash for the entrypoint and shell loading,
# su-exec for dropping privileges from root entrypoint to the mp user.
RUN apk add --no-cache ca-certificates tzdata git openssh-client bash su-exec

# Install Claude Code globally via npm
RUN npm install -g @anthropic-ai/claude-code

# Install moneypenny + mi6-client
COPY --from=moneypenny-builder /build/moneypenny /usr/local/bin/moneypenny
COPY --from=mi6-builder /build/mi6-client /usr/local/bin/mi6-client

# Create non-root user for running moneypenny + claude (Claude Code refuses
# to run as root in YOLO mode). UID/GID 1001 because node:20-alpine already
# uses 1000 for its built-in `node` user.
RUN addgroup -g 1001 mp && adduser -D -u 1001 -G mp -s /bin/bash mp

# Entrypoint runs as root to fix volume ownership, then drops to the mp user.
COPY moneypenny/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Persistent volume mount points (owned by mp user):
#   /home/mp/.ssh         - SSH keys (for git operations done by agents)
#   /home/mp/.claude      - Claude Code config (settings, sessions)
#   /home/mp/.claude.json - Claude Code login state / API token (single file)
#   /data                 - Moneypenny data (SQLite, keys, logs)
VOLUME ["/home/mp/.ssh", "/home/mp/.claude", "/data"]

# Configuration via environment variables.
# MP_MI6_ADDRESS is required: host[:port]/session_id of the MI6 relay.
ENV MP_MI6_ADDRESS=""
ENV MP_AUTO_UPDATE="true"
ENV MP_UPDATE_INTERVAL="15m"
ENV MP_VERBOSE=""

ENTRYPOINT ["/entrypoint.sh"]
