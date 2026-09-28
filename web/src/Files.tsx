import { useEffect, useRef, useState } from 'react'
import { AlertCircle, ArrowLeft, ChevronRight, File, FileText, Folder, FolderOpen, Link2, LockKeyhole } from 'lucide-react'
import { api, ApiError } from './api'
import type { FileContent, FileEntry, FileListing } from './types'

export type FileLocation = { host: string; root: string; path: string; file?: string } | null
type Failure = { message: string; code: string }

const blockedReasons: Record<string, string> = {
  secret: 'Nome indica credenciais ou segredos',
  outside: 'Aponta para fora da raiz',
  name: 'Nome ilegível (não é UTF-8)',
  unreadable: 'Sem permissão de leitura'
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let scaled = value / 1024
  let unit = 0
  while (scaled >= 1024 && unit < units.length - 1) { scaled /= 1024; unit++ }
  return `${scaled.toLocaleString('pt-BR', { maximumFractionDigits: scaled < 10 ? 1 : 0 })} ${units[unit]}`
}

function modified(timestamp: number) {
  return timestamp ? new Date(timestamp * 1000).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—'
}

function join(path: string, name: string) { return `${path}/${name}` }

function failure(err: unknown): Failure {
  if (err instanceof ApiError) return { message: err.message, code: err.code }
  return { message: err instanceof Error ? err.message : 'Não foi possível ler o host.', code: '' }
}

function Breadcrumbs({ location, onNavigate }: { location: NonNullable<FileLocation>; onNavigate: (next: FileLocation) => void }) {
  const parts = location.path.slice(location.root.length).split('/').filter(Boolean)
  const crumbs: { label: string; target: FileLocation; mono?: boolean }[] = [
    { label: 'Raízes', target: null },
    { label: location.root, target: { host: location.host, root: location.root, path: location.root }, mono: true },
    ...parts.map((part, index) => ({ label: part, target: { host: location.host, root: location.root, path: location.root + '/' + parts.slice(0, index + 1).join('/') } }))
  ]
  if (location.file) crumbs.push({ label: location.file.slice(location.path.length + 1), target: location })
  return <nav className="file-crumbs" aria-label="Caminho">
    <span className="file-host">{location.host}</span>
    <ol>{crumbs.map((crumb, index) => {
      const last = index === crumbs.length - 1
      return <li key={index}>
        {index > 0 && <ChevronRight size={14} aria-hidden="true" />}
        {last ? <span aria-current="location" className={crumb.mono ? 'is-mono' : ''}>{crumb.label}</span> : <button type="button" className={crumb.mono ? 'is-mono' : ''} onClick={() => onNavigate(crumb.target)}>{crumb.label}</button>}
      </li>
    })}</ol>
  </nav>
}

function EntryRow({ entry, onOpen }: { entry: FileEntry; onOpen: () => void }) {
  if (entry.blocked) {
    return <div className="file-row is-blocked"><LockKeyhole size={18} aria-hidden="true" /><span className="row-copy"><strong>{entry.name}</strong><small>{blockedReasons[entry.reason] || 'Leitura recusada'}</small></span><span className="state-stamp stamp-unknown">Bloqueado</span></div>
  }
  const Icon = entry.type === 'dir' ? Folder : entry.type === 'file' ? FileText : File
  const detail = entry.type === 'dir' ? <><span>pasta</span>{entry.link && <span> · link</span>}<span> · </span><time className="file-measure">{modified(entry.mtime)}</time></>
    : entry.type === 'file' ? <><span className="file-measure">{formatBytes(entry.size)}</span>{entry.link && <span> · link</span>}<span> · </span><time className="file-measure">{modified(entry.mtime)}</time></>
    : <span>{entry.link ? 'Link sem destino' : 'Arquivo especial não exibido'}</span>
  const copy = <><Icon size={18} aria-hidden="true" /><span className="row-copy"><strong>{entry.name}</strong><small>{detail}</small></span></>
  if (entry.type === 'other') return <div className="file-row">{copy}<span className="state-stamp stamp-unknown">{entry.link ? 'Link' : 'Especial'}</span></div>
  return <button type="button" className="file-row row-button" onClick={onOpen}>{copy}{entry.link ? <Link2 size={16} aria-label="link simbólico" /> : <ChevronRight size={17} aria-hidden="true" />}</button>
}

