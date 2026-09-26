# vpsdash — spec completo (fase B)

Complementa `2026-09-24-vps-dashboard-design.md` (conceito aprovado). Este
documento fixa os detalhes de implementação que lá ficaram pendentes.

Data: 2026-09-26. Status: pronto para implementação; credenciais SMTP ainda
pendentes de coleta no host `allmedical-mail` (§SMTP).

## Nome do produto e repo

- Produto: **vpsdash** (nome definitivo, não placeholder).
- Repo: `heliowap/vpsdash`, privado. Segundo repo habilitado no gh-agents
  (dogfood — contrato §3 do design conceitual).
- Serviço/binário/usuário systemd/DB: todos `vpsdash`.

## GitHub App `gh-agents-ops`

Autenticação do painel com a API do GitHub. PAT foi descartado: expira,
escopo excessivo e prende a operação a uma conta pessoal.

- **Tipo**: GitHub App próprio, criado na conta `heliowap`.
- **Nome**: `gh-agents-ops` (slug gerado: `gh-agents-ops`; se ocupado,
  `vpsdash-ops`).
- **Repository permissions**:
  - `actions`: read/write — listar runs, re-run.
  - `administration`: read/write — listar/remover self-hosted runners.
  - `variables`: read/write — `PATCH
    /repos/{owner}/{repo}/actions/variables/{CI_RUNNER,AGENT_RUNNER}` (o
    switch do painel). Permissão própria — `administration` **não** cobre
    `actions/variables`. Chave no manifesto: `actions_variables`.
    **Não confundir** com `agent_variables`: permissão separada da
    família `/agents/variables` (variáveis de coding agent) — não é
    rename e não cobre o switch.
  - `metadata`: read (implícita).
- **Organization permissions**: `self-hosted runners`: read/write —
  `enable-org` futuro; `organization_actions_variables`: read/write —
  variáveis de org em `/orgs/{org}/actions/variables`, se o painel
  precisar (`organization_agent_variables` cobre `/agents/variables`,
  outra família). O switch em repo de org usa a permissão `variables`
  de repo, via instalação na org (`intrador`, `All-Medical`).
- **Sem** `contents`, sem `issues` — o painel não lê código nem posta.
- **Instalação**: conta `heliowap` (todos os repos) + org `intrador` +
  org `All-Medical` — instalação por org só quando #7 (enable orgs) sair
  do on-hold; até lá a instalação pessoal cobre o uso atual.
- **Credenciais no host**: App ID + private key PEM +
  installation IDs em `/home/vpsdash/.config/vpsdash/github-app.env`
  (`0600`, dono `vpsdash`). Token de instalação gerado por request
  (cache 50 min), nunca persistido.
- **Geração**: manifest JSON commitado em `vpsdash` (`app-manifest.json`),
  bootstrap via `POST /app-manifests/{code}/conversions` — fluxo
  documentado no README do repo.

## SQLite — schema, retenção, polling

Arquivo único `/home/vpsdash/vpsdash.db` (WAL). Sem ORM; migrations
versionadas (`schema_migrations`).

```sql
CREATE TABLE hosts (
  id            TEXT PRIMARY KEY,          -- 'intrador', 'allmedical-app', ...
  tailnet_name  TEXT NOT NULL,             -- nome tailscale
  kind          TEXT NOT NULL,             -- 'vps' | 'presence'
  ssh_user      TEXT,                      -- NULL em kind='presence'
  created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE metrics (                      -- ring buffer por host
  host_id     TEXT NOT NULL REFERENCES hosts(id),
  ts          INTEGER NOT NULL,            -- epoch s
  cpu_pct     REAL, mem_pct REAL, disk_pct REAL, uptime_s INTEGER
);
CREATE INDEX idx_metrics_host_ts ON metrics(host_id, ts);

CREATE TABLE projects (
  id          INTEGER PRIMARY KEY,
  host_id     TEXT NOT NULL REFERENCES hosts(id),
  name        TEXT NOT NULL,
  source      TEXT NOT NULL,               -- 'docker' | 'systemd' | 'tmux'
  monitored   INTEGER NOT NULL DEFAULT 0,  -- candidato=0, monitorado=1
  health_url  TEXT,                        -- critério explícito p/ alerta
  expected    TEXT,                        -- JSON: units/containers esperados
  UNIQUE(host_id, name, source)
);

CREATE TABLE checks (
  project_id  INTEGER NOT NULL REFERENCES projects(id),
  ts          INTEGER NOT NULL,
  ok          INTEGER NOT NULL,
  detail      TEXT
);
CREATE INDEX idx_checks_proj_ts ON checks(project_id, ts);

CREATE TABLE tmux_sessions (
  host_id   TEXT NOT NULL,
  name      TEXT NOT NULL,
  pane_pid  INTEGER,
  cwd       TEXT,
  agent     TEXT,                          -- 'opencode'|'codex'|'claude'|NULL
  state     TEXT,                          -- 'working'|'waiting'|'idle'|NULL (v2)
  seen_at   INTEGER NOT NULL,
  PRIMARY KEY (host_id, name)
);

CREATE TABLE runners (
  repo      TEXT NOT NULL,
  runner_id INTEGER NOT NULL,
  name      TEXT NOT NULL,
  status    TEXT NOT NULL,                 -- 'online'|'offline'
  busy      INTEGER NOT NULL,
  job       TEXT,                          -- job atual, se busy
  seen_at   INTEGER NOT NULL,
  PRIMARY KEY (repo, runner_id)
);

CREATE TABLE alerts (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,                -- 'project_down'|'runner_offline'|...
  subject    TEXT NOT NULL,
  body       TEXT NOT NULL,
  sent_at    INTEGER NOT NULL,
  channel    TEXT NOT NULL                 -- 'smtp' | 'webpush' (v2)
);
```

