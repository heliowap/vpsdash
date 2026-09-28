import type { AuditEntry, Dashboard, FileContent, FileListing, IncidentHistory, InteractiveInfo, JobList, JobLog, Metric, MinutesReport, Reply, SnippetResult, TerminalRequest, UnitOp } from './types'

export type SessionState = { authenticated: boolean; csrf: string; private?: boolean }

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, message: string, code = '') { super(message); this.status = status; this.code = code }
}

async function request<T>(path: string, init: RequestInit = {}, csrf = ''): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(csrf ? { 'X-CSRF-Token': csrf } : {}), ...init.headers }
  })
  const value = await response.json().catch(() => ({}))
  if (!response.ok) throw new ApiError(response.status, value.error || `Erro HTTP ${response.status}`, value.code || '')
  return value as T
}

export const api = {
  session: () => request<SessionState>('/api/session'),
  login: (password: string) => request<SessionState>('/api/login', { method: 'POST', body: JSON.stringify({ password }) }),
  logout: (csrf: string) => request<SessionState>('/api/logout', { method: 'POST' }, csrf),
  dashboard: () => request<Dashboard>('/api/dashboard'),
  minutes: () => request<MinutesReport>('/api/minutes'),
  metrics: (id: string) => request<Metric[]>(`/api/hosts/${encodeURIComponent(id)}/metrics`),
  projectIncidents: (id: number) => request<IncidentHistory>(`/api/projects/${id}/incidents`),
  listFiles: (host: string, path: string) =>
    request<FileListing>(`/api/hosts/${encodeURIComponent(host)}/files?${new URLSearchParams({ path })}`),
  readFile: (host: string, path: string, offset: number) =>
    request<FileContent>(`/api/hosts/${encodeURIComponent(host)}/file?${new URLSearchParams({ path, offset: String(offset) })}`),
  updateProject: (id: number, body: { monitored: boolean; health_url: string; expected: string[] }, csrf: string) =>
    request<{ saved: boolean }>(`/api/projects/${id}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  switchRunner: (repo: string, variable: 'AGENT_RUNNER' | 'CI_RUNNER', label: string, csrf: string) =>
    request<{ label: string }>(`/api/repos/${repo.split('/').map(encodeURIComponent).join('/')}/switch`, { method: 'POST', body: JSON.stringify({ variable, label }) }, csrf),
  bulkSwitch: (repositories: string[], variable: 'AGENT_RUNNER' | 'CI_RUNNER', label: string, csrf: string) =>
    request<{ results: Record<string, string> }>('/api/switches/bulk', { method: 'POST', body: JSON.stringify({ repositories, variable, label }) }, csrf),
  jobs: () => request<JobList>('/api/jobs'),
  jobLog: (repo: string, id: number, signal?: AbortSignal) =>
    request<JobLog>(`/api/repos/${repo.split('/').map(encodeURIComponent).join('/')}/jobs/${id}/log`, { signal }),
  preset: (name: string, csrf: string) => request<{ results: Record<string, string> }>(`/api/presets/${name}`, { method: 'POST' }, csrf),
  runnerUnit: (host: string, unit: string, action: 'restart' | 'drain' | 'drain/cancel', csrf: string) =>
    request<UnitOp>(`/api/runner-units/${encodeURIComponent(host)}/${encodeURIComponent(unit)}/${action}`, { method: 'POST' }, csrf),
  pushStatus: () => request<{ configured: boolean; public_key: string }>('/api/push'),
  pushSubscribe: (subscription: PushSubscriptionJSON, csrf: string) =>
    request<{ subscribed: boolean }>('/api/push/subscriptions', { method: 'POST', body: JSON.stringify(subscription) }, csrf),
  pushUnsubscribe: (endpoint: string, csrf: string) =>
    request<{ subscribed: boolean }>('/api/push/subscriptions', { method: 'DELETE', body: JSON.stringify({ endpoint }) }, csrf),
  pushTest: (endpoint: string, csrf: string) =>
    request<{ sent: boolean }>('/api/push/test', { method: 'POST', body: JSON.stringify({ endpoint }) }, csrf),
  audit: () => request<AuditEntry[]>('/api/audit'),
  interactive: () => request<InteractiveInfo>('/api/interactive'),
  stepUp: (password: string, csrf: string) => request<{ step_up_until: number }>('/api/step-up', { method: 'POST', body: JSON.stringify({ password }) }, csrf),
  terminalTicket: (body: TerminalRequest, csrf: string) => request<{ ticket: string; local_command: string }>('/api/terminal/tickets', { method: 'POST', body: JSON.stringify(body) }, csrf),
  runSnippet: (host: string, name: string, csrf: string) =>
    request<SnippetResult>(`/api/hosts/${encodeURIComponent(host)}/snippets/run`, { method: 'POST', body: JSON.stringify({ name }) }, csrf),
  sendKeys: (host: string, session: string, reply: Reply, csrf: string) =>
    request<{ sent: boolean; state: string }>(`/api/hosts/${encodeURIComponent(host)}/sessions/send-keys`, { method: 'POST', body: JSON.stringify({ session, ...reply, confirm: true }) }, csrf)
}
