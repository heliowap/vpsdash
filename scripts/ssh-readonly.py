#!/usr/bin/env python3
"""Forced SSH command for the vpsdash collector keys.

The fixed collector scripts are accepted by their exact SHA-256 digest.
Health checks run direct commands with argument lists, never a client shell.
Only the gh-agents key (``runner-units`` mode) may also restart or drain one
of that account's own ``actions.runner.*`` user units, without sudo.

File reads (`vpsdash-files`) are read-only and confined to the roots listed
in an operator-owned file on this host (default
`~/.config/vpsdash-files/roots`, or `--file-roots <path>` baked into the
authorized_keys command). The panel cannot change those roots over SSH.
"""

import base64
import hashlib
import json
import os
import pwd
import re
import stat
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


# Keep these rules identical to internal/files/rules.go; the Go tests run
# both implementations over the same fixture tree and compare the results.
READ_LIMIT = 512 * 1024
LIST_LIMIT = 1000
SECRET_NAMES = {
    ".ssh", ".gnupg", ".git", ".aws", ".azure", ".kube", ".docker",
    ".password-store", ".netrc", ".npmrc", ".pypirc", ".pgpass", ".htpasswd",
    ".vault-token", "shadow", "gshadow",
}
SECRET_SUFFIXES = (".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".gpg", ".env")
SECRET_WORDS = ("secret", "credential", "password", "passwd")


class FileDenied(Exception):
    pass


def secret_name(name):
    lower = name.lower()
    return (
        lower in SECRET_NAMES
        or lower.startswith(".env")
        or lower.startswith("id_")
        or lower.endswith(SECRET_SUFFIXES)
        or lower.endswith("_history")
        or any(word in lower for word in SECRET_WORDS)
    )


def display_name(name):
    raw = name.encode("utf-8", "surrogateescape")
    try:
        return raw.decode("utf-8"), True
    except UnicodeDecodeError:
        return re.sub("�+", "�", raw.decode("utf-8", "replace")), False


def clean_path(path):
    return (
        path.startswith("/")
        and path != "/"
        and "\x00" not in path
        and len(path.encode("utf-8", "surrogateescape")) <= 4096
        and all(part not in ("", ".", "..") for part in path.split("/")[1:])
    )


def under(path, root):
    return path == root or path.startswith(root + "/")


def relative_parts(path, root):
    return [part for part in path[len(root):].split("/") if part]


def resolve(path):
    # realpath(strict=True) needs Python 3.10; stat() gives the same check.
    real = os.path.realpath(path)
    os.stat(real)
    return real


def load_roots(roots_file):
    try:
        info = os.stat(roots_file, follow_symlinks=False)
        parent = os.stat(os.path.dirname(roots_file))
    except FileNotFoundError:
        raise FileDenied("disabled")
    for item in (info, parent):
        if item.st_uid not in (os.getuid(), 0) or item.st_mode & 0o022:
            raise FileDenied("roots_insecure")
    if not stat.S_ISREG(info.st_mode):
        raise FileDenied("roots_insecure")
    with open(roots_file, encoding="utf-8") as handle:
        lines = handle.read(65536).splitlines()
    roots = []
    for line in lines:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if not clean_path(line):
            raise FileDenied("roots_insecure")
        try:
            roots.append((line, resolve(line)))
        except OSError:
            continue
    if not roots:
        raise FileDenied("disabled")
    return roots


def contained(path, roots):
    """Return the resolved path after lexical and real confinement checks."""
    lexical = [root for root, _ in roots if under(path, root)]
    if not lexical:
        raise FileDenied("outside")
    if any(secret_name(part) for root in lexical for part in relative_parts(path, root)):
        raise FileDenied("blocked")
    try:
        real = resolve(path)
    except PermissionError:
        raise FileDenied("unreadable")
    except OSError:
        raise FileDenied("not_found")
    matches = [resolved for _, resolved in roots if under(real, resolved)]
    if not matches:
        raise FileDenied("outside")
    if any(secret_name(part) for root in matches for part in relative_parts(real, root)):
        raise FileDenied("blocked")
    return real


def open_confined(real, directory):
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC
    if directory:
        flags |= os.O_DIRECTORY
    try:
        fd = os.open(real, flags)
    except FileNotFoundError:
        raise FileDenied("not_found")
    except NotADirectoryError:
        raise FileDenied("not_dir")
    except OSError:
        raise FileDenied("unreadable")
    try:
        # A component swapped for a symlink after realpath() lands elsewhere.
        opened = os.readlink(f"/proc/self/fd/{fd}")
    except OSError:
        os.close(fd)
        raise FileDenied("unreadable")
    if opened != real:
        os.close(fd)
        raise FileDenied("outside")
    return fd


def entry_kind(mode):
    if stat.S_ISDIR(mode):
        return "dir"
    if stat.S_ISREG(mode):
        return "file"
    return "other"


def list_entry(path, real, name, roots):
    shown, valid = display_name(name)
    entry = {"name": shown, "type": "other", "size": 0, "mtime": 0, "link": False, "blocked": False, "reason": ""}
    try:
        info = os.stat(os.path.join(real, name), follow_symlinks=False)
    except OSError:
        entry.update(blocked=True, reason="unreadable")
        return entry
    entry["link"] = stat.S_ISLNK(info.st_mode)
    if not valid:
        entry.update(blocked=True, reason="name")
        return entry
    if secret_name(name):
        entry.update(type="other" if entry["link"] else entry_kind(info.st_mode), blocked=True, reason="secret")
        return entry
    if entry["link"]:
        try:
            target = contained(path + "/" + name, roots)
            info = os.stat(target)
        except FileDenied as denied:
            reason = str(denied)
            if reason != "not_found":
                entry.update(blocked=True, reason="secret" if reason == "blocked" else reason)
            return entry
        except OSError:
            return entry
    entry.update(type=entry_kind(info.st_mode), mtime=int(info.st_mtime))
    if stat.S_ISREG(info.st_mode):
        entry["size"] = info.st_size
    return entry


def list_files(path, roots):
    real = contained(path, roots)
    fd = open_confined(real, True)
    names = []
    truncated = False
    try:
        with os.scandir(fd) as entries:
            for item in entries:
                if len(names) == LIST_LIMIT:
                    truncated = True
                    break
                names.append(item.name)
    finally:
        os.close(fd)
    items = [list_entry(path, real, name, roots) for name in names]
    items.sort(key=lambda item: (item["type"] != "dir", item["name"]))
    return {"path": path, "entries": items, "truncated": truncated}


def valid_text(data):
    try:
        data.decode("utf-8")
    except UnicodeDecodeError:
        return False
    return True


def read_file(path, offset, roots):
    real = contained(path, roots)
    fd = open_confined(real, False)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise FileDenied("not_file")
        if offset > info.st_size:
            raise FileDenied("invalid")
        os.lseek(fd, offset, os.SEEK_SET)
        data = b""
        while len(data) < READ_LIMIT:
            chunk = os.read(fd, READ_LIMIT - len(data))
            if not chunk:
                break
            data += chunk
    finally:
        os.close(fd)
    result = {"path": path, "size": info.st_size, "mtime": int(info.st_mtime), "offset": offset,
              "length": 0, "next_offset": offset, "truncated": False, "binary": True, "content": ""}
    if b"\x00" in data:
        return result
    # A page may end inside a multibyte character; the next page starts there.
    at_end = offset + len(data) >= info.st_size
    for trim in (0,) if at_end else (0, 1, 2, 3):
        if trim > len(data):
            break
        text = data[: len(data) - trim]
        if valid_text(text):
            end = offset + len(text)
            result.update(length=len(text), next_offset=end, truncated=end < info.st_size,
                          binary=False, content=text.decode("utf-8"))
            break
    return result


def files(command, roots_file):
    parts = command.split(" ")
    if len(parts) < 3 or parts[0] != "vpsdash-files":
        return deny()
    operation, encoded = parts[1], parts[2]
    if (operation, len(parts)) not in (("list", 3), ("read", 4)):
        return deny()
    offset = 0
    if operation == "read":
        if not re.fullmatch(r"[0-9]{1,15}", parts[3]):
            return deny()
        offset = int(parts[3])
    try:
        raw = base64.b64decode(encoded + "=" * (-len(encoded) % 4), altchars=b"-_", validate=True)
        path = raw.decode("utf-8")
    except (ValueError, UnicodeDecodeError):
        return deny()
    try:
        if not clean_path(path):
            raise FileDenied("invalid")
        roots = load_roots(roots_file)
        result = list_files(path, roots) if operation == "list" else read_file(path, offset, roots)
    except FileDenied as denied:
        result = {"error": str(denied)}
    sys.stdout.buffer.write(json.dumps(result, ensure_ascii=False).encode("utf-8") + b"\n")
    return 0


def default_roots_file():
    return os.path.join(pwd.getpwuid(os.getuid()).pw_dir, ".config", "vpsdash-files", "roots")


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "runner-units":
        command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
        if command == "sh -s":
            return fixed_script({RUNNER_UNITS_DIGEST})
        if command.startswith(("runner-restart ", "runner-drain ")):
            return runner_operation(command)
        return deny()
    roots_file = None
    if len(sys.argv) == 3 and sys.argv[1] == "--file-roots" and clean_path(sys.argv[2]):
        roots_file = sys.argv[2]
    elif len(sys.argv) != 1:
        return deny()
    command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
    if command == "sh -s":
        return fixed_script(APPROVED_SCRIPTS)
    if command.startswith("vpsdash-health "):
        return health(command)
    if command.startswith("vpsdash-files "):
        return files(command, roots_file or default_roots_file())
    return deny()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"vpsdash: falha no comando: {error}", file=sys.stderr)
        sys.exit(1)
