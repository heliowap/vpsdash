import type { Session } from './types'

const agentStateLabels: Record<string, string> = { working: 'Trabalhando', waiting: 'Esperando input', idle: 'Ociosa' }

// The agent state is a heuristic; sessions without one read as observed.
export function agentStateLabel(session: Session) {
  return (session.agent && session.state && agentStateLabels[session.state]) || 'Observada'
}

// Both session views say the same thing when the list is empty: an empty
// list from a failed or stale collection is not proof that no session runs.
export function emptySessionsText(collectionUncertain: boolean) {
  return collectionUncertain
    ? 'Coleta de sessões indisponível. Ainda não há uma leitura confirmada.'
    : 'Nenhuma sessão tmux observada. As sessões aparecem quando os hosts forem alcançados.'
}