function Viewer({ location, onBack, onUnauthorized }: { location: NonNullable<FileLocation>; onBack: () => void; onUnauthorized: () => void }) {
  const file = location.file as string
  const [pages, setPages] = useState<string[]>([])
  const [last, setLast] = useState<FileContent | null>(null)
  const [error, setError] = useState<Failure | null>(null)
  const [loading, setLoading] = useState(true)
  const request = useRef(0)
  async function load(offset: number) {
    const version = ++request.current
    setLoading(true); setError(null)
    try {
      const page = await api.readFile(location.host, file, offset)
      if (version !== request.current) return
      setPages(previous => offset === 0 ? [page.content] : [...previous, page.content]); setLast(page)
    } catch (err) {
      if (version !== request.current) return
      if (err instanceof ApiError && err.status === 401) return onUnauthorized()
      setError(failure(err))
    } finally { if (version === request.current) setLoading(false) }
  }
  useEffect(() => { setPages([]); setLast(null); void load(0) }, [location.host, file])
  const name = file.slice(file.lastIndexOf('/') + 1)
  const blocked = error?.code === 'blocked'
  return <section className="file-sheet" aria-labelledby="file-title" aria-busy={loading}>
    <header>
      <div><h2 id="file-title">{name}</h2>{last && <p className="file-meta"><span className="file-measure">{formatBytes(last.size)}</span> · modificado <time className="file-measure">{modified(last.mtime)}</time></p>}</div>
      <div className="file-stamps">
        {blocked && <span className="state-stamp stamp-bad">Bloqueado</span>}
        {last?.binary && <span className="state-stamp stamp-unknown">Binário</span>}
        {last?.truncated && <span className="state-stamp stamp-bad">Truncado</span>}
        {last && !last.binary && !last.truncated && <span className="state-stamp">Completo</span>}
      </div>
    </header>
    {blocked ? <div className="file-state"><LockKeyhole size={20} /><p><strong>Bloqueado.</strong> O nome ou a pasta indica credenciais ou segredos; o painel não lê este arquivo.</p></div>
      : error ? <div className="file-state is-error"><AlertCircle size={20} /><p>{error.message}</p></div>
      : last?.binary && pages.every(page => page === '') ? <div className="file-state"><File size={20} /><p><strong>Binário.</strong> O conteúdo não é texto UTF-8 e não é exibido.</p></div>
      : pages.length ? <pre className="file-text" tabIndex={0} aria-label={`Conteúdo de ${name}`}>{pages.join('')}</pre>
      : loading ? <div className="file-state"><p>Lendo o arquivo no host…</p></div> : null}
    {last && !blocked && !error && <footer>
      {last.binary && pages.some(page => page !== '') && <p className="file-note is-warning">O trecho seguinte parece binário e não foi exibido.</p>}
      {last.truncated && !last.binary && <p className="file-note is-warning">Truncado: exibindo <span className="file-measure">{formatBytes(last.next_offset)}</span> de <span className="file-measure">{formatBytes(last.size)}</span>. Faltam <span className="file-measure">{formatBytes(last.size - last.next_offset)}</span>.</p>}
      {!last.truncated && !last.binary && <p className="file-note">Arquivo exibido por inteiro.</p>}
      <div className="preview-actions">
        {last.truncated && !last.binary && <button type="button" className="button button-small" disabled={loading} onClick={() => void load(last.next_offset)}>{loading ? 'Lendo…' : 'Carregar mais 512 KB'}</button>}
        <button type="button" className="button button-small button-plain" onClick={onBack}><ArrowLeft size={16} /> Voltar para a pasta</button>
      </div>
    </footer>}
    {(blocked || error) && <footer><div className="preview-actions"><button type="button" className="button button-small button-plain" onClick={onBack}><ArrowLeft size={16} /> Voltar para a pasta</button></div></footer>}
  </section>
}

