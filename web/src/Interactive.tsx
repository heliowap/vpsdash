import { useCallback, useEffect, useRef, useState } from 'react'
import type React from 'react'
import { AlertCircle, Check, Copy, CornerDownLeft, KeyRound, LockKeyhole, Play, SquareTerminal, Terminal as TerminalIcon, X } from 'lucide-react'
import '@xterm/xterm/css/xterm.css'
import { api, ApiError } from './api'
import type { AuditEntry, Dashboard, InteractiveHost, InteractiveInfo, Reply, ReplyKey, Session, SessionFocus, Snippet, SnippetResult, TerminalRequest } from './types'
import { agentStateLabel, emptySessionsText } from './sessionState'

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
  snippet: 'Comando',
  send_keys: 'Resposta ao agente'
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
    <div className="section-heading"><div><h2 id="audit-heading">Registro de acesso</h2><p>Aberturas de terminal, attach, respostas a agentes, comandos e confirmações de senha. Teclas, textos e saídas não são gravados.</p></div>{entries && <span className="section-count">{entries.length} recentes</span>}</div>
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
  | { kind: 'reply'; host: string; session: string; reply: Reply }

type ReplyResult = { status: 'sent' | 'refused' | 'failed'; message: string }

const stepUpReasons: Record<Pending['kind'], string> = {
  terminal: 'O terminal exige a senha de novo.',
  snippet: 'Comandos exigem a senha de novo.',
  reply: 'Responder a um agente exige a senha de novo.'
}

// Quick replies mirror the server allowlist. C-c is not offered: it
// interrupts the agent instead of answering it.
const quickKeys: { key: ReplyKey; label: string; name: string }[] = [
  { key: 'y', label: 'y', name: 'y' }, { key: 'n', label: 'n', name: 'n' },
  { key: '1', label: '1', name: '1' }, { key: '2', label: '2', name: '2' }, { key: '3', label: '3', name: '3' },
  { key: 'Enter', label: 'Enter', name: 'tecla Enter' }, { key: 'Escape', label: 'Esc', name: 'tecla Esc' },
  { key: 'Up', label: '↑', name: 'seta para cima' }, { key: 'Down', label: '↓', name: 'seta para baixo' }, { key: 'Tab', label: 'Tab', name: 'tecla Tab' }
]
const maxReplyText = 200
const forbiddenText = /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/

function replyTextError(text: string) {
  if (!text) return ''
  if (forbiddenText.test(text)) return 'Use uma linha, sem tabulação ou caracteres de controle.'
  if ([...text].length > maxReplyText) return `Até ${maxReplyText} caracteres.`
  return ''
}

function ReplyKeys({ reply }: { reply: Reply }) {
  if ('key' in reply) {
    const quick = quickKeys.find(item => item.key === reply.key)
    return <><code>{reply.key.length === 1 ? reply.key : quick?.label}</code>{reply.key.length > 1 ? ` (${quick?.name})` : ''}</>
  }
  return <><code>{reply.text}</code>{reply.enter ? ' e depois Enter' : ' sem Enter'}</>
}

