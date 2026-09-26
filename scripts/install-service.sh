#!/usr/bin/env bash
# Install the service account, binary, and user unit without starting the panel.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
binary="${1:-$repo_dir/bin/vpsdash}"
service_home=/home/vpsdash

if (( EUID != 0 )); then
  echo 'Execute este script com sudo.' >&2
  exit 1
fi
if [[ ! -x "$binary" ]]; then
  echo "Binário não encontrado ou sem permissão de execução: $binary" >&2
  exit 1
fi
"$binary" --version >/dev/null

if ! getent passwd vpsdash >/dev/null; then
  useradd --user-group --create-home --shell /bin/bash vpsdash
fi
actual_home="$(getent passwd vpsdash | cut -d: -f6)"
if [[ "$actual_home" != "$service_home" ]]; then
  echo "O usuário vpsdash usa $actual_home; esperado: $service_home" >&2
  exit 1
fi
service_group="$(id -gn vpsdash)"
if [[ "$service_group" != vpsdash ]]; then
  echo "O grupo primário de vpsdash é $service_group; esperado: vpsdash" >&2
  exit 1
fi
service_uid="$(id -u vpsdash)"
for target in "$service_home" "$service_home/.config" "$service_home/.config/vpsdash" \
  "$service_home/.config/vpsdash/config.json" "$service_home/.config/systemd" \
  "$service_home/.config/systemd/user" "$service_home/.config/systemd/user/vpsdash.service" \
  "$service_home/bin" "$service_home/bin/vpsdash" \
  "$service_home/bin/tailscale"; do
  if [[ -L "$target" ]]; then
    echo "Caminho inesperado com link simbólico: $target" >&2
    exit 1
  fi
done
loginctl enable-linger vpsdash

install -d -o vpsdash -g vpsdash -m 700 "$service_home"
install -d -o vpsdash -g "$service_group" -m 700 "$service_home/.config/vpsdash"
install -d -o vpsdash -g "$service_group" -m 700 "$service_home/.config/systemd/user"
install -d -o root -g root -m 755 "$service_home/bin"
install -o root -g root -m 755 "$binary" "$service_home/bin/vpsdash"
if [[ -x /snap/tailscale/current/bin/tailscale && \
      -S /var/snap/tailscale/common/socket/tailscaled.sock ]]; then
  install -o root -g root -m 755 "$repo_dir/scripts/tailscale-status-snap.sh" "$service_home/bin/tailscale"
elif [[ -f "$service_home/bin/tailscale" ]] && \
     cmp -s "$repo_dir/scripts/tailscale-status-snap.sh" "$service_home/bin/tailscale"; then
  rm -f "$service_home/bin/tailscale"
fi
install -o root -g root -m 644 "$repo_dir/deploy/vpsdash.service" "$service_home/.config/systemd/user/vpsdash.service"
if [[ ! -e "$service_home/.config/vpsdash/config.json" ]]; then
  install -o vpsdash -g "$service_group" -m 600 "$repo_dir/config.example.json" "$service_home/.config/vpsdash/config.json"
fi
chown vpsdash:"$service_group" "$service_home/.config/vpsdash/config.json"
chmod 600 "$service_home/.config/vpsdash/config.json"

runtime_dir="/run/user/$service_uid"
for ((attempt=0; attempt<5; attempt++)); do
  if [[ -S "$runtime_dir/bus" ]]; then
    break
  fi
  sleep 1
done
runuser -u vpsdash -- env XDG_RUNTIME_DIR="$runtime_dir" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" systemctl --user daemon-reload
load_state="$(runuser -u vpsdash -- env XDG_RUNTIME_DIR="$runtime_dir" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" systemctl --user show vpsdash.service -p LoadState --value)"
linger="$(loginctl show-user vpsdash -p Linger --value)"
if [[ "$load_state" != loaded || "$linger" != yes ]] || \
  ! cmp -s "$binary" "$service_home/bin/vpsdash" || \
  ! cmp -s "$repo_dir/deploy/vpsdash.service" "$service_home/.config/systemd/user/vpsdash.service"; then
  echo 'A instalação não passou na verificação final.' >&2
  exit 1
fi
if [[ -x /snap/tailscale/current/bin/tailscale && \
      -S /var/snap/tailscale/common/socket/tailscaled.sock ]] && \
   ! cmp -s "$repo_dir/scripts/tailscale-status-snap.sh" "$service_home/bin/tailscale"; then
  echo 'A instalação do leitor Tailscale não passou na verificação final.' >&2
  exit 1
fi

echo 'vpsdash instalado; a unit foi carregada e o binário foi conferido.'
echo 'Revise config.json, crie auth.env e as chaves SSH antes de iniciar o serviço.'
