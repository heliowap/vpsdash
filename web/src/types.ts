export type Metric = { ts: number; cpu_pct?: number; mem_pct: number; disk_pct: number; uptime_s: number }
export type Host = { id: string; tailnet_name: string; kind: 'vps' | 'presence'; ssh_user?: string; online: boolean; seen_at?: number; latest?: Metric }
export type Project = { id: number; host_id: string; name: string; source: 'docker' | 'systemd' | 'tmux'; monitored: boolean; native?: boolean; health_url?: string; expected?: string; check_ok?: boolean; checked_at?: number }
export type Runner = { repo: string; runner_id: number; name: string; status: string; busy: boolean; job?: string; seen_at: number }
export type Session = { host_id: string; name: string; pane_pid: number; cwd: string; agent?: string; state?: string; seen_at: number }
export type Run = { id: number; name: string; display_title: string; html_url: string; status: string; conclusion?: string }
export type Repository = { name: string; agent_switchable: boolean; ci_switchable: boolean; agent_runner: string; ci_runner: string; agent_known: boolean; ci_known: boolean; error?: string }
export type Dashboard = { hosts: Host[]; projects: Project[]; runners: Runner[]; sessions: Session[]; queued: Record<string, Run[]>; repositories: Repository[]; collector_errors: Record<string, string>; fleet_seen_at: number; smtp_provisioned: boolean }
