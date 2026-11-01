import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Search, Plus, Database, Loader2, X, List, Trash2, Pencil, Download, Upload, Clock, ChevronDown,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  searchMemory, addMemory, listMemories, deleteMemory, fetchMemoryNamespaces,
  exportMemories, importMemories, type AddMemoryOptions,
} from '../api/client'
import type { MemoryHit } from '../api/types'
import MemoryEditModal from '../components/MemoryEditModal'
import { downloadBlob } from '../lib/download'
import { fmtDateTime, fmtRelativeFuture } from '../lib/time'

// Select value for "every namespace"; '' is the shared default namespace.
const ALL_NS = '\u0000all'

function DistanceBadge({ d }: { d: number }) {
  const pct = Math.round(d * 100)
  const color = d < 0.3 ? 'text-emerald-400' : d < 0.6 ? 'text-yellow-400' : 'text-red-400'
  return (
    <span className={`text-[10px] font-mono ${color}`}>
      {pct}% dist
    </span>
  )
}

function MemoryCard({ h, onDelete, onEdit, deleting }: {
  h: MemoryHit
  onDelete: (id: string) => void
  onEdit: (h: MemoryHit) => void
  deleting: boolean
}) {
  const ns = typeof h.metadata.namespace === 'string' ? h.metadata.namespace : ''
  const exp = parseFloat(String(h.metadata.expires_at ?? ''))
  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4 animate-fade-in">
      <div className="flex items-start justify-between gap-3 mb-2">
        <p className="text-sm text-gray-200 leading-relaxed whitespace-pre-wrap break-words">{h.text}</p>
        <div className="flex items-center gap-2 shrink-0">
          {ns && (
            <span className="rounded bg-accent/15 px-1.5 py-0.5 font-mono text-[10px] text-accent" title="Namespace">{ns}</span>
          )}
          {!isNaN(exp) && (
            <span className="flex items-center gap-1 text-[10px] text-amber-400/80" title={`Expires ${fmtDateTime(exp)}`}>
              <Clock size={10} /> {fmtRelativeFuture(exp) === 'expired' ? 'expired' : `expires ${fmtRelativeFuture(exp)}`}
            </span>
          )}
          {typeof h.distance === 'number' && <DistanceBadge d={h.distance} />}
          <button
            onClick={() => onEdit(h)}
            title="Edit memory"
            className="text-gray-600 hover:text-gray-300 transition-colors"
          >
            <Pencil size={13} />
          </button>
          <button
            onClick={() => onDelete(h.id)}
            disabled={deleting}
            title="Delete memory"
            className="text-gray-600 hover:text-red-400 transition-colors disabled:opacity-40"
          >
            {deleting ? <Loader2 size={13} className="animate-spin" /> : <Trash2 size={13} />}
          </button>
        </div>
      </div>
      {Object.keys(h.metadata).length > 0 && (
        <pre className="mt-2 rounded-lg bg-surface p-2 font-mono text-[11px] text-gray-500 overflow-x-auto">
          {JSON.stringify(h.metadata, null, 2)}
        </pre>
      )}
      <p className="mt-1 font-mono text-[10px] text-gray-700 truncate">{h.id}</p>
    </div>
  )
}

