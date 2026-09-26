# vps-dashboard — design conceitual

Painel pessoal "central de comando" na VPS `intrador-tech-vps`: saúde de VPSs
e projetos, acesso a arquivos, terminal web com tmux, e operação da fleet de
GitHub Actions runners (spec A). Um usuário, senha, só tailnet, mobile-first.

Data: 2026-09-24. Status: conceito aprovado; spec completo quando a
implementação chegar (depois do gh-agents). Integrações já contratadas aqui.

## Contexto

- ~15 dispositivos na tailnet: VPSs Linux (`allmedical-app`,
  `allmedical-mail`, `intrador`, esta), Macs de dev, Windows de clientes
  (presença apenas), iOS.
- Projetos rodam em mix de docker-compose, systemd e tmux.
- tmux = workspace persistente de agentes CLI (opencode, codex, claude) —
  o usuário deixa sessões rodando e acessa de qualquer lugar.
- Mail hub na tailnet: `allmedical-mail` (it@intrador.com.br) — alertas por
  e-mail. Porta/auth SMTP: **pendente na implementação**.
- Origem: conversa paralela de 2026-09-24, integrada ao planejamento.

## Arquitetura

Binário único Go servindo API + SPA React/Vite embutida (`embed.FS`).
Sem Node em produção. HTTPS via `tailscale serve`
(`https://intrador-tech-vps.<tailnet>.ts.net`) — requisito de PWA/Web Push.
Bind apenas na tailnet.

```
browser (PWA, mobile-first)
  │ HTTPS/WSS (tailnet only)
  ▼
vpsdash (systemd, usuário `vpsdash`)
  ├── SQLite local (hosts, projetos, checks, sessões, runners, alertas)
  ├── SSH pool (x/crypto/ssh) → métricas + arquivos + tmux das VPSs
  ├── PTY bridge (creack/pty + websocket) → terminal web / tmux attach
  ├── GitHub App "gh-agents-ops" → API runners/variables/runs/orgs
  ├── tailscale status --json → presença de dispositivos
  └── SMTP → it@intrador.com.br (alertas de produção)
```

Isolamento (contrato com gh-agents): usuários separados —
`helio` (sessões pessoais de tmux), `gh-agents` (runners, containers),
`vpsdash` (painel + chaves SSH). Job de runner comprometido não lê
`~vpsdash/.ssh` nem o secret de sessão do painel.

## Features

### v1
- **Hosts**: cards por VPS — online, CPU/mem/disco (sparkline, SQLite ring
  buffer ~30d), uptime. Windows = presença apenas (tailscale).
- **Projetos (híbrido)**: auto-descoberta varre docker/systemd/tmux e lista
  "candidatos" silenciosos; um clique promove a "monitorado" com critério
  explícito (serviços esperados, URL de health, unit ativa). Alerta só para
  monitorados.
- **Terminal web**: xterm.js + WebSocket → SSH PTY no host. Default do
  clique: browser; opção "abrir no terminal local" (comando/ssh://).
- **tmux**: lista sessões por host com agente detectado (processo no pane),
  cwd/projeto; attach com um clique; attach read-only (`-r`).
- **Fleet gh-agents**: cards por runner (idle/busy/offline, job atual,
  unit systemd), fila de jobs `queued`, restart/drenar unit.
- **Switch de runner**: dropdown por repo (`self-hosted`, `ubuntu-latest`,
  `depot-*`, `ubicloud-*`) → `PATCH /actions/variables/{CI_RUNNER,AGENT_RUNNER}`;
  bulk apply; presets nomeados (economia/rapidez/fallback); badge
  "switchable" só onde o padrão `vars.X || default` existe; para os demais,
  botão "adotar switch" que despacha `/oc` no repo.
- **Auth**: senha única (argon2/bcrypt), sessão com cookie assinado.

### v2
- **Web Push (VAPID)** — "agente esperando input", "CI vermelho", "runner
  offline"; e-mail fica para produção (roteamento por severidade).
- **Send-keys da notificação** — responder prompt de permissão do agente sem
  abrir terminal (`tmux send-keys`).
- **Estado de sessão de agente** — trabalhando / esperando input / idle
  (heurística `capture-pane` + estado do processo).
- Logs streaming de run em execução; minutos por backend (proxy de custo);
  snippets/botões de comando por host; histórico de incidentes por projeto.

## Contrato de integração com gh-agents (já vigente no design A)

1. Units `actions.runner.*` e `gh-agents-cleanup.timer` aparecem como
   projeto monitorado nativo.
2. O switch do painel opera **as mesmas variáveis** (`AGENT_RUNNER`,
   `CI_RUNNER`) que o workflow lê — nenhum mecanismo paralelo.
3. O repo do dashboard é o segundo habilitado no gh-agents (dogfood).
4. Credencial: GitHub App `gh-agents-ops` (manifest gerado pelo repo
   gh-agents), instalado em heliowap + orgs — permissions: `actions:write`,
   `administration` (runners), `variables` (o switch), `metadata`.

## Pendências para o spec completo (fase B)

- Credenciais SMTP do `allmedical-mail` (porta, TLS, auth).
- GitHub App: criar, escopos finais, instalação por conta/org.
- Detalhe de dados: schema SQLite, retenção, polling intervals.
- Detecção de agente/estado: mapear assinaturas de opencode/codex/claude.
- Nome do produto e do repo (placeholder: `vpsdash`).
