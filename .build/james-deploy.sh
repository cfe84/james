#!/bin/bash
set -euo pipefail

CONTAINER_NAME="james"
NETWORK_NAME="caddy"

echo "Stopping existing ${CONTAINER_NAME} container..."
docker stop "${CONTAINER_NAME}" 2>/dev/null || true
docker rm "${CONTAINER_NAME}" 2>/dev/null || true

echo "Starting ${CONTAINER_NAME} container..."
docker create \
    --name "${CONTAINER_NAME}" \
    --restart unless-stopped \
    -p "${QEW_PORT:-8077}:8077" \
    -e HEM_MI6_URL="${HEM_MI6_URL}" \
    ${QEW_PASSWORD:+-e QEW_PASSWORD="${QEW_PASSWORD}"} \
    -v "${JAMES_CONFIG_PATH}:/root/.config/james" \
    james

docker network connect --alias james $NETWORK_NAME $CONTAINER_NAME

docker start "${CONTAINER_NAME}"

echo "${CONTAINER_NAME} container started."
