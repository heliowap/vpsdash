import type { Dashboard, Metric } from './types'

export type SessionState = { authenticated: boolean; csrf: string }

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) { super(message); this.status = status }
}

async function request<T>(path: string, init: RequestInit = {}, csrf = ''): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(csrf ? { 'X-CSRF-Token': csrf } : {}), ...init.headers }
  })
  const value = await response.json().catch(() => ({}))
  if (!response.ok) throw new ApiError(response.status, value.error || `Erro HTTP ${response.status}`)
  return value as T
}

export const api = {
  session: () => request<SessionState>('/api/session'),
  login: (password: string) => request<SessionState>('/api/login', { method: 'POST', body: JSON.stringify({ password }) }),
  logout: (csrf: string) => request<SessionState>('/api/logout', { method: 'POST' }, csrf),
  dashboard: () => request<Dashboard>('/api/dashboard'),
  metrics: (id: string) => request<Metric[]>(`/api/hosts/${encodeURIComponent(id)}/metrics`),
  updateProject: (id: number, body: { monitored: boolean; health_url: string; expected: string[] }, csrf: string) =>
    request<{ saved: boolean }>(`/api/projects/${id}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  switchRunner: (repo: string, variable: 'AGENT_RUNNER' | 'CI_RUNNER', label: string, csrf: string) =>
    request<{ label: string }>(`/api/repos/${repo}/switch`, { method: 'POST', body: JSON.stringify({ variable, label }) }, csrf),
  bulkSwitch: (repositories: string[], variable: 'AGENT_RUNNER' | 'CI_RUNNER', label: string, csrf: string) =>
    request<{ results: Record<string, string> }>('/api/switches/bulk', { method: 'POST', body: JSON.stringify({ repositories, variable, label }) }, csrf),
  preset: (name: string, csrf: string) => request<{ results: Record<string, string> }>(`/api/presets/${name}`, { method: 'POST' }, csrf),
  rerun: (repo: string, id: number, csrf: string) => request<{ requested: boolean }>(`/api/repos/${repo}/rerun/${id}`, { method: 'POST' }, csrf)
}
