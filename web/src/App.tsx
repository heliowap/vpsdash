import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type React from 'react'
import {
  Activity, AlertCircle, ArrowUpRight, Boxes, Check, ChevronDown, ChevronRight,
  CircleHelp, Clock3, GitBranch, LockKeyhole, LogOut, RefreshCw,
  Terminal, Wifi
} from 'lucide-react'
import { api, ApiError } from './api'
import type { Dashboard, Host, Metric, Project, Repository, Runner, Session } from './types'

type Tab = 'overview' | 'projects' | 'fleet' | 'sessions'
type Notice = { kind: 'error' | 'success'; message: string } | null

const labels = ['self-hosted', 'ubuntu-latest', 'depot-ubuntu-24.04', 'depot-ubuntu-24.04-4', 'depot-ubuntu-24.04-8', 'ubicloud-standard-2']
const presets = [
  { id: 'economia', name: 'Economia', label: 'self-hosted' },
  { id: 'rapidez', name: 'Rapidez', label: 'depot-ubuntu-24.04-4' },
  { id: 'fallback', name: 'Fallback', label: 'ubuntu-latest' }
]
const tabs: { id: Tab; label: string; Icon: typeof Activity }[] = [
  { id: 'overview', label: 'Visão geral', Icon: Activity },
  { id: 'projects', label: 'Projetos', Icon: Boxes },
  { id: 'fleet', label: 'Frota', Icon: GitBranch },
  { id: 'sessions', label: 'Sessões', Icon: Terminal }
]

function age(timestamp?: number) {
  if (!timestamp) return 'sem leitura'
  const seconds = Math.max(0, Math.round(Date.now() / 1000 - timestamp))
  if (seconds < 60) return 'agora'
  if (seconds < 3600) return `há ${Math.floor(seconds / 60)} min`
  if (seconds < 86400) return `há ${Math.floor(seconds / 3600)} h`
  return `há ${Math.floor(seconds / 86400)} d`
}

function observationTime(timestamp: number) {
  return timestamp ? new Date(timestamp * 1000).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' }) : 'sem leitura'
}

function pct(value?: number) { return value === undefined ? '—' : `${Math.round(value)}%` }
function uptime(value?: number) {
  if (!value) return '—'
  const days = Math.floor(value / 86400)
  return days ? `${days} d` : `${Math.floor(value / 3600)} h`
}

function Sparkline({ points, label }: { points?: Metric[]; label: string }) {
  const values = points?.flatMap(point => point.cpu_pct === undefined ? [] : [point.cpu_pct]) || []
  if (values.length < 2) return <span className="sparkline-empty">Histórico em coleta</span>
  const path = values.map((value, index) => {
    const x = (index / Math.max(values.length - 1, 1)) * 100
    const y = 26 - (Math.min(100, Math.max(0, value)) / 100) * 24
    return `${index ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`
  }).join(' ')
  return <svg className="sparkline" viewBox="0 0 100 28" preserveAspectRatio="none" role="img" aria-label={`${label}: histórico de CPU dos últimos 30 dias`}><path d={path} /></svg>
}