function ReplyPanel({ session, uncertain, busy, result, onSend, onClose }: { session: Session; uncertain: boolean; busy: boolean; result?: ReplyResult; onSend: (reply: Reply) => void; onClose: () => void }) {
  const [text, setText] = useState('')
  const [enter, setEnter] = useState(false)
  const [draft, setDraft] = useState<Reply | null>(null)
  // Clear the typed text only once the host confirmed it; a failed send keeps it.
  const sentText = useRef(false)
  useEffect(() => {
    if (!result) return
    if (result.status === 'sent' && sentText.current) setText('')
    sentText.current = false
  }, [result])
  const textError = replyTextError(text)
  const waiting = session.state === 'waiting' && !uncertain
  const fieldID = `reply-${session.host_id}-${session.name}`.replace(/[^A-Za-z0-9_-]/g, '_')
  return <div className="reply-panel row-wide">
    <div className="reply-head"><strong>Responder ao prompt</strong><button className="icon-button" type="button" aria-label="Fechar resposta" title="Fechar resposta" onClick={onClose}><X size={17} /></button></div>
    <p className="muted-text">As teclas vão para o pane observado desta sessão por <code>tmux send-keys</code>. Você confirma antes do envio.</p>
    <div className="quick-keys" role="group" aria-label="Respostas rápidas">
      {quickKeys.map(item => <button key={item.key} className="button button-small key-button" type="button" disabled={busy} aria-label={`Enviar ${item.name}`} onClick={() => setDraft({ key: item.key })}>{item.label}</button>)}
    </div>
    <form className="reply-text" onSubmit={event => { event.preventDefault(); if (text && !textError) setDraft({ text, enter }) }}>
      <label htmlFor={fieldID}>Texto, uma linha</label>
      <div className="reply-text-row">
        <input id={fieldID} type="text" value={text} maxLength={maxReplyText * 2} autoComplete="off" spellCheck={false} onChange={event => setText(event.target.value)} aria-invalid={Boolean(textError)} aria-describedby={`${fieldID}-help`} />
        <button className="button button-small" type="submit" disabled={busy || !text || Boolean(textError)}>Revisar envio</button>
      </div>
      <label className="checkbox-row"><input type="checkbox" checked={enter} onChange={event => setEnter(event.target.checked)} />Enviar Enter depois do texto</label>
      <small id={`${fieldID}-help`} className={textError ? 'form-error' : 'muted-text'}>{textError || `${[...text].length}/${maxReplyText} caracteres`}</small>
    </form>
    {draft && <div className="operation-preview reply-confirm" role="group" aria-labelledby={`${fieldID}-confirm`}>
      <strong id={`${fieldID}-confirm`}>Enviar estas teclas para {session.name}?</strong>
      <ul>
        <li>Sessão: <code>{session.name}</code></li>
        <li>Host: <code>{session.host_id}</code></li>
        <li>Teclas: <ReplyKeys reply={draft} /></li>
      </ul>
      {!waiting && <p className="reply-warning"><AlertCircle size={16} />{uncertain ? 'A última leitura desta sessão não está confirmada.' : `A coleta não marca esta sessão como esperando input (${agentStateLabel(session)}).`} Confira a tela antes de enviar.</p>}
      <div className="preview-actions"><button className="button button-primary button-small" type="button" disabled={busy} onClick={() => { const reply = draft; setDraft(null); sentText.current = 'text' in reply; onSend(reply) }}><CornerDownLeft size={15} />Enviar teclas</button><button className="button button-plain button-small" type="button" onClick={() => setDraft(null)}>Cancelar</button></div>
    </div>}
    {busy && <p className="inline-feedback" role="status">Enviando…</p>}
    {!busy && result && <p className="reply-result" role={result.status === 'sent' ? 'status' : 'alert'}><span className={`state-stamp ${result.status === 'sent' ? '' : 'stamp-bad'}`}>{result.status === 'sent' ? 'Enviado' : result.status === 'refused' ? 'Recusado' : 'Falhou'}</span><span>{result.message}</span></p>}
  </div>
}

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
      <div><strong>{host.id}</strong><small>{host.terminal ? `Chave interativa configurada · ${host.snippets.length} ${host.snippets.length === 1 ? 'comando' : 'comandos'}` : host.key_missing ? 'Chave interativa ausente no host do painel · execute scripts/setup-interactive-key.sh' : 'Sem chave interativa no inventário'}</small></div>
      <div className="row-actions">
        {host.terminal ? <button className="button button-primary button-small" type="button" onClick={onTerminal}><TerminalIcon size={15} />Abrir terminal</button> : <span className="state-stamp stamp-unknown">Indisponível</span>}
        {host.local_command && <button className="button button-small button-plain" type="button" aria-expanded={showLocal} onClick={() => setShowLocal(!showLocal)}>Terminal local</button>}
      </div>
    </div>
    {showLocal && <LocalCommand command={host.local_command} />}
    {host.snippets.length > 0 && <div className="snippet-list">{host.snippets.map(snippet => <SnippetRow key={snippet.name} host={host.id} snippet={snippet} busy={busy === `${host.id}/${snippet.name}`} result={results[`${host.id}/${snippet.name}`]} onRun={() => onSnippet(snippet)} />)}</div>}
  </article>
}

