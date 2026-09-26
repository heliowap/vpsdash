# vpsdash

Painel privado de operação da tailnet: saúde de hosts e projetos, sessões
tmux, runners do gh-agents e troca de backend pelo mesmo `AGENT_RUNNER` /
`CI_RUNNER` que os workflows leem. Um operador, acesso por senha, somente
atrás de `tailscale serve`.

## Estado da implementação

O binário Go serve a API e a SPA React compilada, sem Node em produção.
SQLite guarda 30 dias de métricas e checks, estado corrente de runners e
sessões, e até 90 dias de alertas enviados. A descoberta de projetos cria
candidatos silenciosos; somente projetos promovidos são verificados e podem
gerar alerta após três falhas. Runners e fila vêm da API do GitHub App. O
switch altera as variáveis Actions do próprio repositório.

O canal SMTP mostra "não provisionado" e mantém eventos pendentes enquanto
`smtp.env` não existir. A lista tmux detecta o agente; o terminal web e o
attach ficam para `v0.2`, conforme a especificação. A interface não afirma
que um host está saudável antes da primeira leitura.

O painel pode ser instalado na tela inicial como PWA pelo navegador da
tailnet. O service worker guarda somente a interface estática; chamadas
`/api/` continuam na rede e nunca são servidas do cache. Sem conexão, o
painel não apresenta uma leitura antiga como estado atual.

## Desenvolver

Requer Go 1.27.1 e Node 22 para o build. Node não é usado pelo serviço.

```bash
cd web && npm ci && npm run build && cd ..
go test ./...
go build -trimpath -o bin/vpsdash ./cmd/vpsdash
bin/vpsdash --version
```

O build do Vite escreve `internal/web/dist/`; esses arquivos são incorporados
no binário por `embed.FS`. Para desenvolvimento, `npm run dev` em `web/`
encaminha `/api` para `127.0.0.1:8484`.

## Configuração do host

Instale o serviço sob o usuário próprio `vpsdash`, separado de `helio` e
`gh-agents`. A criação do usuário e a instalação do serviço exigem acesso
administrativo ao host:

```bash
sudo useradd --create-home --shell /bin/bash vpsdash
sudo loginctl enable-linger vpsdash
sudo install -d -o vpsdash -g vpsdash -m 700 /home/vpsdash/.config/vpsdash
sudo install -d -o vpsdash -g vpsdash -m 700 /home/vpsdash/.config/systemd/user
sudo install -d -o vpsdash -g vpsdash -m 700 /home/vpsdash/bin
sudo install -o vpsdash -g vpsdash -m 755 bin/vpsdash /home/vpsdash/bin/vpsdash
sudo install -o vpsdash -g vpsdash -m 600 config.example.json /home/vpsdash/.config/vpsdash/config.json
sudo install -o vpsdash -g vpsdash -m 644 deploy/vpsdash.service /home/vpsdash/.config/systemd/user/vpsdash.service
```

Edite `config.json`: substitua o sufixo fictício `tailnet.ts.net`, ajuste
usuários SSH e liste somente repositórios instalados no GitHub App. Defina
`agent_switchable` ou `ci_switchable` como `true` apenas depois de verificar
que o workflow daquele repositório contém `vars.AGENT_RUNNER || ...` ou
`vars.CI_RUNNER || ...`, respectivamente. Repositórios sem esse contrato não
recebem o controle de troca no painel.

Como `vpsdash`, gere a senha e a chave de sessão:

```bash
sudo -iu vpsdash /home/vpsdash/bin/vpsdash init-auth
```

O comando pede a senha no terminal e escreve `auth.env` com modo `0600`.
Adicione uma chave ed25519 própria em `~vpsdash/.ssh/`, autorize leitura SSH
nos hosts Linux e confirme cada host key em `known_hosts`. O painel executa
comandos de coleta com timeout de 10 s; a chave não deve permitir acesso aos
arquivos de `gh-agents`. Windows entra pela presença Tailscale, sem SSH.

Inicie o serviço:

