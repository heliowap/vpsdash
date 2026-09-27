import { useCallback, useEffect, useRef, useState } from 'react'
import type React from 'react'
import { AlertCircle, Check, Copy, KeyRound, LockKeyhole, Play, SquareTerminal, Terminal as TerminalIcon, X } from 'lucide-react'
import '@xterm/xterm/css/xterm.css'
import { api, ApiError } from './api'
import type { AuditEntry, Dashboard, InteractiveHost, InteractiveInfo, Session, Snippet, SnippetResult, TerminalRequest } from './types'
import { agentStateLabel } from './sessionState'

function clock(timestamp: number) {
  return new Date(timestamp * 1000).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
}

function auditTime(timestamp: number) {
  return new Date(timestamp * 1000).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
}

const auditActions: Record<AuditEntry['action'], string> = {
  step_up: 'Confirmação de senha',
  terminal: 'Terminal',
  attach_ro: 'Attach somente leitura',
  attach_rw: 'Attach com controle',
  snippet: 'Comando'
}
const auditOutcomes: Record<AuditEntry['outcome'], string> = { ok: 'Concluído', denied: 'Negado', failed: 'Falhou', closed: 'Encerrado' }

export function AuditLedger({ refreshKey }: { refreshKey: number }) {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let current = true
    api.audit().then(value => { if (current) { setEntries(value); setError('') } }).catch(err => { if (current) setError(err instanceof Error ? err.message : 'Registro indisponível.') })
    return () => { current = false }
  }, [refreshKey])
  return <section className="ledger-section" aria-labelledby="audit-heading">
    <div className="section-heading"><div><h2 id="audit-heading">Registro de acesso</h2><p>Aberturas de terminal, attach, comandos e confirmações de senha. Teclas e saídas não são gravadas.</p></div>{entries && <span className="section-count">{entries.length} recentes</span>}</div>
    {error ? <div className="empty-line">{error}</div> : !entries ? <div className="empty-line">Lendo o registro…</div> : entries.length === 0 ? <div className="empty-line">Nenhum acesso interativo registrado.</div> :
      <div className="ruled-list">{entries.map(entry => <div className="audit-row" key={entry.id}>
        <time>{auditTime(entry.ts)}</time>
        <div><strong>{auditActions[entry.action]}{entry.target ? ` · ${entry.target}` : ''}</strong><small>{entry.host_id || 'painel'} · origem {entry.client_ip || 'desconhecida'}</small></div>
        <span className={`state-stamp ${entry.outcome === 'denied' || entry.outcome === 'failed' ? 'stamp-bad' : entry.outcome === 'closed' ? 'stamp-unknown' : ''}`}>{auditOutcomes[entry.outcome]}</span>
      </div>)}</div>}
  </section>
}

function StepUpSheet({ csrf, reason, onConfirmed, onCancel }: { csrf: string; reason: string; onConfirmed: (until: number) => void; onCancel: () => void }) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { const result = await api.stepUp(password, csrf); setPassword(''); onConfirmed(result.step_up_until) }
    catch (err) { setError(err instanceof Error ? err.message : 'Senha não confirmada.') }
    finally { setBusy(false) }
  }
  return <div className="sheet-backdrop" role="presentation">
    <section className="bounded-sheet step-up-sheet" role="dialog" aria-modal="true" aria-labelledby="step-up-title">
      <div className="sheet-seal"><KeyRound size={20} /></div>
      <h2 id="step-up-title">Confirme sua senha</h2>
      <p>{reason} A confirmação vale por 10 minutos nesta sessão.</p>
      <form onSubmit={submit}>
        <label htmlFor="step-up-password">Confirme a senha do painel</label>
        <input id="step-up-password" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required autoFocus />
        {error && <p className="form-error" role="alert"><AlertCircle size={16} /> {error}</p>}
        <div className="preview-actions"><button className="button button-primary" type="submit" disabled={busy}>{busy ? 'Confirmando…' : 'Confirmar senha'}</button><button className="button button-plain" type="button" onClick={onCancel}>Cancelar</button></div>
      </form>
    </section>
  </div>
}

