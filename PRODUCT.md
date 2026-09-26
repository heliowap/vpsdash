# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Confirmed by spec (`docs/specs/2026-09-24-vps-dashboard-design.md`): single Go
binary serving API + embedded React/Vite SPA (`embed.FS`), SQLite local store,
`x/crypto/ssh` pool, `creack/pty` + WebSocket terminal bridge. No Node in
production. HTTPS via `tailscale serve`; binds to the tailnet only.

## Users

One user: Helio, personal infrastructure operator. Accessed from anywhere on
the tailnet, mobile-first (iOS included). No multi-user, no roles — single
password, signed-cookie session.

(Adjacent product context: the gh-agents fleet serves admins of enabled repos —
personal, `intrador`, `All-Medical`, third parties — but Helio alone operates
the fleet. The dashboard is a consumer/operator of that fleet, not
multi-tenant.)

## Product Purpose

Personal "central de comando" on `intrador-tech-vps`: health of VPSs and
projects, file access, web terminal into persistent tmux agent sessions, and
operation of the gh-agents GitHub Actions runner fleet — all from one panel,
usable from a phone. Success means Helio sees what's wrong and acts on it
without opening a laptop.

## Positioning

A single-operator command center that treats things generic dashboards don't
as first-class: persistent tmux sessions running CLI agents (opencode, codex,
claude), tailnet device presence, and the self-hosted Actions fleet — operated
through the same `AGENT_RUNNER`/`CI_RUNNER` variables the workflows read, never
a parallel mechanism. Tailnet-only by design: the network boundary is the
security model, not a feature gap.

## Operating Context

- ~15 tailnet devices: Linux VPSs (`allmedical-app`, `allmedical-mail`,
  `intrador`, `intrador-tech-vps`), dev Macs, client Windows machines
  (presence only), iOS.
- Projects run in a mix of docker-compose, systemd units, and tmux.
- tmux is the persistent workspace for CLI agents; sessions stay alive and are
  attached from anywhere.
- Mail hub `allmedical-mail` (it@intrador.com.br) sends production alerts;
  SMTP credentials pending.
- Runs as systemd service under dedicated `vpsdash` user; isolation contract
  with gh-agents separates `helio` (tmux), `gh-agents` (runners), `vpsdash`
  (panel + SSH keys).
- Integration contract with gh-agents is already in force (spec, "Contrato de
  integração"): runner units appear as monitored projects, the runner switch
  writes the same repo variables, GitHub App `gh-agents-ops` supplies
  runners/variables/runs API access, and the dashboard repo dogfoods gh-agents.

## Capabilities and Constraints

v1 (confirmed): host cards with metrics + ~30d sparklines; hybrid project
discovery (auto-detected candidates promoted explicitly to "monitored");
xterm.js web terminal over SSH PTY; tmux session list with agent detection,
one-click attach incl. read-only; fleet runner cards with restart/drain;
per-repo runner-backend switch via GitHub variables API incl. bulk apply and
named presets; single-password auth.

v2 (confirmed direction, not v1 scope): Web Push via VAPID, send-keys from
notifications, agent session state (working/waiting/idle), run log streaming,
minutes-per-backend cost proxy, command snippets, incident history.

Constraints: tailnet bind only; HTTPS required for PWA/Web Push; no Node in
production; Windows hosts are presence-only; OS-user isolation is a hard
contract; runner-switch badge only where the `vars.X || default` pattern
exists — others get an "adopt switch" action that dispatches `/oc`.

The name, repository, GitHub App permissions, SQLite schema and retention,
and agent signatures are fixed in the phase B spec. SMTP connection details
and the service account still need provisioning on the host.

## Evidence on Hand

- `docs/specs/2026-09-24-vps-dashboard-design.md` — dashboard spec, concept
  approved.
- `docs/specs/2026-09-24-gh-agents-design.md` — fleet spec, approved in
  conversation; defines the integration contract this product consumes.
- No imagery, logos, testimonials, or user research exist; none may be
  fabricated.

## Product Principles

- One glance, one tap: mobile-first; the phone scenario is the primary scene,
  not a fallback.
- Quiet by default: discovery is silent; only explicitly promoted projects
  alert. No noise, no false urgency.
- Operate the contract, not a side channel: switches and actions write the
  same variables and APIs the workflows read.
- Presence over control where trust is thin: read-only attach, presence-only
  Windows, diagnostics before fixes.
- Isolation is the product: OS-user separation is a security promise the UI
  must never blur.
