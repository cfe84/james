#!/bin/bash
set -euo pipefail

VERSION=$(cat VERSION)

docker build \
    --build-arg VERSION="${VERSION}" \
    -t james \
    .