type OpenTerminal = { ticket: string; title: string; mode: 'terminal' | 'read' | 'write'; localCommand: string }

function TerminalSheet({ session, onClose }: { session: OpenTerminal; onClose: () => void }) {
  const container = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState('Conectando…')
  const [ended, setEnded] = useState(false)
  const [copied, setCopied] = useState(false)
  const close = useRef<() => void>(() => undefined)
  useEffect(() => {
    let disposed = false
    let cleanup: () => void = () => {}
    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')])
      if (disposed || !container.current) return
      const term = new Terminal({
        cursorBlink: session.mode !== 'read', disableStdin: session.mode === 'read', convertEol: false, scrollback: 5000,
        fontFamily: '"IBM Plex Mono", monospace', fontSize: window.innerWidth <= 720 ? 12 : 14,
        theme: { background: '#1f2625', foreground: '#eeede8', cursor: '#8fc5c0', selectionBackground: '#3a777c' }
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      term.open(container.current)
      fit.fit()
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(`${protocol}//${window.location.host}/api/terminal/ws?ticket=${encodeURIComponent(session.ticket)}&cols=${term.cols}&rows=${term.rows}`)
      socket.binaryType = 'arraybuffer'
      const encoder = new TextEncoder()
      let finished = false
      socket.onmessage = event => {
        if (typeof event.data === 'string') {
          try {
            const message = JSON.parse(event.data) as { type: string; reason?: string }
            if (message.type === 'ready') setStatus(session.mode === 'read' ? 'Conectado · somente leitura' : 'Conectado')
            if (message.type === 'exit') { finished = true; setEnded(true); setStatus(message.reason || 'Sessão encerrada.') }
          } catch { /* ignore malformed control frames */ }
          return
        }
        term.write(new Uint8Array(event.data as ArrayBuffer))
      }
      socket.onclose = () => { setEnded(true); if (!finished) setStatus('Conexão encerrada. Abra o terminal de novo para continuar.') }
      socket.onerror = () => { if (!finished) setStatus('Não foi possível conectar ao terminal.') }
      const input = term.onData(data => { if (socket.readyState === WebSocket.OPEN) socket.send(encoder.encode(data)) })
      const resize = term.onResize(({ cols, rows }) => { if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'resize', cols, rows })) })
      const observer = new ResizeObserver(() => { try { fit.fit() } catch { /* hidden */ } })
      observer.observe(container.current)
      term.focus()
      close.current = () => socket.close(1000)
      cleanup = () => { observer.disconnect(); input.dispose(); resize.dispose(); socket.close(1000); term.dispose() }
    })()
    return () => { disposed = true; cleanup() }
  }, [session])
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape' && ended) onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [ended, onClose])
  async function copy() {
    try { await navigator.clipboard.writeText(session.localCommand); setCopied(true) } catch { setCopied(false) }
  }
  const stamp = session.mode === 'read' ? 'Somente leitura' : session.mode === 'write' ? 'Com controle' : 'Shell'
  return <div className="sheet-backdrop terminal-backdrop" role="presentation">
    <section className="terminal-sheet" role="dialog" aria-modal="true" aria-labelledby="terminal-title">
      <header>
        <div><h2 id="terminal-title">{session.title}</h2><p role="status">{status}</p></div>
        <span className={`state-stamp ${session.mode === 'write' ? 'stamp-bad' : ''}`}>{stamp}</span>
        <button className="icon-button" type="button" aria-label="Encerrar terminal" title="Encerrar terminal" onClick={() => { close.current(); onClose() }}><X size={19} /></button>
      </header>
      <div className="terminal-body" ref={container} />
      {session.localCommand && <footer><code>{session.localCommand}</code><button className="button button-small" type="button" onClick={() => void copy()}>{copied ? <Check size={15} /> : <Copy size={15} />}{copied ? 'Copiado' : 'Copiar comando local'}</button></footer>}
    </section>
  </div>
}

function LocalCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)
  return <div className="local-command"><code>{command}</code><button className="button button-small button-plain" type="button" onClick={() => { navigator.clipboard.writeText(command).then(() => setCopied(true), () => setCopied(false)) }}>{copied ? <Check size={15} /> : <Copy size={15} />}{copied ? 'Copiado' : 'Copiar'}</button></div>
}

type Pending =
  | { kind: 'terminal'; request: TerminalRequest; title: string; mode: OpenTerminal['mode']; localCommand: string }
  | { kind: 'snippet'; host: string; snippet: Snippet }

function SnippetRow({ host, snippet, busy, result, onRun }: { host: string; snippet: Snippet; busy: boolean; result?: SnippetResult | string; onRun: () => void }) {
  const [confirm, setConfirm] = useState(false)
  return <div className="snippet-row">
    <div className="snippet-head"><div><strong>{snippet.name}</strong><code>{snippet.argv.join(' ')}</code></div><button className="button button-small" type="button" disabled={busy} onClick={() => setConfirm(true)}><Play size={15} />Executar</button></div>
    {confirm && <div className="operation-preview">
      <strong>Executar “{snippet.name}” em {host}?</strong>
      <ul><li>Argumentos fixos do inventário: <code>{snippet.argv.join(' ')}</code></li><li>Tempo máximo {snippet.timeout_seconds} s · saída limitada a 64 KiB</li></ul>
      <div className="preview-actions"><button className="button button-primary button-small" type="button" disabled={busy} onClick={() => { setConfirm(false); onRun() }}>Confirmar execução</button><button className="button button-plain button-small" type="button" onClick={() => setConfirm(false)}>Cancelar</button></div>
    </div>}
    {busy && <p className="inline-feedback" role="status">Executando…</p>}
    {typeof result === 'string' && <p className="inline-feedback is-error" role="alert">{result}</p>}
    {result && typeof result !== 'string' && <div className="snippet-result">
      <div><span className={`state-stamp ${result.result.exit_code === 0 && !result.result.timed_out ? '' : 'stamp-bad'}`}>{result.result.timed_out ? 'Tempo esgotado' : `Código de saída ${result.result.exit_code}`}</span><small>{(result.duration_ms / 1000).toFixed(1)} s{result.result.truncated ? ' · saída cortada em 64 KiB' : ''}</small></div>
      <pre>{result.result.output || '(sem saída)'}</pre>
    </div>}
  </div>
}

function HostAccess({ host, busy, results, onTerminal, onSnippet }: { host: InteractiveHost; busy: string; results: Record<string, SnippetResult | string>; onTerminal: () => void; onSnippet: (snippet: Snippet) => void }) {
  const [showLocal, setShowLocal] = useState(false)
  return <article className="access-row">
    <div className="access-head">
      <SquareTerminal size={19} />
      <div><strong>{host.id}</strong><small>{host.terminal ? `Chave interativa configurada · ${host.snippets.length} ${host.snippets.length === 1 ? 'comando' : 'comandos'}` : 'Sem chave interativa no inventário'}</small></div>
      <div className="row-actions">
        {host.terminal ? <button className="button button-primary button-small" type="button" onClick={onTerminal}><TerminalIcon size={15} />Abrir terminal</button> : <span className="state-stamp stamp-unknown">Indisponível</span>}
        {host.local_command && <button className="button button-small button-plain" type="button" aria-expanded={showLocal} onClick={() => setShowLocal(!showLocal)}>Terminal local</button>}
      </div>
    </div>
    {showLocal && <LocalCommand command={host.local_command} />}
    {host.snippets.length > 0 && <div className="snippet-list">{host.snippets.map(snippet => <SnippetRow key={snippet.name} host={host.id} snippet={snippet} busy={busy === `${host.id}/${snippet.name}`} result={results[`${host.id}/${snippet.name}`]} onRun={() => onSnippet(snippet)} />)}</div>}
  </article>
}

