#!/bin/bash
set -euo pipefail

VERSION=$(cat VERSION)

# Build context must be project root since the Dockerfile copies from
# both moneypenny/ and mi6/ (for mi6-client).
docker build \
    --build-arg VERSION="${VERSION}" \
    -f moneypenny/Dockerfile \
    -t moneypenny \
    .
