#!/usr/bin/env bash
# Install and verify a read-only collector on a tailnet VPS.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
host_id="${1:-}"
remote_user="${2:-}"
expected_fingerprint="${3:-}"
service_home=/home/vpsdash

if (( EUID != 0 )); then
  echo 'Execute este script com sudo.' >&2
  exit 1
fi
if [[ ! "$host_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ||
      ! "$remote_user" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ||
      ! "$expected_fingerprint" =~ ^SHA256:[A-Za-z0-9+/=]+$ ]]; then
  echo 'Uso: setup-remote-collector.sh <host-id> <usuario-ssh> <fingerprint-ed25519>' >&2
  echo 'No console do host remoto: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256' >&2
  echo 'Informe apenas o trecho que começa com SHA256:.' >&2
  exit 2
fi
if ! getent passwd vpsdash >/dev/null; then
  echo 'Instale vpsdash antes de configurar a coleta remota.' >&2
  exit 1
fi
as_operator() { runuser -u helio -- env HOME=/home/helio "$@"; }
as_service() { runuser -u vpsdash -- env HOME=/home/vpsdash "$@"; }

tailnet_name="$(tailscale status --json | python3 -c '
import json, sys
host_id = sys.argv[1]
state = json.load(sys.stdin)
matches = [p["DNSName"].rstrip(".") for p in state.get("Peer", {}).values()
           if p.get("DNSName", "").split(".")[0] == host_id]
if len(matches) != 1:
    raise SystemExit("Host não encontrado ou duplicado na tailnet: " + host_id)
print(matches[0])
' "$host_id")"
if [[ ! "$tailnet_name" =~ ^[A-Za-z0-9.-]+$ ]]; then
  echo 'Nome DNS da tailnet inválido.' >&2
  exit 1
fi

host_key="$(ssh-keyscan -T 5 -t ed25519 "$tailnet_name" 2>/dev/null | awk '$2 == "ssh-ed25519" {print $2 " " $3}')"
if [[ "$(printf '%s\n' "$host_key" | wc -l)" -ne 1 ||
      ! "$host_key" =~ ^ssh-ed25519[[:space:]][A-Za-z0-9+/=]+$ ]]; then
  echo 'Não foi possível obter uma única host key Ed25519.' >&2
  exit 1
fi
actual_fingerprint="$(printf '%s\n' "$host_key" | ssh-keygen -lf - -E sha256 | awk '{print $2}')"
if [[ "$actual_fingerprint" != "$expected_fingerprint" ]]; then
  echo 'A host key difere do fingerprint obtido no console remoto; nada foi instalado.' >&2
  exit 1
fi

umask 077
temporary_known_hosts="$(mktemp)"
trap 'rm -f "$temporary_known_hosts"' EXIT
printf '%s %s\n' "$tailnet_name" "$host_key" > "$temporary_known_hosts"
chown helio:helio "$temporary_known_hosts"
ssh_options=(-o "UserKnownHostsFile=$temporary_known_hosts" -o StrictHostKeyChecking=yes -o ConnectTimeout=10)
target="$remote_user@$tailnet_name"

install -d -o vpsdash -g vpsdash -m 700 "$service_home/.ssh"
service_key="$service_home/.ssh/id_ed25519_$host_id"
if [[ ! -e "$service_key" ]]; then
  as_service ssh-keygen -q -t ed25519 -N '' -f "$service_key"
fi
public_key="$(cat "$service_key.pub")"
if [[ ! "$public_key" =~ ^ssh-ed25519[[:space:]] ]]; then
  echo 'A chave pública da coleta não é Ed25519.' >&2
  exit 1
fi

# shellcheck disable=SC2016 # $HOME is expanded on the remote host.
remote_home="$(as_operator ssh "${ssh_options[@]}" "$target" 'printf %s "$HOME"')"
if [[ ! "$remote_home" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
  echo 'Diretório home remoto inválido.' >&2
  exit 1
fi
# shellcheck disable=SC2016 # $HOME is expanded on the remote host.
as_operator ssh "${ssh_options[@]}" "$target" 'install -d -m 700 "$HOME/.local/bin" "$HOME/.ssh"'
as_operator scp "${ssh_options[@]}" "$repo_dir/scripts/ssh-readonly.py" "$target:.local/bin/vpsdash-ssh-readonly.py"
# shellcheck disable=SC2016 # $HOME is expanded on the remote host.
as_operator ssh "${ssh_options[@]}" "$target" 'chmod 755 "$HOME/.local/bin/vpsdash-ssh-readonly.py"'

entry="restrict,command=\"/usr/bin/python3 -I $remote_home/.local/bin/vpsdash-ssh-readonly.py\" $public_key"
remote_auth_py='import pathlib,sys; p=pathlib.Path.home()/".ssh"/"authorized_keys"; entry=sys.stdin.read().strip(); blob=entry.rsplit(" ",2)[1]; lines=p.read_text().splitlines() if p.exists() else []; matches=[line for line in lines if blob in line]; sys.exit("Chave já autorizada com outro comando") if matches and matches != [entry] else None; p.write_text("\n".join(lines+[entry])+"\n") if not matches else None; p.chmod(0o600)'
printf '%s\n' "$entry" | as_operator ssh "${ssh_options[@]}" "$target" "python3 -c '$remote_auth_py'"

known_hosts="$service_home/.ssh/known_hosts"
as_service touch "$known_hosts"
chmod 600 "$known_hosts"
known_entry="$tailnet_name $host_key"
if ! as_service grep -Fqx -- "$known_entry" "$known_hosts"; then
  printf '%s\n' "$known_entry" | as_service tee -a "$known_hosts" >/dev/null
fi

python3 - "$repo_dir/internal/collect/scripts.go" <<'PY' | \
  as_service ssh -T -i "$service_key" -o IdentitiesOnly=yes \
    -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=yes \
    -o HostKeyAlgorithms=ssh-ed25519 \
    "$target" sh -s >/dev/null
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text()
match = re.search(r"const MetricsScript = `(.*?)`", source, re.DOTALL)
if not match:
    raise SystemExit("MetricsScript não encontrado")
sys.stdout.write(match.group(1))
PY

echo "Coleta SSH restrita verificada em $tailnet_name."
