# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Confirmed by spec (`docs/specs/2026-09-24-vps-dashboard-design.md`): single Go
binary serving API + embedded React/Vite SPA (`embed.FS`), SQLite local store,
and `x/crypto/ssh` pool. Collector SSH keys are unique per host and limited
by a Python 3 forced command that accepts only read operations; the separate
`gh-agents` key may also restart or drain that account's own runner units. The
web terminal, tmux attach, and host snippets use a separate per-host key
(`restrict,pty`) and exist only on a second loopback listener reached by
Tailscale Serve, behind password step-up and an audit log. No Node in
production. The service binds to loopback;
a public HTTPS reverse proxy serves the login page, and `tailscale serve` can
also provide private tailnet access.

## Users

One user: Helio, personal infrastructure operator. Accessed over HTTPS from
the public address or the tailnet, mobile-first (iOS included). No multi-user,
no roles — single password, signed-cookie session.

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
a parallel mechanism. Public access uses HTTPS, the panel password, signed
sessions, and bounded login attempts; the service itself remains on loopback.

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
  repo dogfoods gh-agents. Runner service units and the cleanup timer appear
  as native monitored projects. The runner operations phase adds restart and
  drain of each `actions.runner.*` unit through the `gh-agents` forced
  command, without sudo and without new GitHub App permissions.

## Capabilities and Constraints

First release `v0.1` (phase B DoD): installable PWA with network-only API reads;
host metrics + ~30d sparklines; hybrid
project discovery (auto-detected candidates promoted explicitly to
"monitored", except native runner units); tmux session list with agent
detection; read-only fleet runner and queue state; per-repo runner-backend
switch via GitHub Actions variables,
including bulk apply and named presets; single-password auth. Alert events
queue in SQLite while SMTP is unprovisioned.

After `v0.1`, the session list also shows a heuristic agent state
(working/waiting/idle), brought forward from v2. It is read-only and never
blocks an action.

Runner operations phase (`v0.2`): restart and drain of runner units, with
inline confirmation and an audit record per operation. Drain waits until
neither GitHub nor the host reports a job, then stops the unit; it cannot
stop GitHub from assigning a job in the seconds before the stop.

`v0.2` file access is read-only: browse folders and view text inside roots
listed per host in the inventory and confirmed by an operator-owned roots
file on the host, which the panel cannot widen. Secret-looking names
(`.env*`, keys, `.ssh`, `.git`, credentials) stay visible but blocked;
binary, truncated, and blocked states are written out. No upload, edit, or
delete. Hosts without roots show no file destination.

Concept target beyond `v0.1`: xterm.js terminal over SSH PTY;
one-click read-only tmux attach; SMTP delivery. The phase B spec explicitly permits tmux attach and alerts in
`v0.2` without a schema change.

v2 (confirmed direction, not v1 scope): Web Push via VAPID, send-keys from
notifications, run log streaming,
minutes-per-backend cost proxy, command snippets, incident history.

Constraints: loopback bind plus HTTPS reverse proxy for the public address,
with optional private Tailscale Serve access; HTTPS required for
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