export default function Memory() {
  const [query, setQuery]   = useState('')
  const [k, setK]           = useState(10)
  const [hits, setHits]     = useState<MemoryHit[]>([])
  const [addText, setAddText] = useState('')
  const [addMeta, setAddMeta] = useState('')
  const [showAdd, setShowAdd] = useState(false)
  const [browse, setBrowse]   = useState(false)
  const [addNs, setAddNs]     = useState('')
  const [addTtlDays, setAddTtlDays] = useState('')
  const [addNever, setAddNever] = useState(false)
  const [nsFilter, setNsFilter] = useState<string>(ALL_NS)
  const [editing, setEditing] = useState<MemoryHit | null>(null)
  const [withEmb, setWithEmb] = useState(false)
  const [transferring, setTransferring] = useState<'export' | 'import' | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const qc = useQueryClient()
  const ns = nsFilter === ALL_NS ? undefined : nsFilter

  const nsQuery = useQuery({
    queryKey: ['memory-namespaces'],
    queryFn: fetchMemoryNamespaces,
    staleTime: 30_000,
  })
  const namespaces = nsQuery.data ?? []

  const browseQuery = useQuery({
    queryKey: ['memories', ns ?? null],
    queryFn: () => listMemories(200, 0, ns),
    enabled: browse,
  })

  async function handleExport() {
    setTransferring('export')
    try {
      const blob = await exportMemories(withEmb)
      downloadBlob(blob, 'memories.jsonl')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setTransferring(null)
    }
  }

  async function handleImport(file: File) {
    setTransferring('import')
    try {
      const r = await importMemories(file)
      const summary = `${r.imported} imported, ${r.duplicates} duplicate, ${r.existing} existing, ${r.failed} failed`
      const detail = r.errors?.length ? r.errors.slice(0, 3).join('; ') : undefined
      if (r.failed > 0) toast.warning(`Import finished: ${summary}`, { description: detail })
      else toast.success(`Import finished: ${summary}`)
      qc.invalidateQueries({ queryKey: ['memories'] })
      qc.invalidateQueries({ queryKey: ['memory-namespaces'] })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setTransferring(null)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  function onSaved(hit: MemoryHit) {
    setHits((prev) => prev.map((h) => (h.id === hit.id ? { ...hit, distance: h.distance } : h)))
  }

  const searchMut = useMutation({
    mutationFn: () => searchMemory(query, k, ns === undefined ? undefined : [ns]),
    onSuccess: setHits,
    onError: (e: Error) => toast.error(e.message),
  })

  const addMut = useMutation({
    mutationFn: () => {
      let meta: Record<string, unknown> = {}
      try { if (addMeta.trim()) meta = JSON.parse(addMeta) } catch { throw new Error('Invalid JSON in metadata') }
      const opts: AddMemoryOptions = {}
      if (addNs.trim()) opts.namespace = addNs.trim()
      if (addNever) opts.ttl_seconds = -1
      else if (Number(addTtlDays) > 0) opts.ttl_seconds = Math.round(Number(addTtlDays) * 86400)
      return addMemory(addText, meta, opts)
    },
    onSuccess: (res) => {
      if (res.skipped) {
        toast.info(`Skipped: ${res.reason ?? 'duplicate'}`)
      } else {
        toast.success('Memory stored')
        qc.invalidateQueries({ queryKey: ['memories'] })
        qc.invalidateQueries({ queryKey: ['memory-namespaces'] })
      }
      setAddText('')
      setAddMeta('')
      setAddTtlDays('')
      setAddNever(false)
      setShowAdd(false)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const delMut = useMutation({
    mutationFn: (id: string) => deleteMemory(id),
    onSuccess: (res) => {
      toast.success('Memory deleted')
      setHits((prev) => prev.filter((h) => h.id !== res.deleted))
      qc.invalidateQueries({ queryKey: ['memories'] })
      qc.invalidateQueries({ queryKey: ['memory-namespaces'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const totalAll = namespaces.reduce((n, x) => n + x.count, 0)

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <Database size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Vector Memory</span>
        <div className="flex-1" />
        <div className="relative">
          <select
            value={nsFilter}
            onChange={(e) => setNsFilter(e.target.value)}
            title="Namespace filter"
            className="appearance-none rounded-lg border border-surface-border bg-surface-overlay py-1.5 pl-3 pr-7 text-xs text-gray-300 outline-none focus:border-accent/50 cursor-pointer"
          >
            <option value={ALL_NS}>All namespaces{namespaces.length ? ` (${totalAll})` : ''}</option>
            {!namespaces.some((n) => n.namespace === '') && <option value="">(shared)</option>}
            {namespaces.map((n) => (
              <option key={n.namespace} value={n.namespace}>
                {n.namespace === '' ? '(shared)' : n.namespace} ({n.count})
              </option>
            ))}
          </select>
          <ChevronDown size={12} className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-gray-600" />
        </div>
        <label className="flex items-center gap-1 text-[11px] text-gray-500 cursor-pointer" title="Include embedding vectors in the export (much larger file)">
          <input type="checkbox" checked={withEmb} onChange={(e) => setWithEmb(e.target.checked)} className="accent-indigo-500" />
          embeddings
        </label>
        <button
          onClick={handleExport}
          disabled={transferring !== null}
          title="Export all memories as JSONL"
          className="flex items-center gap-1.5 rounded-lg bg-surface-overlay px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 transition-colors disabled:opacity-40"
        >
          {transferring === 'export' ? <Loader2 size={12} className="animate-spin" /> : <Download size={12} />}
          Export
        </button>
        <button
          onClick={() => fileRef.current?.click()}
          disabled={transferring !== null}
          title="Import memories from a JSONL export"
          className="flex items-center gap-1.5 rounded-lg bg-surface-overlay px-3 py-1.5 text-xs font-medium text-gray-400 hover:text-gray-200 transition-colors disabled:opacity-40"
        >
          {transferring === 'import' ? <Loader2 size={12} className="animate-spin" /> : <Upload size={12} />}
          Import
        </button>
        <input
          ref={fileRef}
          type="file"
          accept=".jsonl,.ndjson,.json,application/x-ndjson,application/json"
          className="hidden"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) handleImport(f) }}
        />
        <button
          onClick={() => setBrowse((b) => !b)}
          className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors ${
            browse ? 'bg-accent/30 text-accent' : 'bg-surface-overlay text-gray-400 hover:text-gray-200'
          }`}
        >
          <List size={12} />
          {browse ? 'Hide all' : 'Browse all'}
        </button>
        <button
          onClick={() => setShowAdd((s) => !s)}
          className="flex items-center gap-1.5 rounded-lg bg-accent/20 px-3 py-1.5 text-xs font-medium text-accent hover:bg-accent/30 transition-colors"
        >
          {showAdd ? <X size={12} /> : <Plus size={12} />}
          {showAdd ? 'Cancel' : 'Add memory'}
        </button>
      </header>

      <div className="flex-1 overflow-y-auto p-5 space-y-4">
        {/* Add form */}
        {showAdd && (
          <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4 space-y-3 animate-slide-up">
            <h3 className="text-xs font-semibold uppercase tracking-widest text-gray-500">Store new memory</h3>
            <textarea
              rows={3}
              value={addText}
              onChange={(e) => setAddText(e.target.value)}
              placeholder="Memory text…"
              className="w-full rounded-xl border border-surface-border bg-surface px-3 py-2 text-sm text-gray-200 placeholder-gray-600 outline-none focus:border-accent/50 resize-none"
            />
            <textarea
              rows={2}
              value={addMeta}
              onChange={(e) => setAddMeta(e.target.value)}
              placeholder='Optional metadata JSON, e.g. {"tag":"work"}'
              className="w-full rounded-xl border border-surface-border bg-surface px-3 py-2 font-mono text-xs text-gray-400 placeholder-gray-600 outline-none focus:border-accent/50 resize-none"
            />
            <div className="flex flex-wrap items-center gap-3">
              <input
                value={addNs}
                onChange={(e) => setAddNs(e.target.value)}
                placeholder="Namespace (blank = shared)"
                list="memory-namespaces"
                className="w-56 rounded-xl border border-surface-border bg-surface px-3 py-2 font-mono text-xs text-gray-300 placeholder-gray-600 outline-none focus:border-accent/50"
              />
              <datalist id="memory-namespaces">
                {namespaces.filter((n) => n.namespace).map((n) => <option key={n.namespace} value={n.namespace} />)}
              </datalist>
              <div className="flex items-center gap-1.5 text-xs text-gray-500">
                <span>Expires in</span>
                <input
                  type="number"
                  min={0}
                  step="any"
                  value={addTtlDays}
                  disabled={addNever}
                  onChange={(e) => setAddTtlDays(e.target.value)}
                  placeholder="default"
                  className="w-20 rounded-xl border border-surface-border bg-surface px-2 py-2 text-xs text-gray-300 placeholder-gray-600 outline-none focus:border-accent/50 disabled:opacity-40"
                />
                <span>days</span>
              </div>
              <label className="flex items-center gap-1.5 text-xs text-gray-500 cursor-pointer">
                <input type="checkbox" checked={addNever} onChange={(e) => setAddNever(e.target.checked)} className="accent-indigo-500" />
                never expire
              </label>
            </div>
            <button
              onClick={() => addMut.mutate()}
              disabled={!addText.trim() || addMut.isPending}
              className="flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-xs font-semibold text-white hover:bg-accent-hover disabled:opacity-40 transition-colors"
            >
              {addMut.isPending && <Loader2 size={12} className="animate-spin" />}
              Store
            </button>
          </div>
        )}

        {/* Browse-all list */}
        {browse && (
          <div className="space-y-2">
            {browseQuery.isLoading && (
              <div className="flex items-center gap-2 text-sm text-gray-500 py-6 justify-center">
                <Loader2 size={14} className="animate-spin" /> Loading memories…
              </div>
            )}
            {browseQuery.data && (
              <>
                <p className="text-xs text-gray-600">
                  {browseQuery.data.total} stored memor{browseQuery.data.total !== 1 ? 'ies' : 'y'}
                  {ns !== undefined && ` in ${ns === '' ? '(shared)' : ns}`}
                  {browseQuery.data.hits.length < browseQuery.data.total &&
                    ` (showing ${browseQuery.data.hits.length})`}
                </p>
                {browseQuery.data.hits.map((h) => (
                  <MemoryCard
                    key={h.id}
                    h={h}
                    onDelete={(id) => delMut.mutate(id)}
                    onEdit={setEditing}
                    deleting={delMut.isPending && delMut.variables === h.id}
                  />
                ))}
                {browseQuery.data.total === 0 && (
                  <p className="text-center text-sm text-gray-600 py-8">No memories stored yet</p>
                )}
              </>
            )}
          </div>
        )}

        {/* Search bar */}
        {!browse && (
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-600" />
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && searchMut.mutate()}
                placeholder="Semantic search…"
                className="w-full rounded-xl border border-surface-border bg-surface-overlay py-2 pl-9 pr-3 text-sm text-gray-200 placeholder-gray-600 outline-none focus:border-accent/50"
              />
            </div>
            <div className="flex items-center gap-1.5 rounded-xl border border-surface-border bg-surface-overlay px-3 text-xs text-gray-500">
              <span>k =</span>
              <input
                type="number"
                min={1}
                max={50}
                value={k}
                onChange={(e) => setK(Number(e.target.value))}
                className="w-10 bg-transparent text-gray-300 outline-none text-center"
              />
            </div>
            <button
              onClick={() => searchMut.mutate()}
              disabled={!query.trim() || searchMut.isPending}
              className="flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-xs font-semibold text-white hover:bg-accent-hover disabled:opacity-40 transition-colors"
            >
              {searchMut.isPending ? <Loader2 size={12} className="animate-spin" /> : <Search size={12} />}
              Search
            </button>
          </div>
        )}

        {/* Search results */}
        {!browse && hits.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs text-gray-600">{hits.length} result{hits.length !== 1 ? 's' : ''}</p>
            {hits.map((h) => (
              <MemoryCard
                key={h.id}
                h={h}
                onDelete={(id) => delMut.mutate(id)}
                onEdit={setEditing}
                deleting={delMut.isPending && delMut.variables === h.id}
              />
            ))}
          </div>
        )}

        {!browse && hits.length === 0 && !searchMut.isPending && query && searchMut.isSuccess && (
          <p className="text-center text-sm text-gray-600 py-8">No memories found for "{query}"</p>
        )}

        {!browse && !query && hits.length === 0 && !showAdd && (
          <div className="flex flex-col items-center justify-center py-20 gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-accent/10 text-accent">
              <Database size={22} />
            </div>
            <p className="text-sm text-gray-500">Search the vector memory store</p>
            <p className="text-xs text-gray-700 max-w-xs">
              Memories are stored automatically when the agent uses <code className="font-mono text-indigo-400">vector_add</code>, or you can add them manually.
            </p>
          </div>
        )}
      </div>

      {editing && <MemoryEditModal hit={editing} onClose={() => setEditing(null)} onSaved={onSaved} />}
    </div>
  )
}
