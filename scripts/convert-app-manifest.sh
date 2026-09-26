#!/usr/bin/env bash
# Exchange a one-time GitHub App manifest code without exposing it in argv.
set -euo pipefail

target_dir="${1:-/home/vpsdash/.config/vpsdash}"
operator=helio
operator_home="$(getent passwd "$operator" | cut -d: -f6)"

if [[ -t 0 ]]; then
  printf 'Código de uso único do manifesto: ' >&2
  IFS= read -rs code
  printf '\n' >&2
else
  IFS= read -r code
fi
if [[ ! "$code" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo 'Código de manifesto inválido.' >&2
  exit 2
fi
if ! getent passwd vpsdash >/dev/null; then
  echo 'Crie o usuário vpsdash antes de converter o manifesto.' >&2
  exit 1
fi
if (( EUID == 0 )); then
  as_root() { "$@"; }
  as_operator() { runuser -u "$operator" -- env -u GH_TOKEN -u GITHUB_TOKEN HOME="$operator_home" "$@"; }
else
  if [[ "$(id -un)" != "$operator" ]]; then
    echo 'Execute como helio ou com sudo.' >&2
    exit 1
  fi
  sudo -v
  as_root() { sudo "$@"; }
  as_operator() { "$@"; }
fi
if [[ -e "$target_dir/github-app.pem" || -e "$target_dir/github-app.env" ]]; then
  echo 'O GitHub App já tem credenciais neste destino; não serão sobrescritas.' >&2
  exit 1
fi
operator_gh="$(as_operator bash -lc 'command -v gh' || true)"
if [[ -z "$operator_gh" ]]; then
  echo 'gh precisa estar instalado para helio.' >&2
  exit 1
fi
as_operator "$operator_gh" auth status >/dev/null 2>&1 || {
  echo 'gh precisa estar autenticado como helio.' >&2
  exit 1
}

umask 077
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT
printf '%s' "$code" | as_operator python3 -c '
import subprocess
import sys
import urllib.error
import urllib.request

code = sys.stdin.read().strip()
token = subprocess.check_output([sys.argv[1], "auth", "token"], text=True).strip()
request = urllib.request.Request(
    "https://api.github.com/app-manifests/" + code + "/conversions",
    data=b"",
    method="POST",
    headers={"Accept": "application/vnd.github+json", "Authorization": "Bearer " + token,
             "X-GitHub-Api-Version": "2026-03-10"},
)
try:
    with urllib.request.urlopen(request, timeout=30) as response:
        sys.stdout.buffer.write(response.read())
except urllib.error.HTTPError as error:
    raise SystemExit("Falha ao converter o manifesto: HTTP " + str(error.code))
except urllib.error.URLError:
    raise SystemExit("Falha de rede ao converter o manifesto.")
' "$operator_gh" > "$temp_dir/response.json"
jq -er '.pem | strings' "$temp_dir/response.json" > "$temp_dir/github-app.pem"
app_id="$(jq -er '.id | numbers' "$temp_dir/response.json")"
cat > "$temp_dir/github-app.env" <<EOF
GITHUB_APP_ID=$app_id
GITHUB_PRIVATE_KEY_FILE=$target_dir/github-app.pem
GITHUB_INSTALLATIONS_JSON={}
EOF
as_root install -d -o vpsdash -g vpsdash -m 700 "$target_dir"
as_root install -o vpsdash -g vpsdash -m 600 "$temp_dir/github-app.pem" "$target_dir/github-app.pem"
as_root install -o vpsdash -g vpsdash -m 600 "$temp_dir/github-app.env" "$target_dir/github-app.env"
echo "App ID $app_id; arquivos privados salvos em $target_dir. Preencha os installation IDs após instalar o App." >&2
