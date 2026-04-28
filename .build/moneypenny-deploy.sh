#!/bin/bash
set -euo pipefail

CONTAINER_NAME="moneypenny"

echo "Stopping existing ${CONTAINER_NAME} container..."
docker stop "${CONTAINER_NAME}" 2>/dev/null || true
docker rm "${CONTAINER_NAME}" 2>/dev/null || true

echo "Starting ${CONTAINER_NAME} container..."
docker create \
    --name "${CONTAINER_NAME}" \
    --restart unless-stopped \
    -e MP_MI6_ADDRESS="${MP_MI6_ADDRESS}" \
    ${MP_AUTO_UPDATE:+-e MP_AUTO_UPDATE="${MP_AUTO_UPDATE}"} \
    ${MP_UPDATE_INTERVAL:+-e MP_UPDATE_INTERVAL="${MP_UPDATE_INTERVAL}"} \
    ${MP_VERBOSE:+-e MP_VERBOSE="${MP_VERBOSE}"} \
    -v "${MP_SSH_PATH}:/home/mp/.ssh" \
    -v "${MP_CLAUDE_PATH}:/home/mp/.claude" \
    -v "${MP_CLAUDE_JSON_PATH}:/home/mp/.claude.json" \
    -v "${MP_DATA_PATH}:/data" \
    moneypenny

docker start "${CONTAINER_NAME}"

echo "${CONTAINER_NAME} container started."
