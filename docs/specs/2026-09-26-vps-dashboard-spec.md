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
  - `actions`: read — listar runs. Re-run fica para uma fase posterior e
    exigirá elevar a permissão do App antes de expor o controle no painel.
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
  state     TEXT,                          -- 'working'|'waiting'|'idle'|NULL
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
timeout SSH 10 s, circuit-breaker: 3 falhas de comando SSH → host
`unreachable` por 5 min; erros de parsing e armazenamento ficam no coletor
específico e não derrubam a presença do host).

### Retenção

| Tabela | Retenção | Poda |
|---|---|---|
| `metrics` | 30 d | `DELETE … WHERE ts < now-30d`, nightly 03:00 |
| `checks` | 30 d | idem |
| `tmux_sessions` | último snapshot confirmado por host | substituído após coleta completa; falha conserva a observação e a UI marca "Não confirmado" |
| `runners` | estado corrente | upsert por chave; ausente 10 min → delete; falha da frota marca estado incerto na UI |
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

Estado (antecipado da v2): `tmux capture-pane -p` de cada pane (tela
visível) + estado e CPU acumulada do processo (`ps -o stat=,times=`). O host
devolve só o checksum da tela e a última linha não-vazia (até 160
caracteres); o painel guarda apenas a amostra anterior em memória e nunca
persiste o conteúdo do pane. Com uma leitura por minuto, "há ≥30 s" equivale a
tela e CPU estáveis entre duas leituras consecutivas do mesmo pane:
- `waiting` — pane parado num prompt de permissão/input (heurística:
  última linha não-vazia casa padrões `❯`, `(y/n)`, `Allow?`, `› ` e
  processo em `S` há ≥30 s).
- `working` — CPU da árvore do pane >5% ou output mudando entre polls
  (tem precedência sobre `waiting`).
- `idle` — demais casos.
- `NULL` — primeira leitura, pane trocado ou sessão sem agente: o painel não
  presume estado.

Heurística, não contrato: estado errado nunca bloqueia ação — send-keys
(v2) exige confirmação no painel.

## SMTP (`allmedical-mail` → it@intrador.com.br)

Contrato do coletor: `alerts` com `channel='smtp'` → fila interna → envio
via `allmedical-mail:587` STARTTLS, auth `vpsdash@intrador.com.br`
(senha em `/home/vpsdash/.config/vpsdash/smtp.env`).

Roteamento por severidade (v2): `project_down` e `runner_offline` geram uma
linha `smtp` e, havendo dispositivo inscrito, uma linha `webpush`;
`agent_waiting` gera só `webpush`. A linha `webpush` se desdobra em
`push_deliveries` (uma por inscrição em `push_subscriptions`) e recebe
`sent_at` quando todas as entregas terminam. Chaves VAPID em
`/home/vpsdash/.config/vpsdash/webpush.env` (`0600`).

**Pendente de coleta no host** (única pendência restante): confirmar porta
(587 vs 465), mecanismo de auth (PLAIN vs LOGIN) e criar a conta
`vpsdash@` — rodar uma vez: `openssl s_client -starttls smtp -connect
allmedical-mail:587` no próprio host e registrar o resultado aqui antes do
primeiro alerta real. Sem essa coleta, alerts ficam enfileirados e o painel
mostra o badge "canal de alerta não provisionado".

Tentativa de coleta em 2026-09-26 no `intrador-tech-vps`: o dispositivo
`allmedical-mail` apareceu online na tailnet, mas conexões SMTP sem
autenticação às portas 587 e 465 expiraram após 8 s. Porta, TLS e mecanismos
de auth continuam sem confirmação; verificar serviço e firewall no host de
e-mail antes de criar a conta e ativar o canal.

## Operação de units de runner (fase de operações, `v0.2`)

