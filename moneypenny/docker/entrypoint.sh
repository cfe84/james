#!/bin/sh
set -e

if [ -z "$MP_MI6_ADDRESS" ]; then
  echo "Error: MP_MI6_ADDRESS environment variable is required"
  echo "Set it to host[:port]/session_id of your MI6 relay"
  exit 1
fi

# Ensure mounted volumes are owned by the mp user (volumes from the host
# may be owned by a different UID).
chown -R mp:mp /home/mp/.ssh /home/mp/.claude /data 2>/dev/null || true

# Build moneypenny args.
MP_ARGS="--mi6 $MP_MI6_ADDRESS --data-dir /data"

if [ "$MP_AUTO_UPDATE" = "true" ]; then
  MP_ARGS="$MP_ARGS --auto-update --update-interval $MP_UPDATE_INTERVAL"
fi

if [ -n "$MP_VERBOSE" ]; then
  MP_ARGS="$MP_ARGS -v"
fi

# Show moneypenny public key on startup so it can be added to MI6 authorized_keys.
# Run as mp user so the key file is owned correctly.
echo "=== Moneypenny public key ==="
su-exec mp:mp moneypenny --data-dir /data --show-public-key
echo "============================="

# Drop privileges and run moneypenny as the mp user.
exec su-exec mp:mp moneypenny $MP_ARGS