function SessionActions({ session, host, onOpen }: { session: Session; host?: InteractiveHost; onOpen: (write: boolean) => void }) {
  const [confirmWrite, setConfirmWrite] = useState(false)
  const [showLocal, setShowLocal] = useState(false)
  const localCommand = host?.attach_commands[session.name]
  return <>
    <div className="row-actions">
      {host?.terminal ? <><button className="button button-small button-primary" type="button" onClick={() => onOpen(false)}>Ver sessão</button><button className="button button-small" type="button" onClick={() => setConfirmWrite(true)}>Assumir controle</button></> : <span className="muted-text">Sem chave interativa</span>}
      {localCommand && <button className="button button-small button-plain" type="button" aria-expanded={showLocal} onClick={() => setShowLocal(!showLocal)}>Terminal local</button>}
    </div>
    {confirmWrite && <div className="inline-confirm row-wide"><span>Assumir o controle de <b>{session.name}</b> em {session.host_id}? O que você digitar chega ao agente desta sessão.</span><div><button className="button button-small button-primary" type="button" onClick={() => { setConfirmWrite(false); onOpen(true) }}>Assumir controle</button><button className="button button-small button-plain" type="button" onClick={() => setConfirmWrite(false)}>Cancelar</button></div></div>}
    {showLocal && localCommand && <div className="row-wide"><LocalCommand command={localCommand} /></div>}
  </>
}