```bash
vpsdash_uid="$(id -u vpsdash)"
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user daemon-reload
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user enable --now vpsdash.service
sudo -u vpsdash XDG_RUNTIME_DIR="/run/user/$vpsdash_uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$vpsdash_uid/bus" \
  systemctl --user status vpsdash.service
```

Antes de publicar na tailnet, confira a configuração atual de Serve com
`tailscale serve status`. O backend aceita somente loopback. Configure HTTPS
privado para `127.0.0.1:8484` e verifique o domínio `*.ts.net` no navegador:

```bash
sudo tailscale serve --bg 127.0.0.1:8484
```

Use **Serve**, não Funnel. O cookie de sessão exige HTTPS. A sintaxe de
`tailscale serve` segue a [documentação oficial](https://tailscale.com/docs/reference/tailscale-cli/serve).

## GitHub App `gh-agents-ops`

`app-manifest.json` pede `actions:write`, `administration:write`,
`actions_variables:write`, `organization_self_hosted_runners:write` e
`organization_actions_variables:write`. Não pede `contents` nem `issues`.
Metadata é implícita. Sem webhook ativo.

O fluxo de manifesto do GitHub exige uma confirmação no navegador. Em uma
máquina com navegador e este checkout:

```bash
python3 scripts/manifest-form.py > /tmp/vpsdash-app-form.html
xdg-open /tmp/vpsdash-app-form.html
```

Revise o manifesto no GitHub e crie o App na conta `heliowap`. O navegador
volta ao repositório com `?code=...` na URL. Troque esse código de uso único
em até uma hora, **sem imprimir a resposta**:

```bash
scripts/convert-app-manifest.sh '<code>' /home/vpsdash/.config/vpsdash
```

O script verifica o acesso `sudo` antes de consumir o código, chama
`POST /app-manifests/{code}/conversions` e instala o PEM e o
`github-app.env` como `vpsdash`, modo `0600`. Instale o App nos repositórios
desejados, obtenha os IDs de instalação e edite `GITHUB_INSTALLATIONS_JSON`
com `sudoedit /home/vpsdash/.config/vpsdash/github-app.env`, por exemplo
`{"heliowap":123456}`. Reinicie o serviço.
Tokens de instalação são gerados por request, mantidos em memória por até
50 minutos e nunca persistidos. As instalações nas organizações `intrador`
e `All-Medical` aguardam o rollout delas no gh-agents.

## SMTP

O arquivo privado `/home/vpsdash/.config/vpsdash/smtp.env` aceita
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_TO` e
`SMTP_AUTH=plain|login`. O serviço exige STARTTLS na porta 587 ou TLS
implícito na 465. Antes de provisionar, confirme porta, TLS, mecanismo de
autenticação e crie `vpsdash@intrador.com.br` em `allmedical-mail`. Alertas
pendentes permanecem na tabela `alerts` com `sent_at=0`.

Em 2026-09-26, o peer apareceu online na tailnet, mas conexões às portas 587
e 465 expiraram a partir do `intrador-tech-vps`. Verifique o serviço e o
firewall no host de e-mail antes de configurar `smtp.env`.

## gh-agents

`.github/workflows/agents.yml` chama
`heliowap/gh-agents/.github/workflows/agents.yml@v1` desde o primeiro PR.
Defina `OPENCODE_API_KEY` no repositório. Até haver um runner privado
registrado para este repositório, defina `AGENT_RUNNER=ubuntu-latest`; depois
o painel pode trocar para `self-hosted`. O CI usa
`vars.CI_RUNNER || 'ubuntu-latest'`.

## Verificar

```bash
cd web && npm ci && npm run build && cd ..
go test ./...
actionlint .github/workflows/*.yml
shellcheck scripts/*.sh
go build -o /tmp/vpsdash ./cmd/vpsdash
/tmp/vpsdash --version
```

Os parsers têm fixtures capturadas em `intrador-tech-vps`, com nomes de
tailnet sanitizados. Um teste de navegador do fluxo login → hosts → projetos
está em `web/tests/overview.plan.json` para execução TestSprite com
`--local 8484`. Chaves, senha, IDs de instalação e endereços reais ficam
fora do repositório.