### Polling

| Coletor | Intervalo | Transporte | Nota |
|---|---|---|---|
| métricas CPU/mem/disco | 60 s | SSH (`/proc`, `df`, `uptime`) | um comando composto por host |
| presença tailnet | 30 s | `tailscale status --json` local | todos os dispositivos de uma vez |
| descoberta de projetos | 5 min | SSH (`docker ps`, `systemctl`, `tmux ls`) | só cria candidatos (`monitored=0`) |
| health de monitorados | 30 s | HTTP `health_url` ou `systemctl is-active` | falha 3× seguidas → alerta |
| sessões tmux | 60 s | SSH (`tmux ls`, `list-panes -F`) | inclui detecção de agente (§abaixo) |
| runners/fila GitHub | 30 s | REST via App (`/actions/runners`, `queued`) | burst de 10 s enquanto `busy` |

Polling é sequencial por host e paralelo entre hosts (um worker por host,
timeout SSH 10 s, circuit-breaker: 3 falhas → host `unreachable` por 5 min).

### Retenção

| Tabela | Retenção | Poda |
|---|---|---|
| `metrics` | 30 d | `DELETE … WHERE ts < now-30d`, nightly 03:00 |
| `checks` | 30 d | idem |
| `tmux_sessions`, `runners` | estado corrente | upsert por chave; ausente 10 min → delete |
| `alerts` | 90 d | nightly |
| DB | — | `VACUUM` semanal (domingo 03:30) |

## Detecção de agente e estado (assinaturas reais, 2026-09-26)

Por pane tmux (`tmux list-panes -F '#{pane_pid} #{pane_current_command}'`)
+ varredura da árvore de processos do pane:

| Agente | Assinatura de processo | Cwd → projeto |
|---|---|---|
| opencode | `comm=opencode` ou arg `opencode serve` / `opencode run` / `opencode github run` | `pane_current_path` |
| codex | `comm=codex`, arg contém `app-server` ou `codex exec`/`codex resume` | idem |
| claude | `comm=claude` ou `node` cujo argv contém `claude`/`@anthropic-ai/claude-code` | idem |

Estado (v2): `tmux capture-pane -t <sessão> -p` (últimas ~20 linhas) +
estado do processo (`R`/`S` em `/proc/<pid>/stat`):
- `waiting` — pane parado num prompt de permissão/input (heurística:
  última linha não-vazia casa padrões `❯`, `(y/n)`, `Allow?`, `› ` e
  processo em `S` há ≥30 s).
- `working` — CPU do pane >5% ou output mudando entre polls.
- `idle` — demais casos.

Heurística, não contrato: estado errado nunca bloqueia ação — send-keys
(v2) exige confirmação no painel.

## SMTP (`allmedical-mail` → it@intrador.com.br)

Contrato do coletor: `alerts` com `channel='smtp'` → fila interna → envio
via `allmedical-mail:587` STARTTLS, auth `vpsdash@intrador.com.br`
(senha em `/home/vpsdash/.config/vpsdash/smtp.env`).

**Pendente de coleta no host** (única pendência restante): confirmar porta
(587 vs 465), mecanismo de auth (PLAIN vs LOGIN) e criar a conta
`vpsdash@` — rodar uma vez: `openssl s_client -starttls smtp -connect
allmedical-mail:587` no próprio host e registrar o resultado aqui antes do
primeiro alerta real. Sem essa coleta, alerts ficam enfileirados e o painel
mostra o badge "canal de alerta não provisionado".

## Isolamento (inalterado do conceito)

Usuário `vpsdash` próprio; chaves SSH em `~vpsdash/.ssh` (ed25519 por host,
`command=` restrito onde possível). O painel lê tmux de `helio` via SSH
read-only — sem escrita fora de `send-keys` (v2, por trás de confirmação).

## DoD da implementação

- `vpsdash --version` e `systemd` unit user-level `vpsdash.service` sob
  `~vpsdash` (mesmo padrão das units da fleet — sem sudo).
- Todo coletor tem teste de parsing com fixture real capturada nesta VPS.
- `gh-agents` habilitado no repo desde o primeiro PR (dogfood).
- Primeira release: `v0.1` com hosts + projetos + fleet + switch + auth;
  tmux attach e alerts podem entrar em `v0.2` sem mudança de schema.
