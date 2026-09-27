#!/usr/bin/env bash
# Authorize vpsdash to collect read-only data from helio on this VPS.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
operator_home=/home/helio
service_home=/home/vpsdash
bridge="$operator_home/.local/bin/vpsdash-ssh-readonly.py"
tailnet_name="$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')"
local_host_id="${tailnet_name%%.*}"
if [[ ! "$tailnet_name" =~ ^[A-Za-z0-9.-]+$ || ! "$local_host_id" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo 'Nome DNS da tailnet inválido.' >&2
  exit 1
fi
service_key="$service_home/.ssh/id_ed25519_$local_host_id"

if (( EUID == 0 )); then
  as_operator() { runuser -u helio -- env HOME=/home/helio "$@"; }
  as_root() { "$@"; }
  as_service() { runuser -u vpsdash -- env HOME=/home/vpsdash "$@"; }
elif [[ "$(id -un)" == helio ]]; then
  as_operator() { "$@"; }
  as_root() { sudo "$@"; }
  as_service() { sudo -H -u vpsdash "$@"; }
  sudo -v
else
  echo 'Execute este script como helio ou com sudo.' >&2
  exit 1
fi
if ! getent passwd vpsdash >/dev/null; then
  echo 'Instale o usuário vpsdash antes de configurar a coleta.' >&2
  exit 1
fi

as_operator install -d -m 755 "$operator_home/.local/bin"
as_operator install -m 755 "$repo_dir/scripts/ssh-readonly.py" "$bridge"
as_root install -d -o vpsdash -g vpsdash -m 700 "$service_home/.ssh"
if ! as_root test -e "$service_key"; then
  as_service ssh-keygen -q -t ed25519 -N '' -f "$service_key"
fi
public_key="$(as_root cat "$service_key.pub")"
if [[ ! "$public_key" =~ ^ssh-ed25519[[:space:]] ]]; then
  echo 'A chave pública da coleta não é Ed25519.' >&2
  exit 1
fi

as_operator install -d -m 700 "$operator_home/.ssh"
as_operator touch "$operator_home/.ssh/authorized_keys"
as_operator chmod 600 "$operator_home/.ssh/authorized_keys"
entry="restrict,command=\"/usr/bin/python3 -I $bridge\" $public_key"
key_blob="$(printf '%s\n' "$public_key" | awk '{print $2}')"
existing="$(as_operator grep -F -- "$key_blob" "$operator_home/.ssh/authorized_keys" || true)"
if [[ -n "$existing" && "$existing" != "$entry" ]]; then
  echo 'Esta chave já tem outra autorização SSH. Revise authorized_keys antes de continuar.' >&2
  exit 1
fi
if ! as_operator grep -Fqx -- "$entry" "$operator_home/.ssh/authorized_keys"; then
  printf '%s\n' "$entry" | as_operator tee -a "$operator_home/.ssh/authorized_keys" >/dev/null
fi

host_key="$(awk 'NR==1 {print $1 " " $2}' /etc/ssh/ssh_host_ed25519_key.pub)"
if [[ ! "$host_key" =~ ^ssh-ed25519[[:space:]] ]]; then
  echo 'A chave Ed25519 do SSH local não está disponível.' >&2
  exit 1
fi
known_entry="$tailnet_name $host_key"
as_service touch "$service_home/.ssh/known_hosts"
as_root chmod 600 "$service_home/.ssh/known_hosts"
if ! as_service grep -Fqx -- "$known_entry" "$service_home/.ssh/known_hosts"; then
  printf '%s\n' "$known_entry" | as_service tee -a "$service_home/.ssh/known_hosts" >/dev/null
fi

python3 - "$repo_dir/internal/collect/scripts.go" <<'PY' | \
  as_service ssh -T -i "$service_key" -o IdentitiesOnly=yes \
    -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=yes \
    -o HostKeyAlgorithms=ssh-ed25519 \
    "helio@$tailnet_name" sh -s >/dev/null
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
