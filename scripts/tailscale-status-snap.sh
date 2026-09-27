#!/bin/sh
# Read Tailscale status without invoking snap-confine under NoNewPrivileges.
set -eu

if [ "$#" -ne 2 ] || [ "$1" != status ] || [ "$2" != --json ]; then
  echo 'Somente tailscale status --json é permitido.' >&2
  exit 2
fi

exec /snap/tailscale/current/bin/tailscale \
  --socket=/var/snap/tailscale/common/socket/tailscaled.sock status --json
