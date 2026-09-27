import type { Session } from './types'

const agentStateLabels: Record<string, string> = { working: 'Trabalhando', waiting: 'Esperando input', idle: 'Ociosa' }

// The agent state is a heuristic; sessions without one read as observed.
export function agentStateLabel(session: Session) {
  return (session.agent && session.state && agentStateLabels[session.state]) || 'Observada'
}
