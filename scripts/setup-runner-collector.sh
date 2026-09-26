#!/usr/bin/env bash
# Authorize a separate vpsdash key to read the gh-agents user units on this VPS.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runner_home=/home/gh-agents
service_home=/home/vpsdash
bridge="$runner_home/.local/bin/vpsdash-ssh-readonly.py"
service_key="$service_home/.ssh/id_ed25519_gh-agents_intrador-tech-vps"

if (( EUID == 0 )); then
  as_root() { "$@"; }
  as_service() { runuser -u vpsdash -- env HOME=/home/vpsdash "$@"; }
  as_runner() { runuser -u gh-agents -- env HOME=/home/gh-agents "$@"; }
elif [[ "$(id -un)" == helio ]]; then
  as_root() { sudo "$@"; }
  as_service() { sudo -H -u vpsdash "$@"; }
  as_runner() { sudo -u gh-agents "$@"; }
  sudo -v
else
  echo 'Execute este script como helio ou com sudo.' >&2
  exit 1
fi
if ! getent passwd vpsdash >/dev/null; then
  echo 'Instale o usuário vpsdash antes de configurar a coleta.' >&2
  exit 1
fi
if [[ "$(getent passwd gh-agents | cut -d: -f6)" != "$runner_home" ]]; then
  echo 'A conta gh-agents não existe ou usa outro diretório home.' >&2
  exit 1
fi

runner_group="$(id -gn gh-agents)"
as_root install -d -o gh-agents -g "$runner_group" -m 700 "$runner_home/.local/bin"
as_root install -o root -g root -m 755 "$repo_dir/scripts/ssh-readonly.py" "$bridge"
as_root install -d -o vpsdash -g vpsdash -m 700 "$service_home/.ssh"
if ! as_root test -e "$service_key"; then
  as_service ssh-keygen -q -t ed25519 -N '' -f "$service_key"
fi
public_key="$(as_root cat "$service_key.pub")"
if [[ ! "$public_key" =~ ^ssh-ed25519[[:space:]] ]]; then
  echo 'A chave pública da coleta não é Ed25519.' >&2
  exit 1
fi

as_runner install -d -m 700 "$runner_home/.ssh"
as_runner touch "$runner_home/.ssh/authorized_keys"
as_runner chmod 600 "$runner_home/.ssh/authorized_keys"
entry="restrict,command=\"/usr/bin/python3 -I $bridge runner-units\" $public_key"
key_blob="$(printf '%s\n' "$public_key" | awk '{print $2}')"
existing="$(as_runner grep -F -- "$key_blob" "$runner_home/.ssh/authorized_keys" || true)"
if [[ -n "$existing" && "$existing" != "$entry" ]]; then
  echo 'Esta chave já tem outra autorização SSH. Revise authorized_keys antes de continuar.' >&2
  exit 1
fi
if ! as_runner grep -Fqx -- "$entry" "$runner_home/.ssh/authorized_keys"; then
  printf '%s\n' "$entry" | as_runner tee -a "$runner_home/.ssh/authorized_keys" >/dev/null
fi

tailnet_name="$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')"
if [[ ! "$tailnet_name" =~ ^[A-Za-z0-9.-]+$ ]]; then
  echo 'Nome DNS da tailnet inválido.' >&2
  exit 1
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

snapshot="$(python3 - "$repo_dir/internal/collect/scripts.go" <<'PY' | \
  as_service ssh -T -i "$service_key" -o IdentitiesOnly=yes \
    -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=yes \
    -o HostKeyAlgorithms=ssh-ed25519 \
    "gh-agents@$tailnet_name" sh -s
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text()
match = re.search(r"const RunnerUnitsScript = `(.*?)`", source, re.DOTALL)
if not match:
    raise SystemExit("RunnerUnitsScript não encontrado")
sys.stdout.write(match.group(1))
PY
)"
if ! printf '%s\n' "$snapshot" | awk -F '\t' '$1 == "gh-agents-cleanup.timer" && NF == 3 {seen=1} END {exit !seen}'; then
  echo 'A leitura SSH não retornou o timer de limpeza.' >&2
  exit 1
fi
fixture_path="$(mktemp /tmp/vpsdash-runner-units.XXXXXX)"
if ! printf '%s\n' "$snapshot" | awk -f "$repo_dir/scripts/sanitize-runner-units.awk" > "$fixture_path"; then
  rm -f "$fixture_path"
  echo 'A leitura das units contém uma linha inesperada.' >&2
  exit 1
fi
if (( EUID == 0 )); then
  chown helio:helio "$fixture_path"
fi
echo "Coleta restrita das units gh-agents verificada em $tailnet_name."
echo "Amostra com nomes ocultados para o teste: $fixture_path"