function SessionActions({ session, host, focused, uncertain, replyBusy, replyResult, onOpen, onReply }: { session: Session; host?: InteractiveHost; focused: boolean; uncertain: boolean; replyBusy: boolean; replyResult?: ReplyResult; onOpen: (write: boolean) => void; onReply: (reply: Reply) => void }) {
  const [confirmWrite, setConfirmWrite] = useState(false)
  const [showLocal, setShowLocal] = useState(false)
  const [replying, setReplying] = useState(focused)
  useEffect(() => { if (focused) setReplying(true) }, [focused])
  const localCommand = host?.attach_commands[session.name]
  return <>
    <div className="row-actions">
      {host?.terminal ? <><button className="button button-small button-primary" type="button" onClick={() => onOpen(false)}>Ver sessão</button><button className="button button-small" type="button" aria-expanded={replying} onClick={() => setReplying(!replying)}>Responder</button><button className="button button-small" type="button" onClick={() => setConfirmWrite(true)}>Assumir controle</button></> : <span className="muted-text">Sem chave interativa</span>}
      {localCommand && <button className="button button-small button-plain" type="button" aria-expanded={showLocal} onClick={() => setShowLocal(!showLocal)}>Terminal local</button>}
    </div>
    {confirmWrite && <div className="inline-confirm row-wide"><span>Assumir o controle de <b>{session.name}</b> em {session.host_id}? O que você digitar chega ao agente desta sessão.</span><div><button className="button button-small button-primary" type="button" onClick={() => { setConfirmWrite(false); onOpen(true) }}>Assumir controle</button><button className="button button-small button-plain" type="button" onClick={() => setConfirmWrite(false)}>Cancelar</button></div></div>}
    {showLocal && localCommand && <div className="row-wide"><LocalCommand command={localCommand} /></div>}
    {replying && host?.terminal && <ReplyPanel session={session} uncertain={uncertain} busy={replyBusy} result={replyResult} onSend={onReply} onClose={() => setReplying(false)} />}
  </>
}