function Login({ onLogin }: { onLogin: (csrf: string) => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  async function submit(event: React.FormEvent) {
    event.preventDefault(); setError(''); setBusy(true)
    try { const result = await api.login(password); setPassword(''); onLogin(result.csrf) }
    catch (err) { setError(err instanceof Error ? err.message : 'Não foi possível entrar.') }
    finally { setBusy(false) }
  }
  return <main className="login-page">
    <div className="login-mark"><Activity size={20} strokeWidth={2.4} /><span>vpsdash</span></div>
    <section className="login-sheet" aria-labelledby="login-title">
      <div className="login-seal"><LockKeyhole size={22} /></div>
      <h1 id="login-title">Sua operação, à mão.</h1>
      <p>Entre para ver a saúde das VPSs, projetos e runners da tailnet.</p>
      <form onSubmit={submit}>
        <label htmlFor="password">Senha do painel</label>
        <input id="password" autoComplete="current-password" type="password" value={password} onChange={event => setPassword(event.target.value)} required autoFocus />
        {error && <p className="form-error" role="alert"><AlertCircle size={16} /> {error}</p>}
        <button className="button button-primary" type="submit" disabled={busy}>{busy ? 'Entrando…' : 'Entrar no painel'} <ArrowUpRight size={17} /></button>
      </form>
    </section>
    <p className="login-foot">Acesso por senha · conexão HTTPS</p>
  </main>
}

function HostLedger({ hosts, histories }: { hosts: Host[]; histories: Record<string, Metric[]> }) {
  const vps = hosts.filter(host => host.kind === 'vps')
  const presence = hosts.filter(host => host.kind === 'presence')
  return <section className="ledger-section" aria-labelledby="hosts-heading">
    <div className="section-heading"><div><h2 id="hosts-heading">Hosts</h2><p>Métricas da última leitura e tendência de CPU em 30 dias.</p></div><span className="section-count">{vps.length} VPS</span></div>
    {vps.length === 0 ? <div className="empty-line">Nenhuma VPS configurada. Adicione hosts ao inventário do serviço.</div> :
      <div className="host-list">{vps.map(host => <article className="host-row" key={host.id}>
        <div className="host-title"><span className={`status-dot ${host.seen_at ? (host.online ? 'is-good' : 'is-bad') : 'is-unknown'}`} aria-hidden="true" /><div><h3>{host.id}</h3><p>{host.seen_at ? `${host.online ? 'Online' : 'Offline'} · ${age(host.seen_at)}` : 'Aguardando primeira leitura'}</p></div></div>
        <div className="host-measures"><span><b>{pct(host.latest?.cpu_pct)}</b><small>CPU</small></span><span><b>{pct(host.latest?.mem_pct)}</b><small>MEM</small></span><span><b>{pct(host.latest?.disk_pct)}</b><small>DISCO</small></span><span><b>{uptime(host.latest?.uptime_s)}</b><small>UPTIME</small></span></div>
        <Sparkline points={histories[host.id]} label={host.id} />
      </article>)}</div>}
    {presence.length > 0 && <div className="presence-line"><Wifi size={16} /><span>{presence.filter(host => host.online).length} de {presence.length} outros dispositivos online na tailnet</span></div>}
  </section>
}

type Incident = { id: string; title: string; detail: string; age?: number }
function incidents(data: Dashboard): Incident[] {
  const items: Incident[] = []
  for (const host of data.hosts) if (host.kind === 'vps' && host.seen_at && !host.online) items.push({ id: `host-${host.id}`, title: `${host.id} está offline`, detail: 'Verifique o acesso SSH e a conexão pela tailnet.', age: host.seen_at })
  for (const project of data.projects) if (project.monitored && project.check_ok === false) items.push({ id: `project-${project.id}`, title: `${project.name} falhou`, detail: `Em ${project.host_id} · verifique o critério monitorado.`, age: project.checked_at })
  for (const runner of data.runners) if (runner.status === 'offline') items.push({ id: `runner-${runner.runner_id}`, title: `${runner.name} está offline`, detail: `Runner de ${runner.repo}.`, age: runner.seen_at })
  for (const [scope] of Object.entries(data.collector_errors)) {
    if (scope.startsWith('host:') && items.some(item => item.id === `host-${scope.slice(5)}`)) continue
    items.push({ id: `collector-${scope}`, title: `Coleta indisponível: ${scope.replace(':', ' / ')}`, detail: 'A última consulta falhou. Confira a configuração e os logs do serviço.' })
  }
  for (const repo of data.repositories) if (repo.error && !data.collector_errors[`github:${repo.name}`]) {
    items.push({ id: `integration-${repo.name}`, title: `GitHub indisponível: ${repo.name}`, detail: repo.error })
  }
  return items
}

function Overview({ data, histories, refreshFailed }: { data: Dashboard; histories: Record<string, Metric[]>; refreshFailed: boolean }) {
  const problems = useMemo(() => incidents(data), [data])
  const observedAt = Math.max(0, data.fleet_seen_at, ...data.hosts.map(host => host.seen_at || 0), ...data.projects.map(project => project.checked_at || 0), ...data.sessions.map(session => session.seen_at || 0))
  const observed = observedAt > 0
  const stale = refreshFailed || (observed && Date.now() / 1000 - observedAt > 120)
  const fleetCurrent = !refreshFailed && data.fleet_seen_at > 0 && Date.now() / 1000 - data.fleet_seen_at <= 120 && !Object.keys(data.collector_errors).some(scope => scope.startsWith('github:'))
  return <>
    <section className={`situation ${problems.length ? 'situation-attention' : ''}`} aria-labelledby="situation-title">
      <div className="situation-top"><span className={`live-mark ${stale ? 'is-stale' : ''}`}><span /> {stale ? 'LEITURA DESATUALIZADA' : 'ESTADO OBSERVADO'}</span><time>Última leitura {observationTime(observedAt)}</time></div>
      <h1 id="situation-title">{problems.length ? `${problems.length} ${problems.length === 1 ? 'item precisa' : 'itens precisam'} de atenção` : observed ? 'Nenhum incidente ativo' : 'Aguardando a primeira leitura'}</h1>
      <p>{stale ? 'A última leitura continua visível; atualize para confirmar o estado.' : problems.length ? 'Comece pelo primeiro item da lista. Os demais continuam visíveis abaixo.' : observed ? 'Os projetos monitorados e hosts observados não indicam falhas nesta leitura.' : 'Os coletores preencherão o painel assim que o serviço alcançar os hosts.'}</p>
      {problems.length > 0 && <div className="incident-list">{problems.map(problem => <div className="incident-row" key={problem.id}><AlertCircle size={19} /><div><strong>{problem.title}</strong><small>{problem.detail}</small></div>{problem.age && <time>{age(problem.age)}</time>}</div>)}</div>}
      {!data.smtp_provisioned && <div className="situation-warning"><AlertCircle size={16} /><span>Alertas por e-mail ainda não provisionados.</span></div>}
    </section>
    <HostLedger hosts={data.hosts} histories={histories} />
    <section className="ledger-section overview-tail" aria-labelledby="activity-heading"><div className="section-heading"><div><h2 id="activity-heading">Em andamento</h2><p>Atividade recente da frota e das sessões.</p></div></div>
      <div className="activity-grid"><div><span className="activity-number">{fleetCurrent ? data.runners.filter(runner => runner.busy).length : '—'}</span><span>runners ocupados</span></div><div><span className="activity-number">{fleetCurrent ? Object.values(data.queued).flat().length : '—'}</span><span>jobs na fila</span></div><div><span className="activity-number">{data.sessions.length}</span><span>sessões tmux</span></div></div>
    </section>
  </>
}

function ProjectEditor({ project, csrf, onSaved }: { project: Project; csrf: string; onSaved: () => void }) {
  const [monitored, setMonitored] = useState(project.monitored)
  const [healthURL, setHealthURL] = useState(project.health_url || '')
  const [expected, setExpected] = useState(() => {
    try { return (JSON.parse(project.expected || '[]') as string[]).join(', ') } catch { return '' }
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function save(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api.updateProject(project.id, { monitored, health_url: healthURL.trim(), expected: expected.split(',').map(item => item.trim()).filter(Boolean) }, csrf); onSaved() }
    catch (err) { setError(err instanceof Error ? err.message : 'Não foi possível salvar.') }
    finally { setBusy(false) }
  }
  return <form className="inline-editor" onSubmit={save}>
    <label className="switch-line"><input type="checkbox" checked={monitored} onChange={event => setMonitored(event.target.checked)} /><span>Monitorar este projeto</span></label>
    <div className="editor-fields"><label>URL de health <input type="url" value={healthURL} onChange={event => setHealthURL(event.target.value)} placeholder="https://serviço/health" /></label><label>Serviços esperados <input value={expected} onChange={event => setExpected(event.target.value)} placeholder={project.name} /><small>Separe por vírgulas. Use a URL ou os serviços esperados.</small></label></div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <button className="button button-primary button-small" disabled={busy} type="submit">{busy ? 'Salvando…' : 'Salvar monitoramento'}</button>
  </form>
}

function Projects({ data, csrf, onRefresh }: { data: Dashboard; csrf: string; onRefresh: () => void }) {
  const [expanded, setExpanded] = useState<number | null>(null)
  const [query, setQuery] = useState('')
  const [showAll, setShowAll] = useState(false)
  const monitored = data.projects.filter(project => project.monitored)
  const candidates = data.projects.filter(project => !project.monitored).sort((a, b) => {
    const rank = { docker: 0, tmux: 1, systemd: 2 }
    return rank[a.source] - rank[b.source] || a.name.localeCompare(b.name)
  })
  const filtered = candidates.filter(project => `${project.name} ${project.host_id} ${project.source}`.toLocaleLowerCase('pt-BR').includes(query.toLocaleLowerCase('pt-BR')))
  const visible = showAll || query ? filtered : filtered.slice(0, 8)
  function row(project: Project) {
    const open = expanded === project.id
    const contents = <>
      <span className={`status-dot ${!project.monitored || project.check_ok === undefined ? 'is-unknown' : project.check_ok ? 'is-good' : 'is-bad'}`} />
      <span className="row-copy"><strong>{project.name}</strong><small>{project.host_id} · {project.native ? 'unit nativa' : project.source} · {project.monitored ? age(project.checked_at) : 'candidato'}</small></span>
      <span className={`state-stamp ${project.check_ok === false ? 'stamp-bad' : ''}`}>{project.monitored ? project.check_ok === undefined ? 'Aguardando' : project.check_ok ? 'Ativo' : 'Falhou' : 'Silencioso'}</span>
    </>
    return <article className="project-row" key={project.id}>
      {project.native ? <div className="row-main native-main">{contents}</div> : <button className="row-main" type="button" aria-expanded={open} onClick={() => setExpanded(open ? null : project.id)}>{contents}{open ? <ChevronDown size={17} /> : <ChevronRight size={17} />}</button>}
      {open && !project.native && <ProjectEditor key={`${project.id}-${project.monitored}`} project={project} csrf={csrf} onSaved={() => { setExpanded(null); onRefresh() }} />}
    </article>
  }
  return <div className="page-body"><div className="page-title"><h1>Projetos</h1><p>Candidatos só geram alertas após promoção. As units nativas dos runners são monitoradas automaticamente.</p></div>
    <section className="ledger-section"><div className="section-heading"><h2>Monitorados</h2><span className="section-count">{monitored.length}</span></div>{monitored.length ? <div className="ruled-list">{monitored.map(row)}</div> : <div className="empty-line">Nenhum projeto monitorado ainda. Abra um candidato abaixo para definir o critério.</div>}</section>
    <section className="ledger-section"><div className="section-heading"><h2>Candidatos</h2><span className="section-count">{candidates.length}</span></div>{candidates.length ? <><div className="candidate-tools"><input type="search" aria-label="Buscar candidatos" placeholder="Buscar por nome, host ou origem" value={query} onChange={event => setQuery(event.target.value)} /></div><div className="ruled-list">{visible.length ? visible.map(row) : <div className="empty-line">Nenhum candidato corresponde à busca.</div>}</div>{!query && !showAll && candidates.length > 8 && <button type="button" className="button candidate-more" onClick={() => setShowAll(true)}>Ver todos os {candidates.length} candidatos</button>}</> : <div className="empty-line">Aguardando a próxima descoberta em docker, systemd e tmux.</div>}</section>
  </div>
}

function RepoSwitch({ repo, variable, csrf, onRefresh }: { repo: Repository; variable: 'AGENT_RUNNER' | 'CI_RUNNER'; csrf: string; onRefresh: () => void }) {
  const current = variable === 'AGENT_RUNNER' ? repo.agent_runner : repo.ci_runner
  const enabled = variable === 'AGENT_RUNNER' ? repo.agent_switchable : repo.ci_switchable
  const operable = enabled && !repo.error
  const [choice, setChoice] = useState(current || 'self-hosted')
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const [feedback, setFeedback] = useState<Notice>(null)
  useEffect(() => { setChoice(current || 'self-hosted') }, [current])
  async function apply() {
    setBusy(true); setFeedback(null)
    try { await api.switchRunner(repo.name, variable, choice, csrf); setFeedback({ kind: 'success', message: 'Alteração salva no GitHub.' }); setConfirm(false); onRefresh() }
    catch (err) { setFeedback({ kind: 'error', message: err instanceof Error ? err.message : 'Não foi possível trocar.' }) }
    finally { setBusy(false) }
  }
  return <div className="backend-row"><div><strong>{variable === 'AGENT_RUNNER' ? 'Agentes' : 'CI'}</strong><small>{!enabled ? 'Workflow sem switch confirmado' : repo.error ? 'Valor atual desconhecido — GitHub indisponível' : `Atual: ${current || 'padrão do workflow'}`}</small></div>
    {operable ? <div className="backend-control"><select aria-label={`Backend de ${variable} em ${repo.name}`} value={choice} onChange={event => { setChoice(event.target.value); setConfirm(false) }}>{labels.map(label => <option key={label} value={label}>{label}</option>)}</select><button className="button button-small" type="button" disabled={choice === current || busy} onClick={() => setConfirm(true)}>Trocar</button></div> : enabled ? <span className="muted-text">Integração indisponível</span> : <a className="button button-small" href={`https://github.com/${repo.name}/pulls`} target="_blank" rel="noreferrer" aria-label={`Abrir PRs de ${repo.name} para solicitar adoção de ${variable}`}>Abrir PRs para pedir /oc</a>}
    {confirm && <div className="inline-confirm"><span>Trocar {repo.name} de <b>{current || 'padrão'}</b> para <b>{choice}</b>?</span><div><button type="button" className="button button-small button-primary" disabled={busy} onClick={apply}>{busy ? 'Aplicando…' : 'Confirmar'}</button><button type="button" className="button button-small button-plain" onClick={() => setConfirm(false)}>Cancelar</button></div></div>}
    {feedback && <p className={`inline-feedback ${feedback.kind === 'error' ? 'is-error' : ''}`} role={feedback.kind === 'error' ? 'alert' : 'status'}>{feedback.message}</p>}
  </div>
}

type SwitchVariable = 'AGENT_RUNNER' | 'CI_RUNNER'
type SwitchTarget = { repo: string; variable: SwitchVariable; current: string; currentKnown: boolean }

function currentLabel(target: SwitchTarget): string {
  return target.currentKnown ? target.current || 'padrão do workflow' : 'valor atual desconhecido'
}

function switchOutcome(results: Record<string, string>): Notice {
  const failed = Object.entries(results).filter(([, result]) => result !== 'ok').map(([name]) => name)
  const updated = Object.keys(results).length - failed.length
  return { kind: failed.length ? 'error' : 'success', message: failed.length ? `Atualizações concluídas: ${updated}. Falharam: ${failed.join(', ')}.` : `Atualizações concluídas: ${updated}.` }
}

function Fleet({ data, csrf, onRefresh, refreshFailed }: { data: Dashboard; csrf: string; onRefresh: () => void; refreshFailed: boolean }) {
  const [presetPending, setPresetPending] = useState('')
  const [bulkVariable, setBulkVariable] = useState<SwitchVariable>('AGENT_RUNNER')
  const [bulkLabel, setBulkLabel] = useState('ubuntu-latest')
  const [selected, setSelected] = useState<string[]>([])
  const [bulkPending, setBulkPending] = useState(false)
  const [feedback, setFeedback] = useState<Notice>(null)
  const [busy, setBusy] = useState(false)
  const selectable = data.repositories.filter(repo => !repo.error && (bulkVariable === 'AGENT_RUNNER' ? repo.agent_switchable : repo.ci_switchable))
  const hasOperations = data.repositories.some(repo => !repo.error && (repo.agent_switchable || repo.ci_switchable))
  const fleetObserved = data.fleet_seen_at > 0
  const fleetErrors = Object.keys(data.collector_errors).some(scope => scope.startsWith('github:'))
  const fleetStale = fleetObserved && Date.now() / 1000 - data.fleet_seen_at > 120
  const fleetUncertain = refreshFailed || fleetErrors || fleetStale
  const bulkTargets: SwitchTarget[] = selectable.filter(repo => selected.includes(repo.name)).map(repo => ({ repo: repo.name, variable: bulkVariable, current: bulkVariable === 'AGENT_RUNNER' ? repo.agent_runner : repo.ci_runner, currentKnown: true }))
  const presetTargets: SwitchTarget[] = data.repositories.flatMap(repo => ([
    ...(repo.agent_switchable ? [{ repo: repo.name, variable: 'AGENT_RUNNER' as const, current: repo.agent_runner, currentKnown: !repo.error }] : []),
    ...(repo.ci_switchable ? [{ repo: repo.name, variable: 'CI_RUNNER' as const, current: repo.ci_runner, currentKnown: !repo.error }] : [])
  ]))
  const unknownPresetTargets = presetTargets.filter(target => !target.currentKnown).length
  const pendingPreset = presets.find(preset => preset.id === presetPending)
  const queue = Object.entries(data.queued).flatMap(([repo, runs]) => runs.map(run => ({ repo, ...run })))
  async function applyPreset(name: string) {
    setBusy(true); setFeedback(null)
    try { const result = await api.preset(name, csrf); setFeedback(switchOutcome(result.results)); onRefresh() }
    catch (err) { setFeedback({ kind: 'error', message: err instanceof Error ? err.message : 'Preset não aplicado.' }) }
    finally { setBusy(false); setPresetPending('') }
  }
  async function applyBulk() {
    setBusy(true); setFeedback(null)
    try { const result = await api.bulkSwitch(selected, bulkVariable, bulkLabel, csrf); setFeedback(switchOutcome(result.results)); onRefresh() }
    catch (err) { setFeedback({ kind: 'error', message: err instanceof Error ? err.message : 'Alteração não aplicada.' }) }
    finally { setBusy(false); setBulkPending(false) }
  }
  return <div className="page-body"><div className="page-title"><h1>Frota</h1><p>Runners, fila e backends operados pelas variáveis que os workflows já leem.</p></div>
    {!data.repositories.length ? <section className="ledger-section"><div className="section-heading"><h2>Fonte da frota</h2></div><div className="empty-line">Adicione repositórios ao inventário para iniciar a leitura de runners e fila.</div></section> : !fleetObserved ? <section className="ledger-section"><div className="section-heading"><h2>Fonte da frota</h2></div><div className="empty-line">{fleetErrors ? 'GitHub App indisponível. Runners e fila ainda são desconhecidos.' : 'Aguardando a primeira leitura do GitHub App. Runners e fila ainda são desconhecidos.'}</div></section> : <>
      {fleetUncertain && <p className="notice notice-error" role="alert"><AlertCircle size={18} />Leitura da frota parcial ou desatualizada. Confirme o estado antes de agir.</p>}
      <section className="ledger-section"><div className="section-heading"><h2>Runners</h2><span className="section-count">{fleetUncertain ? 'contagem desconhecida' : `${data.runners.length} ${data.runners.length === 1 ? 'registrado' : 'registrados'}`}</span></div>{data.runners.length ? <div className="ruled-list">{data.runners.map(runner => <RunnerRow runner={runner} uncertain={fleetUncertain} key={`${runner.repo}-${runner.runner_id}`} />)}</div> : <div className="empty-line">{fleetUncertain ? 'A leitura atual dos runners não está disponível.' : 'Nenhum runner registrado nos repositórios consultados.'}</div>}</section>
      <section className="ledger-section"><div className="section-heading"><h2>Fila</h2><span className="section-count">{fleetUncertain ? 'contagem desconhecida' : `${queue.length} ${queue.length === 1 ? 'job' : 'jobs'}`}</span></div>{queue.length ? <div className="ruled-list">{queue.map(run => <a className="queue-row" key={`${run.repo}-${run.id}`} href={run.html_url} target="_blank" rel="noreferrer"><Clock3 size={18} /><span><strong>{run.display_title || run.name}</strong><small>{run.repo}</small></span><ArrowUpRight size={17} /></a>)}</div> : <div className="empty-line">{fleetUncertain ? 'A leitura atual da fila não está disponível.' : 'Nenhum job aguardando runner.'}</div>}</section>
    </>}
    <section className="ledger-section"><div className="section-heading"><div><h2>Backend por repositório</h2><p>Somente workflows com switch confirmado podem ser alterados aqui.</p></div></div>{data.repositories.length ? data.repositories.map(repo => <article className="repo-sheet" key={repo.name}><header><h3>{repo.name}</h3>{repo.error && <span className="state-stamp stamp-bad">{repo.error}</span>}</header><RepoSwitch repo={repo} variable="AGENT_RUNNER" csrf={csrf} onRefresh={onRefresh} /><RepoSwitch repo={repo} variable="CI_RUNNER" csrf={csrf} onRefresh={onRefresh} /></article>) : <div className="empty-line">Adicione repositórios ao inventário para operar o switch.</div>}</section>
    {hasOperations && <>
      <section className="ledger-section">
        <div className="section-heading"><h2>Aplicar em lote</h2></div>
        <div className="bulk-panel">
          <div className="bulk-fields">
            <label>Variável<select value={bulkVariable} onChange={event => { setBulkVariable(event.target.value as SwitchVariable); setSelected([]); setBulkPending(false) }}><option value="AGENT_RUNNER">Agentes</option><option value="CI_RUNNER">CI</option></select></label>
            <label>Backend<select value={bulkLabel} onChange={event => { setBulkLabel(event.target.value); setBulkPending(false) }}>{labels.map(label => <option key={label}>{label}</option>)}</select></label>
          </div>
          <fieldset><legend>Repositórios</legend>{selectable.length ? selectable.map(repo => <label key={repo.name} className="checkbox-row"><input type="checkbox" checked={selected.includes(repo.name)} onChange={event => { setSelected(event.target.checked ? [...selected, repo.name] : selected.filter(name => name !== repo.name)); setBulkPending(false) }} />{repo.name}</label>) : <p className="muted-text">Nenhum repositório usa esta variável ainda.</p>}</fieldset>
          <button className="button button-primary button-small" type="button" disabled={!selected.length || busy} onClick={() => setBulkPending(true)}>Revisar {selected.length} {selected.length === 1 ? 'repositório' : 'repositórios'}</button>
          {bulkPending && <div className="operation-preview">
            <strong>Confirmar alterações em lote</strong>
            <ul>{bulkTargets.map(target => <li key={target.repo}><b>{target.repo}</b> · {target.variable}: {currentLabel(target)} → {bulkLabel}</li>)}</ul>
            <div className="preview-actions"><button className="button button-primary button-small" type="button" disabled={busy} onClick={applyBulk}>Confirmar alterações</button><button className="button button-plain button-small" type="button" onClick={() => setBulkPending(false)}>Cancelar</button></div>
          </div>}
        </div>
      </section>
      <section className="ledger-section">
        <div className="section-heading"><h2>Presets</h2></div>
        <div className="preset-list">{presets.map(preset => <div className="preset-row" key={preset.id}><div><strong>{preset.name}</strong><small>{preset.label} · todos os switches confirmados</small></div><button className="button button-small" type="button" onClick={() => setPresetPending(preset.id)}>Ver prévia</button></div>)}</div>
        {pendingPreset && <div className="operation-preview preset-preview">
          <strong>{pendingPreset.name}: {presetTargets.length} alterações</strong>
          <ul>{presetTargets.map(target => <li key={target.repo + target.variable}><b>{target.repo}</b> · {target.variable}: {currentLabel(target)} → {pendingPreset.label}</li>)}</ul>
          {unknownPresetTargets > 0 && <p className="preview-warning" role="alert">GitHub não confirmou o valor atual de {unknownPresetTargets} {unknownPresetTargets === 1 ? 'switch' : 'switches'}. Confirmar tentará sobrescrever esses valores mesmo assim.</p>}
          <div className="preview-actions"><button className="button button-primary button-small" type="button" disabled={busy} onClick={() => applyPreset(pendingPreset.id)}>{unknownPresetTargets > 0 ? 'Confirmar com valores desconhecidos' : 'Confirmar preset'}</button><button className="button button-plain button-small" type="button" onClick={() => setPresetPending('')}>Cancelar</button></div>
        </div>}
      </section>
    </>}
    {feedback && <p className={'notice notice-' + feedback.kind} role={feedback.kind === 'error' ? 'alert' : 'status'}>{feedback.kind === 'error' ? <AlertCircle size={18} /> : <Check size={18} />}{feedback.message}</p>}
  </div>
}

function RunnerRow({ runner, uncertain }: { runner: Runner; uncertain: boolean }) {
  const lastState = runner.job || (runner.busy ? 'Ocupado' : runner.status === 'online' ? 'Livre' : 'Offline')
  return <div className="runner-row"><span className={`status-dot ${uncertain ? 'is-unknown' : runner.status === 'online' ? 'is-good' : 'is-bad'}`} /><div><strong>{runner.name}</strong><small>{runner.repo} · {uncertain ? `última leitura ${age(runner.seen_at)} · ${lastState}` : lastState}</small></div><span className={`state-stamp ${uncertain ? 'stamp-unknown' : runner.status === 'offline' ? 'stamp-bad' : ''}`}>{uncertain ? 'Não confirmado' : runner.status === 'offline' ? 'Offline' : runner.busy ? 'Ocupado' : 'Livre'}</span></div>
}

function Sessions({ data }: { data: Dashboard }) {
  return <div className="page-body"><div className="page-title"><h1>Sessões</h1><p>tmux mantém seus agentes vivos no host. O processo em cada pane identifica o agente.</p></div><section className="ledger-section"><div className="section-heading"><h2>Sessões encontradas</h2><span className="section-count">{data.sessions.length}</span></div>{data.sessions.length ? <div className="ruled-list">{data.sessions.map((session: Session) => <div className="session-row" key={`${session.host_id}-${session.name}`}><Terminal size={19} /><div><strong>{session.name}</strong><small>{session.host_id} · {session.cwd || 'caminho indisponível'} · {age(session.seen_at)}</small></div><span className="state-stamp">{session.agent || 'shell'}</span></div>)}</div> : <div className="empty-line">Nenhuma sessão tmux observada. As sessões aparecem quando os hosts forem alcançados.</div>}</section><div className="info-note"><CircleHelp size={18} /><p>O acesso ao terminal requer a chave SSH de leitura do usuário <code>vpsdash</code> no host de cada sessão.</p></div></div>
}

export default function App() {
  const [session, setSession] = useState<{ authenticated: boolean; csrf: string } | null>(null)
  const [data, setData] = useState<Dashboard | null>(null)
  const [histories, setHistories] = useState<Record<string, Metric[]>>({})
  const [tab, setTab] = useState<Tab>('overview')
  const [notice, setNotice] = useState<Notice>(null)
  const [refreshing, setRefreshing] = useState(false)
  const refreshVersion = useRef(0)

  const refresh = useCallback(async () => {
    if (!session?.authenticated) return
    const version = ++refreshVersion.current
    setRefreshing(true)
    try {
      const result = await api.dashboard()
      if (version !== refreshVersion.current) return
      setData(result)
      setNotice(null)
      const hosts = result.hosts.filter(host => host.kind === 'vps')
      const items = await Promise.all(hosts.map(async host => {
        try { return [host.id, await api.metrics(host.id)] as const }
        catch (err) {
          if (err instanceof ApiError && err.status === 401) throw err
          return [host.id, null] as const
        }
      }))
      if (version !== refreshVersion.current) return
      setHistories(current => Object.fromEntries(items.map(([id, points]) => [id, points ?? current[id] ?? []])))
    }
    catch (err) {
      if (version !== refreshVersion.current) return
      if (err instanceof ApiError && err.status === 401) { setSession({ authenticated: false, csrf: '' }); setData(null); setHistories({}) }
      else setNotice({ kind: 'error', message: err instanceof Error ? err.message : 'Falha ao atualizar o painel.' })
    } finally { if (version === refreshVersion.current) setRefreshing(false) }
  }, [session?.authenticated])

  useEffect(() => { api.session().then(setSession).catch(() => setSession({ authenticated: false, csrf: '' })) }, [])
  useEffect(() => { if (!session?.authenticated) return; void refresh(); const timer = window.setInterval(() => void refresh(), 30_000); return () => window.clearInterval(timer) }, [refresh, session?.authenticated])

  async function logout() {
    if (!session) return
    ++refreshVersion.current
    try { await api.logout(session.csrf) } finally { setSession({ authenticated: false, csrf: '' }); setData(null); setHistories({}); setRefreshing(false) }
  }

  if (!session) return <div className="boot-screen"><div className="boot-mark"><Activity size={23} /> vpsdash</div><span>Carregando painel…</span></div>
  if (!session.authenticated) return <Login onLogin={csrf => setSession({ authenticated: true, csrf })} />

  return <div className="app-shell">
    <aside className="side-rail"><div className="brand"><span className="brand-icon"><Activity size={19} strokeWidth={2.5} /></span><span>vpsdash</span></div><nav aria-label="Seções do painel">{tabs.map(({ id, label, Icon }) => <button key={id} type="button" className={`nav-item ${tab === id ? 'active' : ''}`} onClick={() => setTab(id)} aria-current={tab === id ? 'page' : undefined}><Icon size={19} /><span>{label}</span></button>)}</nav><div className="rail-foot"><span className="rail-label">ACESSO AUTENTICADO</span><span>HTTPS · operador único</span></div></aside>
    <div className="main-wrap"><header className="top-bar"><div className="mobile-brand"><Activity size={19} strokeWidth={2.5} /><strong>vpsdash</strong></div><div className="top-context"><span className="top-context-title">Central de comando</span><span className="top-context-sub">Observação em tempo real</span></div><div className="top-actions"><button className={`icon-button ${refreshing ? 'is-spinning' : ''}`} type="button" onClick={() => void refresh()} aria-label="Atualizar dados" title="Atualizar dados"><RefreshCw size={19} /></button><button className="icon-button" type="button" onClick={() => void logout()} aria-label="Sair" title="Sair"><LogOut size={19} /></button></div></header>
      <main className="content"><div className="content-inner">
        {notice && <div className={`notice notice-${notice.kind}`} role="alert"><AlertCircle size={18} />{notice.message}</div>}
        {data ? tab === 'overview' ? <Overview data={data} histories={histories} refreshFailed={notice?.kind === 'error'} /> : tab === 'projects' ? <Projects data={data} csrf={session.csrf} onRefresh={() => void refresh()} /> : tab === 'fleet' ? <Fleet data={data} csrf={session.csrf} onRefresh={() => void refresh()} refreshFailed={notice?.kind === 'error'} /> : <Sessions data={data} /> : <div className="loading-ledger"><div className="loading-line" /><div className="loading-line short" /><p>Buscando a primeira leitura…</p></div>}
        {data && !data.smtp_provisioned && tab !== 'overview' && <div className="service-note"><AlertCircle size={16} /><span>Canal de alerta por e-mail não provisionado. Eventos ficam enfileirados.</span></div>}
      </div></main>
    </div>
    <nav className="bottom-nav" aria-label="Seções do painel">{tabs.map(({ id, label, Icon }) => <button key={id} type="button" className={tab === id ? 'active' : ''} aria-current={tab === id ? 'page' : undefined} onClick={() => setTab(id)}><Icon size={20} strokeWidth={tab === id ? 2.3 : 1.8} /><span>{label}</span></button>)}</nav>
  </div>
}