export function InteractiveSessions({ data, csrf, uncertain, age }: { data: Dashboard; csrf: string; uncertain: (session: Session) => boolean; age: (ts?: number) => string }) {
  const [info, setInfo] = useState<InteractiveInfo | null>(null)
  const [infoError, setInfoError] = useState('')
  const [stepUpUntil, setStepUpUntil] = useState(0)
  const [pending, setPending] = useState<Pending | null>(null)
  const [stepUpReason, setStepUpReason] = useState('')
  const [terminal, setTerminal] = useState<OpenTerminal | null>(null)
  const [busy, setBusy] = useState('')
  const [results, setResults] = useState<Record<string, SnippetResult | string>>({})
  const [notice, setNotice] = useState('')
  const [auditKey, setAuditKey] = useState(0)
  const loadInfo = useCallback(() => {
    api.interactive().then(value => { setInfo(value); setStepUpUntil(value.step_up_until); setInfoError('') }).catch(err => setInfoError(err instanceof Error ? err.message : 'Acesso interativo indisponível.'))
  }, [])
  useEffect(() => { loadInfo() }, [loadInfo, data])
  const [, tick] = useState(0)
  useEffect(() => { const timer = window.setInterval(() => tick(value => value + 1), 15_000); return () => window.clearInterval(timer) }, [])
  const steppedUp = stepUpUntil > Date.now() / 1000

  const execute = useCallback(async (action: Pending) => {
    setNotice('')
    try {
      if (action.kind === 'terminal') {
        const result = await api.terminalTicket(action.request, csrf)
        setTerminal({ ticket: result.ticket, title: action.title, mode: action.mode, localCommand: result.local_command })
      } else {
        const key = `${action.host}/${action.snippet.name}`
        setBusy(key)
        try { const result = await api.runSnippet(action.host, action.snippet.name, csrf); setResults(current => ({ ...current, [key]: result })) }
        finally { setBusy('') }
      }
      setAuditKey(value => value + 1)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'step_up_required') { setStepUpUntil(0); setPending(action); setStepUpReason(action.kind === 'snippet' ? 'Comandos exigem a senha de novo.' : 'O terminal exige a senha de novo.'); return }
      const message = err instanceof Error ? err.message : 'Ação não concluída.'
      if (action.kind === 'snippet') setResults(current => ({ ...current, [`${action.host}/${action.snippet.name}`]: message }))
      else setNotice(message)
      setAuditKey(value => value + 1)
    }
  }, [csrf])

  function request(action: Pending) {
    if (!steppedUp) { setPending(action); setStepUpReason(action.kind === 'snippet' ? 'Comandos exigem a senha de novo.' : 'O terminal exige a senha de novo.'); return }
    void execute(action)
  }

  const hostsByID = Object.fromEntries((info?.hosts || []).map(host => [host.id, host]))
  return <>
    <section className="ledger-section" aria-labelledby="access-heading">
      <div className="section-heading"><div><h2 id="access-heading">Acesso aos hosts</h2><p>Terminal e comandos usam a chave interativa de cada host, separada da chave de leitura.</p></div><span className={`state-stamp ${steppedUp ? '' : 'stamp-unknown'}`}>{steppedUp ? `Senha confirmada até ${clock(stepUpUntil)}` : 'Senha a confirmar'}</span></div>
      {notice && <p className="notice notice-error" role="alert"><AlertCircle size={18} />{notice}</p>}
      {infoError ? <div className="empty-line">{infoError}</div> : !info ? <div className="empty-line">Lendo a configuração interativa…</div> : info.hosts.length === 0 ? <div className="empty-line">Nenhuma VPS no inventário.</div> :
        <div className="ruled-list">{info.hosts.map(host => <HostAccess key={host.id} host={host} busy={busy} results={results}
          onTerminal={() => request({ kind: 'terminal', request: { host: host.id, kind: 'terminal' }, title: `Terminal · ${host.id}`, mode: 'terminal', localCommand: host.local_command })}
          onSnippet={snippet => request({ kind: 'snippet', host: host.id, snippet })} />)}</div>}
      {info && <p className="muted-text access-foot">Até {info.max_sessions} sessões interativas ao mesmo tempo · o terminal fecha após {info.idle_minutes} min sem digitação.</p>}
    </section>
    <section className="ledger-section" aria-labelledby="sessions-heading">
      <div className="section-heading"><div><h2 id="sessions-heading">Sessões tmux</h2><p>“Ver sessão” abre o attach somente leitura. Assumir o controle pede confirmação.</p></div><span className="section-count">{data.sessions.length}</span></div>
      {data.sessions.length ? <div className="ruled-list">{data.sessions.map(session => {
        const unsure = uncertain(session)
        return <div className="session-row has-actions" key={`${session.host_id}-${session.name}`}>
          <TerminalIcon size={19} />
          <div><strong>{session.name}</strong><small>{session.host_id} · {session.cwd || 'caminho indisponível'} · {session.agent || 'shell'} · {unsure ? 'última leitura ' : ''}{age(session.seen_at)}</small></div>
          <span className={`state-stamp ${unsure ? 'stamp-unknown' : ''}`}>{unsure ? 'Não confirmado' : agentStateLabel(session)}</span>
          <SessionActions session={session} host={hostsByID[session.host_id]} onOpen={write => request({ kind: 'terminal', request: { host: session.host_id, kind: 'attach', session: session.name, write, confirm_write: write }, title: `tmux ${session.name} · ${session.host_id}`, mode: write ? 'write' : 'read', localCommand: hostsByID[session.host_id]?.attach_commands[session.name] || '' })} />
        </div>
      })}</div> : <div className="empty-line">Nenhuma sessão tmux observada. As sessões aparecem quando os hosts forem alcançados.</div>}
    </section>
    <AuditLedger refreshKey={auditKey} />
    {pending && <StepUpSheet csrf={csrf} reason={stepUpReason} onCancel={() => setPending(null)} onConfirmed={until => { setStepUpUntil(until); const action = pending; setPending(null); void execute(action) }} />}
    {terminal && <TerminalSheet session={terminal} onClose={() => { setTerminal(null); setAuditKey(value => value + 1); loadInfo() }} />}
  </>
}

export function PublicRouteNote() {
  return <div className="route-note" role="note">
    <LockKeyhole size={19} />
    <div><strong>Terminal, attach e comandos só pela tailnet</strong><p>Você entrou pela rota pública. Aqui o painel apenas lê o estado das sessões. Abra o endereço privado do Tailscale para usar o terminal, o attach tmux e os comandos do host.</p></div>
  </div>
}

