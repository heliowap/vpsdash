# vpsdash

Painel privado do operador da tailnet. A especificação de produto está em
`docs/specs/2026-09-26-vps-dashboard-spec.md`; `PRODUCT.md` e `DESIGN.md`
fixam comportamento visível e linguagem da interface.

- Nunca grave senhas, PEM, IDs de instalação ou endereços reais da tailnet no
  repositório. A configuração de exemplo usa um sufixo fictício.
- O processo escuta apenas em loopback; Tailscale Serve fornece HTTPS e o
  limite de acesso. Preserve o isolamento dos usuários `helio`, `gh-agents`
  e `vpsdash`.
- `AGENT_RUNNER` e `CI_RUNNER` são as únicas variáveis que o painel altera.
  Um repositório só mostra o switch quando seu workflow usa o padrão
  `vars.X || default` e foi marcado como verificado no inventário.
- Projetos descobertos permanecem silenciosos até promoção explícita. Três
  checks consecutivos falhos geram alerta.
- Para código Go, teste o seam público com fixture ou servidor fake. Para a
  UI, valide o fluxo no navegador em desktop e mobile. Rode `go test ./...`,
  `npm run build`, `actionlint .github/workflows/*.yml` e `shellcheck
  scripts/*.sh` antes de declarar uma mudança pronta.
