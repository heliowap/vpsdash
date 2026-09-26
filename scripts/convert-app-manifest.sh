#!/usr/bin/env bash
# Exchange the one-time GitHub App manifest code without printing the PEM.
set -euo pipefail

code="${1:-}"
target_dir="${2:-/home/vpsdash/.config/vpsdash}"
if [[ ! "$code" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo 'usage: convert-app-manifest.sh <one-time-code> [target-directory]' >&2
  exit 2
fi

if ! getent passwd vpsdash >/dev/null; then
  echo 'Crie o usuário vpsdash antes de converter o manifesto.' >&2
  exit 1
fi
sudo -v
sudo install -d -o vpsdash -g vpsdash -m 700 "$target_dir"
umask 077
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT
gh api -X POST "app-manifests/${code}/conversions" > "$temp_dir/response.json"
jq -er '.pem' "$temp_dir/response.json" > "$temp_dir/github-app.pem"
app_id="$(jq -er '.id' "$temp_dir/response.json")"
cat > "$temp_dir/github-app.env" <<EOF
GITHUB_APP_ID=$app_id
GITHUB_PRIVATE_KEY_FILE=$target_dir/github-app.pem
GITHUB_INSTALLATIONS_JSON={}
EOF
sudo install -o vpsdash -g vpsdash -m 600 "$temp_dir/github-app.pem" "$target_dir/github-app.pem"
sudo install -o vpsdash -g vpsdash -m 600 "$temp_dir/github-app.env" "$target_dir/github-app.env"
echo "App ID $app_id; arquivos privados salvos em $target_dir. Preencha os installation IDs após instalar o App." >&2
