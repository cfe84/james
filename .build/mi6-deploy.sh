#!/bin/bash
set -euo pipefail

CONTAINER_NAME="mi6"

echo "Stopping existing ${CONTAINER_NAME} container..."
docker stop "${CONTAINER_NAME}" 2>/dev/null || true
docker rm "${CONTAINER_NAME}" 2>/dev/null || true

echo "Starting ${CONTAINER_NAME} container..."
docker create \
    --name "${CONTAINER_NAME}" \
    --restart unless-stopped \
    -p 7007:7007 \
    -v "${CONFIG}:/etc/mi6" \
    mi6

docker start "${CONTAINER_NAME}"

echo "${CONTAINER_NAME} container started."