Restart e drenagem das units `actions.runner.<runner-name>.service` do
`gh-agents` (issue #7).

- **Mecanismo privilegiado mínimo**: a ponte SSH forçada da chave
  `gh-agents` (`ssh-readonly.py runner-units`) aceita, além da leitura por
  digest, exatamente `runner-restart <unit>` e `runner-drain <unit>`. A unit
  precisa casar com `actions\.runner\.[A-Za-z0-9._-]+\.service` e existir
  em `~/.config/systemd/user/` da própria conta. `systemctl --user` roda com
  lista de argumentos, sem shell e sem sudo. A chave de `helio` continua
  somente leitura; o GitHub App não ganha permissão.
- **Restart**: `systemctl --user restart`. A unit gerada pelo `lib.sh` do
  gh-agents usa `KillSignal=SIGTERM`; o `runsvc.sh` repassa o sinal ao
  runner, que encerra e cancela o job em andamento. O painel avisa isso na
  confirmação.
- **Drenagem**: o runner não oferece pausa, e a API do GitHub só permite
  remover o registro. O painel então espera: a cada 15 s, se a última
  leitura do GitHub (até 2 min) não mostra o runner ocupado, envia
  `runner-drain`; a ponte recusa (saída 75) enquanto houver `Runner.Worker`
  no cgroup da unit e, sem ele, executa `systemctl --user stop`. Limite de
  2 h; cancelável. Resta uma janela de segundos em que o GitHub pode atribuir
  um job antes da parada.
- **Estado**: tabela `runner_unit_ops` (migração 7) com ação, status
  (`running|done|failed|cancelled|expired`), detalhe e horários; a última
  operação por unit vai em `unit_ops` no `/api/dashboard`. Após cada
  operação o coletor relê as units. Unit parada por drenagem concluída,
  com operação do painel em andamento ou ainda `activating`/`deactivating`
  até 2 min após um reinício registra o check como falho e planejado
  (`checks.planned`, migração 8): não conta para as três falhas seguidas,
  interrompe a sequência e não gera `runner_offline`; a UI a mostra como
  drenada ou reiniciando, fora da lista de incidentes. Drenagem cancelada,
  expirada ou interrompida com `runner-drain` em andamento lê o estado da
  unit com contexto novo: parada vira `done` (drenada); ativa mantém o
  status com o estado lido; sem leitura, fica com
  `runner_unit_ops.stop_unconfirmed` (migração 9) e a próxima leitura das
  units resolve. Operações de drenagem abandonadas no início do serviço
  também ficam não confirmadas. Retenção de 90 dias, preservando a última operação de cada unit.
- **API**: `POST /api/runner-units/{host}/{unit}/restart`, `.../drain` e
  `.../drain/cancel`, com sessão e CSRF. Só aceita units nativas já
  observadas em um host de `runner_unit_hosts`; uma operação por unit.

## Isolamento

Usuário `vpsdash` próprio; chaves SSH em `~vpsdash/.ssh` (ed25519 por host,
`command=` restrito onde possível). A coleta lê tmux de `helio` via SSH
read-only pela ponte `ssh-readonly.py`, que não muda.

## Terminal, attach e snippets (v0.2, rota privada)

Emenda de 2026-09-27 (issues #5, #6 e #14). Decisões do dono:

- **Dois listeners em loopback.** `listen` (`127.0.0.1:8484`) atende o
  proxy público; `private_listen` (`127.0.0.1:8485`, desligado se ausente)
  atende só o Tailscale Serve. As rotas interativas (`/api/step-up`,
  `/api/interactive`, `/api/terminal/tickets`, `/api/terminal/ws`,
  `/api/hosts/{id}/snippets/run`) existem apenas no mux privado; o público
  responde 404. Nenhum cabeçalho participa da decisão. `/api/session` e
  `/api/dashboard` informam `private` conforme o listener.
- **Reautenticação.** Senha de novo antes de terminal, attach ou snippet;
  vale 10 min, fica em memória ligada à sessão (HMAC do cookie) e usa os
  limites do login. Rotas mutáveis exigem CSRF; o WebSocket usa ticket de
  uso único (30 s, ligado à sessão) e `Origin` igual ao `Host`.
- **Chave separada.** `interactive_key_file` por host, nunca igual a uma
  chave de coleta, autorizada em `helio` com `restrict,pty`
  (`scripts/setup-interactive-key.sh`). Host sem a chave não oferece acesso.
- **Terminal.** xterm.js (build web) → WebSocket (`github.com/coder/websocket`)
  → PTY SSH (`x/crypto/ssh`); sem `creack/pty`, pois o PTY é remoto.
  Mensagens binárias levam bytes; texto leva `resize`, `ready` e `exit`.
  Até 4 sessões interativas, fechamento após 15 min sem digitação, ping a
  cada 30 s. Clique padrão abre no navegador; "Terminal local" mostra o
  comando `ssh`.
- **Attach.** `tmux attach-session -r -t =<sessão>` por padrão; escrita é
  segunda opção com confirmação (`confirm_write`). A sessão precisa constar
  de `tmux_sessions` para aquele host.
- **Snippets.** Lista fixa `{name, argv, timeout_seconds?}` por host no
  inventário; o pedido traz só o nome. `argv` vai palavra a palavra entre
  aspas simples; timeout padrão 30 s (máx. 300 s), saída limitada a 64 KiB.
- **Auditoria.** Migração 11:

```sql
CREATE TABLE audit_log (
  id        INTEGER PRIMARY KEY,
  ts        INTEGER NOT NULL,
  action    TEXT NOT NULL,   -- step_up|terminal|attach_ro|attach_rw|snippet
  host_id   TEXT NOT NULL DEFAULT '',
  target    TEXT NOT NULL DEFAULT '',   -- sessão tmux ou nome do snippet
  client_ip TEXT NOT NULL DEFAULT '',
  outcome   TEXT NOT NULL    -- ok|denied|failed|closed
);
```

  Nunca guarda teclas nem saída. Retenção de 90 dias na poda noturna.

## Acesso a arquivos (v0.2, issue #8)

Escopo aprovado: **somente leitura** (listar pastas e exibir texto), confinado
a raízes explícitas por host. Sem upload, edição, renomeação ou download de
binários. Sem schema novo: nada é persistido no SQLite.

- **Duas listas de raízes.** `file_roots` no inventário (`config.json`) liga
  a aba e filtra pedidos no painel. No host, a ponte `ssh-readonly.py` aceita
  somente as raízes de `~/.config/vpsdash-files/roots` (ou do caminho fixado
  com `--file-roots` no `command=` de `authorized_keys`). O arquivo precisa
  pertencer à conta SSH ou a root e não pode ter escrita para grupo/outros,
  nem a pasta. O painel não tem comando que altere esse arquivo; o acesso
  efetivo é a interseção das duas listas.
- **Opt-in.** `config.example.json` não define `file_roots`, então a
  instalação padrão não liga a leitura. Sem `file_roots`, a leitura fica
  desativada e a aba não aparece. O assistente só grava o arquivo de raízes
  de um host quando o inventário privado define `file_roots` para ele e o
  operador responde sim (padrão não) a uma pergunta que lista os caminhos
  exatos.
- **Protocolo.** Mesma chave e mesmo `command=` da coleta. Comandos
  `vpsdash-files list <caminho-base64url>` e
  `vpsdash-files read <caminho-base64url> <offset>`; resposta JSON em uma
  linha, recusa como `{"error": "<código>"}` (`disabled`, `roots_insecure`,
  `invalid`, `outside`, `blocked`, `not_found`, `not_dir`, `not_file`,
  `unreadable`). Comando malformado segue a recusa geral da ponte (exit 126).
  Os modos existentes (`sh -s` com digest aprovado, `vpsdash-health`,
  `runner-units`) não mudam; a chave `gh-agents` não lê arquivos.
- **Confinamento.** Caminho absoluto e normalizado; `realpath` precisa ficar
  sob a raiz resolvida; abertura com `O_NOFOLLOW` e conferência de
  `/proc/self/fd/N` contra o caminho resolvido (troca por link entre a
  checagem e a abertura é recusada). Leitura só de arquivos regulares, com
  `O_NONBLOCK` para que FIFOs não travem a ponte.
- **Segredos.** Regras por componente do caminho, iguais em Python e Go
  (`internal/files/rules.go`): `.env*`, `*.env`, `*.pem`, `*.key`, `*.p12`,
  `*.pfx`, `*.jks`, `*.keystore`, `*.kdbx`, `*.gpg`, `id_*`, `*secret*`,
  `*credential*`, `*password*`, `*passwd*`, `*_history`, `.netrc`, `.npmrc`,
  `.pypirc`, `.pgpass`, `.htpasswd`, `.vault-token`, `shadow`, `gshadow` e o
  conteúdo de `.ssh`, `.gnupg`, `.git`, `.aws`, `.azure`, `.kube`, `.docker`,
  `.password-store`. Na listagem essas entradas aparecem como bloqueadas, sem
  tamanho nem data; a leitura é recusada.
- **Limites.** Página de 512 KB (o corte recua até 3 bytes para não partir um
  caractere UTF-8); `truncated` e `next_offset` permitem pedir o restante.
  Byte nulo ou UTF-8 inválido → `binary`, sem conteúdo. Pastas: até 1000
  entradas, com `truncated`.
- **API.** `GET /api/hosts/{id}/files?path=` e
  `GET /api/hosts/{id}/file?path=&offset=`, atrás da sessão; o host precisa
  ser uma VPS do inventário. SSH por conexão própria (`files:<host>`),
  timeout de 10 s.
- **`local: true`.** O mesmo código de regras roda em Go no processo
  `vpsdash`, com as permissões dessa conta e somente o `file_roots` do
  inventário. Os testes executam a ponte Python e a implementação Go sobre a
  mesma árvore (links de fuga, `..`, segredos, binário, arquivo grande, FIFO,
  nome não UTF-8) e exigem respostas idênticas.

## DoD da implementação

- `vpsdash --version` e `systemd` unit user-level `vpsdash.service` sob
  `~vpsdash` (mesmo padrão das units da fleet — sem sudo).
- Todo coletor tem teste de parsing com fixture real capturada nesta VPS.
- `gh-agents` habilitado no repo desde o primeiro PR (dogfood).
- Primeira release: `v0.1` com hosts + projetos + fleet + switch + auth;
  tmux attach e alerts podem entrar em `v0.2` sem mudança de schema.
