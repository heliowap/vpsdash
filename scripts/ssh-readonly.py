#!/usr/bin/env python3
"""Forced SSH command for the vpsdash collector keys.

The fixed collector scripts are accepted by their exact SHA-256 digest.
Health checks run direct commands with argument lists, never a client shell.
Only the gh-agents key (``runner-units`` mode) may also restart or drain one
of that account's own ``actions.runner.*`` user units, without sudo.
"""

import base64
import hashlib
import os
import pwd
import re
import subprocess
import sys

APPROVED_SCRIPTS = {
    "44c270297679ba536d8236382fdc608d42fa16e475ff8baf4b18b90d8400e2dd",  # metrics
    "bd00bf9b9b1ce93a4d5fe8e7307e9af7f0bb72747fd5ef5183582545aa1ed724",  # discovery
    "32412b3f96ecfcf4bd09fe78ae55791e3b9ef505196c064d2f87967fec7aa9a9",  # sessions
}
RUNNER_UNITS_DIGEST = "0df33256bd3e5fae028dbebf0eaab5962e85c63a16187bf3ac06bfa55852d0d2"
RUNNER_UNIT = re.compile(r"actions\.runner\.[A-Za-z0-9._-]+\.service")
# Tests replace these module values; production never reads them from the
# environment.
SYSTEMCTL = "systemctl"
CGROUP_ROOT = "/sys/fs/cgroup"
PROC_ROOT = "/proc"
EXIT_BUSY = 75


def runner_unit_dir():
    home = pwd.getpwuid(os.getuid()).pw_dir
    return os.path.join(home, ".config", "systemd", "user")


def environment():
    safe = {"PATH": "/usr/local/bin:/usr/bin:/bin"}
    for key in ("HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"):
        if key in os.environ:
            safe[key] = os.environ[key]
    return safe


def deny():
    print("vpsdash: comando SSH não permitido", file=sys.stderr)
    return 126


def fixed_script(allowed):
    script = sys.stdin.buffer.read(4097)
    if len(script) > 4096 or hashlib.sha256(script).hexdigest() not in allowed:
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
    target = name.removeprefix("user:") if source == "systemd" else name
    if not name or not target or target.startswith("-") or len(raw) > 256 or any(ord(char) < 32 for char in name):
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


def user_bus_environment():
    safe = environment()
    runtime = f"/run/user/{os.getuid()}"
    safe["XDG_RUNTIME_DIR"] = runtime
    safe["DBUS_SESSION_BUS_ADDRESS"] = f"unix:path={runtime}/bus"
    return safe


def runner_unit(name):
    if len(name) > 255 or not RUNNER_UNIT.fullmatch(name):
        return None
    directory = runner_unit_dir()
    try:
        if os.path.basename(name) != name or not os.path.isfile(os.path.join(directory, name)):
            return None
    except OSError:
        return None
    return name


def unit_runs_job(unit, env):
    """Report whether the runner worker process lives in the unit cgroup."""
    result = subprocess.run(
        [SYSTEMCTL, "--user", "show", "--property=ControlGroup", "--value", "--", unit],
        capture_output=True, text=True, timeout=9, env=env,
    )
    if result.returncode != 0:
        raise OSError("systemctl show falhou")
    group = result.stdout.strip()
    if not group:
        return False  # The unit is not running, so no job can be running.
    if not group.startswith("/") or ".." in group.split("/"):
        raise OSError("cgroup inesperado")
    with open(os.path.join(CGROUP_ROOT, group.lstrip("/"), "cgroup.procs"), encoding="ascii") as procs:
        pids = [line.strip() for line in procs if line.strip().isdigit()]
    for pid in pids:
        try:
            with open(os.path.join(PROC_ROOT, pid, "comm"), encoding="utf-8") as comm:
                if comm.read().strip() == "Runner.Worker":
                    return True
        except FileNotFoundError:
            continue  # The process exited while the list was read.
    return False


def runner_operation(command):
    parts = command.split(" ")
    if len(parts) != 2 or parts[0] not in ("runner-restart", "runner-drain"):
        return deny()
    unit = runner_unit(parts[1])
    if unit is None:
        return deny()
    env = user_bus_environment()
    if parts[0] == "runner-drain":
        # Drain stops the unit only when no job is running; otherwise the
        # caller keeps waiting. Stopping a runner cancels its current job.
        if unit_runs_job(unit, env):
            print("busy")
            return EXIT_BUSY
        args = [SYSTEMCTL, "--user", "stop", "--", unit]
    else:
        args = [SYSTEMCTL, "--user", "restart", "--", unit]
    result = subprocess.run(args, capture_output=True, text=True, timeout=330, env=env)
    if result.returncode != 0:
        sys.stderr.write(result.stderr[-2000:])
        return result.returncode
    print("stopped" if parts[0] == "runner-drain" else "restarted")
    return 0


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "runner-units":
        command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
        if command == "sh -s":
            return fixed_script({RUNNER_UNITS_DIGEST})
        if command.startswith(("runner-restart ", "runner-drain ")):
            return runner_operation(command)
        return deny()
    if len(sys.argv) != 1:
        return deny()
    command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
    if command == "sh -s":
        return fixed_script(APPROVED_SCRIPTS)
    if command.startswith("vpsdash-health "):
        return health(command)
    return deny()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"vpsdash: falha no comando: {error}", file=sys.stderr)
        sys.exit(1)
