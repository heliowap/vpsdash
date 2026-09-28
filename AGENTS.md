# vpsdash

Painel de operador único com acesso público por HTTPS e login. A especificação de produto está em
`docs/specs/2026-09-26-vps-dashboard-spec.md`; `PRODUCT.md` e `DESIGN.md`
fixam comportamento visível e linguagem da interface.

- Nunca grave senhas, PEM, IDs de instalação ou endereços reais da tailnet no
  repositório. A configuração de exemplo usa um sufixo fictício.
- O processo escuta apenas em loopback. O proxy público fornece HTTPS, define
  `X-Real-IP` a partir da conexão recebida e encaminha para a porta local; o
  login limita tentativas por cliente e globalmente. Tailscale Serve continua
  disponível como rota privada. Preserve o isolamento dos usuários `helio`,
  `gh-agents` e `vpsdash`. A chave `gh-agents` só lê as units e reinicia ou
  drena as próprias `actions.runner.*` por `systemctl --user`, sem sudo; a
  chave de `helio` continua somente leitura.
- `AGENT_RUNNER` e `CI_RUNNER` são as únicas variáveis que o painel altera.
  Um repositório só mostra o switch quando seu workflow usa o padrão
  `vars.X || default` e foi marcado como verificado no inventário.
- Projetos descobertos permanecem silenciosos até promoção explícita. Três
  checks consecutivos falhos geram alerta.
- Para código Go, teste o seam público com fixture ou servidor fake. Para a
  UI, valide o fluxo no navegador em desktop e mobile. Rode `go test ./...`,
  `npm run build`, `actionlint .github/workflows/*.yml` e `shellcheck
  scripts/*.sh` antes de declarar uma mudança pronta.
