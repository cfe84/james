#!/bin/sh
set -e

# Show public keys on startup for MI6 authorized_keys setup.
echo "=== Hem public key ==="
hem show-public-key
echo "=== Qew public key ==="
qew --show-public-key
echo "======================"

# Start Hem server in background.
HEM_ARGS="start server -v"
if [ -n "$HEM_MI6_URL" ]; then
  HEM_ARGS="$HEM_ARGS --mi6-control $HEM_MI6_URL"
else
  echo "Warning, no mi6 URL"
fi
hem $HEM_ARGS &
HEM_PID=$!

# Wait for Hem socket to be ready.
SOCK="/root/.config/james/hem/hem.sock"
for i in $(seq 1 30); do
  [ -S "$SOCK" ] && break
  sleep 0.2
done

# Start Qew in foreground (connects to Hem via Unix socket).
QEW_ARGS="--listen $LISTEN --socket $SOCK -v"
if [ -n "$QEW_PASSWORD" ]; then
  QEW_ARGS="$QEW_ARGS --password $QEW_PASSWORD"
else
  QEW_ARGS="$QEW_ARGS --development"
fi

qew $QEW_ARGS &
QEW_PID=$!

# Wait for either process to exit.
wait -n $HEM_PID $QEW_PID 2>/dev/null || wait $HEM_PID $QEW_PID
