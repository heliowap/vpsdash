# vpsdash

Painel de operador único com acesso público por HTTPS e login. A especificação de produto está em
`docs/specs/2026-09-26-vps-dashboard-spec.md`; `PRODUCT.md` e `DESIGN.md`
fixam comportamento visível e linguagem da interface.

- Nunca grave senhas, PEM, IDs de instalação ou endereços reais da tailnet no
  repositório. A configuração de exemplo usa um sufixo fictício.
<<<<<<< HEAD
- O processo escuta apenas em loopback. O proxy público fornece HTTPS, define
  `X-Real-IP` a partir da conexão recebida e encaminha para a porta local; o
  login limita tentativas por cliente e globalmente. Tailscale Serve continua
  disponível como rota privada. Preserve o isolamento dos usuários `helio`,
  `gh-agents` e `vpsdash`. A chave `gh-agents` só lê as units e reinicia ou
  drena as próprias `actions.runner.*` por `systemctl --user`, sem sudo; a
  chave de `helio` continua somente leitura.
=======
- O processo escuta apenas em loopback, em dois listeners. `listen`
  (`127.0.0.1:8484`) recebe o proxy público, que fornece HTTPS, define
  `X-Real-IP` a partir da conexão recebida e encaminha para essa porta; o
  login limita tentativas por cliente e globalmente. `private_listen`
  (`127.0.0.1:8485`, desligado quando ausente) recebe somente o Tailscale
  Serve. Terminal, attach tmux e snippets são registrados apenas no mux do
  listener privado; no público respondem 404. A decisão vem do listener que
  recebeu a conexão, nunca de cabeçalhos. Nunca aponte o proxy público para
  `private_listen`.
- Toda ação interativa exige sessão, CSRF, reautenticação por senha válida
  por 10 minutos e ligada à sessão, e WebSocket com Origin igual ao Host;
  cada abertura, recusa e encerramento vai para `audit_log` (hora, ação,
  host, alvo, IP e resultado, nunca teclas ou saída). Attach é `tmux
  attach -r` por padrão; escrita pede confirmação explícita. Snippets vêm
  só do inventário (argv fixo, cada palavra entre aspas simples, sem
  argumento do usuário).
- Preserve o isolamento dos usuários `helio`, `gh-agents` e `vpsdash`. A
  coleta usa chaves com a ponte `ssh-readonly.py` (somente leitura,
  scripts aprovados por SHA-256) e não muda. O acesso interativo usa uma
  chave própria por host (`interactive_key_file`, criada por
  `scripts/setup-interactive-key.sh`), autorizada em `helio` com
  `restrict,pty`, sem encaminhamento de porta, agente ou X11, e nunca
  reutiliza uma chave de coleta.
>>>>>>> 4a91175 (docs: describe the private interactive contract)
- `AGENT_RUNNER` e `CI_RUNNER` são as únicas variáveis que o painel altera.
  Um repositório só mostra o switch quando seu workflow usa o padrão
  `vars.X || default` e foi marcado como verificado no inventário.
- Projetos descobertos permanecem silenciosos até promoção explícita. Três
  checks consecutivos falhos geram alerta.
- Para código Go, teste o seam público com fixture ou servidor fake. Para a
  UI, valide o fluxo no navegador em desktop e mobile. Rode `go test ./...`,
  `npm run build`, `actionlint .github/workflows/*.yml` e `shellcheck
  scripts/*.sh` antes de declarar uma mudança pronta.