function Directory({ location, onNavigate, onUnauthorized }: { location: NonNullable<FileLocation>; onNavigate: (next: FileLocation) => void; onUnauthorized: () => void }) {
  const [listing, setListing] = useState<FileListing | null>(null)
  const [error, setError] = useState<Failure | null>(null)
  useEffect(() => {
    let current = true
    setListing(null); setError(null)
    api.listFiles(location.host, location.path)
      .then(result => { if (current) setListing(result) })
      .catch(err => {
        if (!current) return
        if (err instanceof ApiError && err.status === 401) onUnauthorized()
        else setError(failure(err))
      })
    return () => { current = false }
  }, [location.host, location.path, onUnauthorized])
  const name = location.path === location.root ? location.root : location.path.slice(location.path.lastIndexOf('/') + 1)
  const blocked = listing?.entries.filter(entry => entry.blocked).length || 0
  return <section className="ledger-section file-listing" aria-labelledby="folder-heading" aria-busy={!listing && !error}>
    <div className="section-heading"><div><h2 id="folder-heading" className={location.path === location.root ? 'is-mono' : ''}>{name}</h2><p>{listing ? `${listing.entries.length} ${listing.entries.length === 1 ? 'item' : 'itens'}${blocked ? ` · ${blocked} ${blocked === 1 ? 'bloqueado' : 'bloqueados'}` : ''}` : error ? 'Leitura não concluída' : 'Lendo a pasta no host…'}</p></div>{location.path !== location.root && <button type="button" className="button button-small button-plain" onClick={() => onNavigate({ ...location, path: location.path.slice(0, location.path.lastIndexOf('/')) })}><ArrowLeft size={16} /> Pasta acima</button>}</div>
    {error ? <div className={`file-state ${error.code === 'blocked' ? '' : 'is-error'}`}>{error.code === 'blocked' ? <LockKeyhole size={20} /> : <AlertCircle size={20} />}<p>{error.code === 'blocked' ? <><strong>Bloqueado.</strong> Esta pasta guarda credenciais ou segredos.</> : error.message}</p></div>
      : !listing ? <div className="loading-ledger"><div className="loading-line" /><div className="loading-line short" /></div>
      : listing.entries.length === 0 ? <div className="empty-line">Pasta vazia.</div>
      : <div className="ruled-list">{listing.entries.map(entry => <EntryRow key={entry.name} entry={entry} onOpen={() => onNavigate(entry.type === 'dir' ? { ...location, path: join(location.path, entry.name), file: undefined } : { ...location, file: join(location.path, entry.name) })} />)}</div>}
    {listing?.truncated && <p className="file-note is-warning">Truncado: a pasta tem mais de {listing.entries.length} itens; somente os primeiros lidos aparecem.</p>}
  </section>
}

export function Files({ roots, location, onNavigate, onUnauthorized }: { roots: Record<string, string[]>; location: FileLocation; onNavigate: (next: FileLocation) => void; onUnauthorized: () => void }) {
  const hosts = Object.entries(roots)
  const valid = location && roots[location.host]?.includes(location.root) ? location : null
  return <div className="page-body"><div className="page-title"><h1>Arquivos</h1><p>Leitura das pastas autorizadas em cada host. Nada aqui altera arquivos; nomes com aparência de segredo ficam bloqueados.</p></div>
    {!valid ? <section className="ledger-section" aria-labelledby="roots-heading"><div className="section-heading"><div><h2 id="roots-heading">Raízes autorizadas</h2><p>Definidas no inventário. Hosts remotos aplicam também a lista gravada no próprio host.</p></div><span className="section-count">{hosts.reduce((total, [, list]) => total + list.length, 0)}</span></div>
      <div className="ruled-list">{hosts.flatMap(([host, list]) => list.map(root => <button type="button" className="file-row row-button" key={host + root} onClick={() => onNavigate({ host, root, path: root })}><FolderOpen size={18} aria-hidden="true" /><span className="row-copy"><strong className="is-mono">{root}</strong><small>{host} · somente leitura</small></span><ChevronRight size={17} aria-hidden="true" /></button>))}</div>
    </section> : <>
      <Breadcrumbs location={valid} onNavigate={onNavigate} />
      {valid.file ? <Viewer location={valid} onBack={() => onNavigate({ ...valid, file: undefined })} onUnauthorized={onUnauthorized} /> : <Directory location={valid} onNavigate={onNavigate} onUnauthorized={onUnauthorized} />}
    </>}
  </div>
}