export function InteractiveSessions({ data, csrf, uncertain, collectionUncertain, age, focus }: { data: Dashboard; csrf: string; uncertain: (session: Session) => boolean; collectionUncertain: boolean; age: (ts?: number) => string; focus: SessionFocus | null }) {
  const [info, setInfo] = useState<InteractiveInfo | null>(null)
  const [infoError, setInfoError] = useState('')
  const [stepUpUntil, setStepUpUntil] = useState(0)
  const [pending, setPending] = useState<Pending | null>(null)
  const [stepUpReason, setStepUpReason] = useState('')
  const [terminal, setTerminal] = useState<OpenTerminal | null>(null)
  const [busy, setBusy] = useState('')
  const [results, setResults] = useState<Record<string, SnippetResult | string>>({})
  const [replies, setReplies] = useState<Record<string, ReplyResult>>({})
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
      } else if (action.kind === 'reply') {
        const key = `reply:${action.host}/${action.session}`
        setBusy(key)
        try {
          await api.sendKeys(action.host, action.session, action.reply, csrf)
          setReplies(current => ({ ...current, [key]: { status: 'sent', message: `Teclas entregues às ${clock(Date.now() / 1000)}. Veja a sessão para conferir a resposta do agente.` } }))
        } finally { setBusy('') }
      } else {
        const key = `${action.host}/${action.snippet.name}`
        setBusy(key)
        try { const result = await api.runSnippet(action.host, action.snippet.name, csrf); setResults(current => ({ ...current, [key]: result })) }
        finally { setBusy('') }
      }
      setAuditKey(value => value + 1)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'step_up_required') { setStepUpUntil(0); setPending(action); setStepUpReason(stepUpReasons[action.kind]); return }
      const message = err instanceof Error ? err.message : 'Ação não concluída.'
      if (action.kind === 'reply') {
        // A refusal is the panel saying no (validation, session, limits);
        // a failure is the host or the network not confirming the send.
        const refused = err instanceof ApiError && err.status >= 400 && err.status < 500
        setReplies(current => ({ ...current, [`reply:${action.host}/${action.session}`]: { status: refused ? 'refused' : 'failed', message: err instanceof ApiError ? message : 'Sem resposta do painel. O envio não foi confirmado.' } }))
      } else if (action.kind === 'snippet') setResults(current => ({ ...current, [`${action.host}/${action.snippet.name}`]: message }))
      else setNotice(message)
      setAuditKey(value => value + 1)
    }
  }, [csrf])

  function request(action: Pending) {
    if (!steppedUp) { setPending(action); setStepUpReason(stepUpReasons[action.kind]); return }
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
      <div className="section-heading"><div><h2 id="sessions-heading">Sessões tmux</h2><p>“Ver sessão” abre o attach somente leitura. “Responder” envia uma tecla ou uma linha ao agente; responder e assumir o controle pedem confirmação.</p></div><span className="section-count">{data.sessions.length}</span></div>
      {data.sessions.length ? <div className="ruled-list">{data.sessions.map(session => {
        const unsure = uncertain(session)
        const focused = Boolean(focus && focus.host === session.host_id && focus.session === session.name)
        const replyKey = `reply:${session.host_id}/${session.name}`
        return <div className={`session-row has-actions ${focused ? 'is-focused' : ''}`} data-focused={focused || undefined} key={`${session.host_id}-${session.name}`}>
          <TerminalIcon size={19} />
          <div><strong>{session.name}</strong><small>{session.host_id} · {session.cwd || 'caminho indisponível'} · {session.agent || 'shell'} · {unsure ? 'última leitura ' : ''}{age(session.seen_at)}</small></div>
          <span className={`state-stamp ${unsure ? 'stamp-unknown' : ''}`}>{unsure ? 'Não confirmado' : agentStateLabel(session)}</span>
          <SessionActions session={session} host={hostsByID[session.host_id]} focused={focused} uncertain={unsure} replyBusy={busy === replyKey} replyResult={replies[replyKey]}
            onReply={reply => { setReplies(current => { const next = { ...current }; delete next[replyKey]; return next }); request({ kind: 'reply', host: session.host_id, session: session.name, reply }) }}
            onOpen={write => request({ kind: 'terminal', request: { host: session.host_id, kind: 'attach', session: session.name, write, confirm_write: write }, title: `tmux ${session.name} · ${session.host_id}`, mode: write ? 'write' : 'read', localCommand: hostsByID[session.host_id]?.attach_commands[session.name] || '' })} />
        </div>
      })}</div> : <div className="empty-line">{emptySessionsText(collectionUncertain)}</div>}
    </section>
    <AuditLedger refreshKey={auditKey} />
    {pending && <StepUpSheet csrf={csrf} reason={stepUpReason} onCancel={() => setPending(null)} onConfirmed={until => { setStepUpUntil(until); const action = pending; setPending(null); void execute(action) }} />}
    {terminal && <TerminalSheet session={terminal} onClose={() => { setTerminal(null); setAuditKey(value => value + 1); loadInfo() }} />}
  </>
}

export function PublicRouteNote() {
  return <div className="route-note" role="note">
    <LockKeyhole size={19} />
    <div><strong>Terminal, attach, respostas e comandos só pela tailnet</strong><p>Você entrou pela rota pública. Aqui o painel apenas lê o estado das sessões. Abra o endereço privado do Tailscale para usar o terminal, o attach tmux, responder a um agente que espera input e os comandos do host.</p></div>
  </div>
}

