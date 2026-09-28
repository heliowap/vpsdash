#!/usr/bin/env bash
# Authorize a separate vpsdash key for the private web terminal.
#
# The key belongs to the vpsdash account, is distinct from every collector
# key, and is authorized for the operator account with "restrict,pty": a PTY
# is allowed, while port, agent, and X11 forwarding stay disabled. The
# read-only collector bridge is not touched.
#
#   scripts/setup-interactive-key.sh local
#   sudo scripts/setup-interactive-key.sh <host-id> helio <fingerprint-ed25519>
set -euo pipefail

mode="${1:-}"
operator_home=/home/helio
service_home=/home/vpsdash

usage() {
  echo 'Uso: setup-interactive-key.sh local' >&2
  echo '     sudo setup-interactive-key.sh <host-id> helio <fingerprint-ed25519>' >&2
  echo 'No console do host remoto: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256' >&2
  exit 2
}

if ! getent passwd vpsdash >/dev/null; then
  echo 'Instale o usuário vpsdash antes de configurar o terminal.' >&2
  exit 1
fi

if [[ "$mode" == local ]]; then
  (( $# == 1 )) || usage
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
  tailnet_name="$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')"
  host_id="${tailnet_name%%.*}"
  remote_user=helio
  host_key="$(awk 'NR==1 {print $1 " " $2}' /etc/ssh/ssh_host_ed25519_key.pub)"
else
  (( $# == 3 )) || usage
  if (( EUID != 0 )); then
    echo 'Execute este script com sudo para um host remoto.' >&2
    exit 1
  fi
  host_id="$mode"
  remote_user="$2"
  expected_fingerprint="$3"
  if [[ ! "$remote_user" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ||
        ! "$expected_fingerprint" =~ ^SHA256:[A-Za-z0-9+/=]+$ ]]; then
    usage
  fi
  # Same rule as config.ValidateInteractiveUser: the PTY runs as this
  # account, so only the operator account is accepted.
  case "$remote_user" in
    root|gh-agents|vpsdash)
      echo "A conta $remote_user é isolada e não recebe acesso interativo; use helio." >&2
      exit 1
      ;;
    helio) ;;
    *)
      echo "O acesso interativo só é autorizado para helio, não para $remote_user." >&2
      exit 1
      ;;
  esac
  as_operator() { runuser -u helio -- env HOME=/home/helio "$@"; }
  as_root() { "$@"; }
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
  host_key="$(ssh-keyscan -T 5 -t ed25519 "$tailnet_name" 2>/dev/null | awk '$2 == "ssh-ed25519" {print $2 " " $3}')"
  if [[ "$(printf '%s\n' "$host_key" | wc -l)" -ne 1 ]]; then
    echo 'Não foi possível obter uma única host key Ed25519.' >&2
    exit 1
  fi
  actual_fingerprint="$(printf '%s\n' "$host_key" | ssh-keygen -lf - -E sha256 | awk '{print $2}')"
  if [[ "$actual_fingerprint" != "$expected_fingerprint" ]]; then
    echo 'A host key difere do fingerprint obtido no console remoto; nada foi instalado.' >&2
    exit 1
  fi
fi

if [[ ! "$tailnet_name" =~ ^[A-Za-z0-9.-]+$ || ! "$host_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo 'Nome DNS da tailnet inválido.' >&2
  exit 1
fi
if [[ ! "$host_key" =~ ^ssh-ed25519[[:space:]][A-Za-z0-9+/=]+$ ]]; then
  echo 'A chave Ed25519 do SSH do host não está disponível.' >&2
  exit 1
fi

# The interactive key never reuses a collector key path.
service_key="$service_home/.ssh/id_ed25519_interactive_$host_id"
as_root install -d -o vpsdash -g vpsdash -m 700 "$service_home/.ssh"
if ! as_root test -e "$service_key"; then
  as_service ssh-keygen -q -t ed25519 -N '' -C "vpsdash-interactive@$host_id" -f "$service_key"
fi
public_key="$(as_root cat "$service_key.pub")"
if [[ ! "$public_key" =~ ^ssh-ed25519[[:space:]] ]]; then
  echo 'A chave pública interativa não é Ed25519.' >&2
  exit 1
fi
key_blob="$(printf '%s\n' "$public_key" | awk '{print $2}')"
entry="restrict,pty $(printf '%s\n' "$public_key" | awk '{print $1 " " $2}') vpsdash-interactive"

if [[ "$mode" == local ]]; then
  as_operator install -d -m 700 "$operator_home/.ssh"
  as_operator touch "$operator_home/.ssh/authorized_keys"
  as_operator chmod 600 "$operator_home/.ssh/authorized_keys"
  existing="$(as_operator grep -F -- "$key_blob" "$operator_home/.ssh/authorized_keys" || true)"
  if [[ -n "$existing" && "$existing" != "$entry" ]]; then
    echo 'Esta chave já tem outra autorização SSH. Revise authorized_keys antes de continuar.' >&2
    exit 1
  fi
  if [[ -z "$existing" ]]; then
    printf '%s\n' "$entry" | as_operator tee -a "$operator_home/.ssh/authorized_keys" >/dev/null
  fi
else
  umask 077
  temporary_known_hosts="$(mktemp)"
  trap 'rm -f "$temporary_known_hosts"' EXIT
  printf '%s %s\n' "$tailnet_name" "$host_key" > "$temporary_known_hosts"
  chown helio:helio "$temporary_known_hosts"
  ssh_options=(-o "UserKnownHostsFile=$temporary_known_hosts" -o StrictHostKeyChecking=yes -o ConnectTimeout=10)
  remote_auth_py='import pathlib,sys; d=pathlib.Path.home()/".ssh"; d.mkdir(mode=0o700, exist_ok=True); p=d/"authorized_keys"; entry=sys.stdin.read().strip(); blob=entry.split()[2]; lines=p.read_text().splitlines() if p.exists() else []; matches=[line for line in lines if blob in line]; sys.exit("Chave já autorizada com outras opções") if matches and matches != [entry] else None; p.write_text("\n".join(lines+[entry])+"\n") if not matches else None; p.chmod(0o600)'
  printf '%s\n' "$entry" | as_operator ssh "${ssh_options[@]}" "$remote_user@$tailnet_name" "python3 -c '$remote_auth_py'"
fi

known_entry="$tailnet_name $host_key"
as_service touch "$service_home/.ssh/known_hosts"
as_root chmod 600 "$service_home/.ssh/known_hosts"
if ! as_service grep -Fqx -- "$known_entry" "$service_home/.ssh/known_hosts"; then
  printf '%s\n' "$known_entry" | as_service tee -a "$service_home/.ssh/known_hosts" >/dev/null
fi

# A non-PTY command proves the key logs in; restrict keeps forwarding off.
as_service ssh -T -i "$service_key" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o ConnectTimeout=5 -o StrictHostKeyChecking=yes -o HostKeyAlgorithms=ssh-ed25519 \
  "$remote_user@$tailnet_name" true

echo "Chave interativa verificada em $tailnet_name para $remote_user."
echo "No inventário, no host \"$host_id\", defina:"
echo "  \"interactive_key_file\": \"$service_key\""
echo 'Defina também "private_listen" e aponte o Tailscale Serve para ele (README).'
