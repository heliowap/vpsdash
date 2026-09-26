# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Confirmed by spec (`docs/specs/2026-09-24-vps-dashboard-design.md`): single Go
binary serving API + embedded React/Vite SPA (`embed.FS`), SQLite local store,
and `x/crypto/ssh` pool. The terminal bridge is a later phase. No Node in
production. The service binds to loopback; `tailscale serve` provides private
HTTPS access.

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
- Integration contract with gh-agents (spec, "Contrato de integração"):
  the runner switch writes the same repo variables, GitHub App
  `gh-agents-ops` supplies runners/variables/runs API access, and the dashboard
  repo dogfoods gh-agents. Direct unit controls require the later runner
  operations phase.

## Capabilities and Constraints

First release `v0.1` (phase B DoD): installable PWA with network-only API reads;
host metrics + ~30d sparklines; hybrid
project discovery (auto-detected candidates promoted explicitly to
"monitored"); tmux session list with agent detection; read-only fleet runner
and queue state; per-repo runner-backend switch via GitHub Actions variables,
including bulk apply and named presets; single-password auth. Alert events
queue in SQLite while SMTP is unprovisioned.

Concept target beyond `v0.1`: file access; xterm.js terminal over SSH PTY;
one-click read-only tmux attach; runner unit display and restart/drain; SMTP
delivery. The phase B spec explicitly permits tmux attach and alerts in
`v0.2` without a schema change.

v2 (confirmed direction, not v1 scope): Web Push via VAPID, send-keys from
notifications, agent session state (working/waiting/idle), run log streaming,
minutes-per-backend cost proxy, command snippets, incident history.

Constraints: loopback bind plus private Tailscale Serve; HTTPS required for
PWA/Web Push; no Node in production; Windows hosts are presence-only; OS-user
isolation is a hard contract. The runner switch appears only where the
`vars.X || default` pattern was confirmed in inventory. The phase B GitHub
App has no `issues` or `contents` permission, so the panel flags adoption
as pending and does not dispatch `/oc`.

The name, repository, GitHub App permissions, SQLite schema and retention,
and agent signatures are fixed in the phase B spec. The service account,
GitHub App installation, and SMTP connection still need provisioning.

## Evidence on Hand

- `docs/specs/2026-09-24-vps-dashboard-design.md` — dashboard spec, concept
  approved.
- The gh-agents fleet design in the source repository defines the integration
  contract this product consumes.
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
