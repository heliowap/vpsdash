#!/usr/bin/env python3
"""Forced SSH command for the vpsdash collector key.

The three fixed collector scripts are accepted by their exact SHA-256 digest.
Health checks run direct commands with argument lists, never a client shell.
"""

import base64
import hashlib
import os
import subprocess
import sys

APPROVED_SCRIPTS = {
    "44c270297679ba536d8236382fdc608d42fa16e475ff8baf4b18b90d8400e2dd",  # metrics
    "bd00bf9b9b1ce93a4d5fe8e7307e9af7f0bb72747fd5ef5183582545aa1ed724",  # discovery
    "65f9c354476d0e225f0e529734d1b8a7eee4130d5ec56f5376ad1844c073c82b",  # sessions
}


def environment():
    safe = {"PATH": "/usr/local/bin:/usr/bin:/bin"}
    for key in ("HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"):
        if key in os.environ:
            safe[key] = os.environ[key]
    return safe


def deny():
    print("vpsdash: comando SSH não permitido", file=sys.stderr)
    return 126


def fixed_script():
    script = sys.stdin.buffer.read(4097)
    if len(script) > 4096 or hashlib.sha256(script).hexdigest() not in APPROVED_SCRIPTS:
        return deny()
    result = subprocess.run(
        ["/bin/sh", "-s"], input=script, capture_output=True, timeout=9, env=environment()
    )
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    return result.returncode


def health(command):
    parts = command.split()
    if len(parts) != 3 or parts[0] != "vpsdash-health":
        return deny()
    _, source, encoded = parts
    try:
        raw = base64.b64decode(encoded + "=" * (-len(encoded) % 4), altchars=b"-_", validate=True)
        name = raw.decode("utf-8")
    except (ValueError, UnicodeDecodeError):
        return deny()
    if not name or len(raw) > 256 or any(ord(char) < 32 for char in name):
        return deny()
    if source == "systemd":
        args = ["systemctl", "is-active", name]
        if name.startswith("user:"):
            args = ["systemctl", "--user", "is-active", name.removeprefix("user:")]
    elif source == "docker":
        args = ["docker", "inspect", "-f", "{{.State.Running}}", name]
    elif source == "tmux":
        args = ["tmux", "has-session", "-t", name]
    else:
        return deny()
    result = subprocess.run(args, capture_output=True, text=True, timeout=9, env=environment())
    active = result.returncode == 0 and (source == "tmux" or result.stdout.strip() in ("active", "true"))
    print("active" if active else "inactive")
    return 0


def main():
    command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
    if command == "sh -s":
        return fixed_script()
    if command.startswith("vpsdash-health "):
        return health(command)
    return deny()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"vpsdash: falha na leitura: {error}", file=sys.stderr)
        sys.exit(1)
