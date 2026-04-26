import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Search, Plus, Database, Loader2, X } from 'lucide-react'
import { toast } from 'sonner'
import { searchMemory, addMemory } from '../api/client'
import type { MemoryHit } from '../api/types'

function DistanceBadge({ d }: { d: number }) {
  const pct = Math.round(d * 100)
  const color = d < 0.3 ? 'text-emerald-400' : d < 0.6 ? 'text-yellow-400' : 'text-red-400'
  return (
    <span className={`text-[10px] font-mono ${color}`}>
      {pct}% dist
    </span>
  )
}

export default function Memory() {
  const [query, setQuery]   = useState('')
  const [k, setK]           = useState(10)
  const [hits, setHits]     = useState<MemoryHit[]>([])
  const [addText, setAddText] = useState('')
  const [addMeta, setAddMeta] = useState('')
  const [showAdd, setShowAdd] = useState(false)

  const searchMut = useMutation({
    mutationFn: () => searchMemory(query, k),
    onSuccess: setHits,
    onError: (e: Error) => toast.error(e.message),
  })

  const addMut = useMutation({
    mutationFn: () => {
      let meta: Record<string, unknown> = {}
      try { if (addMeta.trim()) meta = JSON.parse(addMeta) } catch { /* ignore */ }
      return addMemory(addText, meta)
    },
    onSuccess: (res) => {
      if (res.skipped) {
        toast.info(`Skipped: ${res.reason ?? 'duplicate'}`)
      } else {
        toast.success('Memory stored')
      }
      setAddText('')
      setAddMeta('')
      setShowAdd(false)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <Database size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Vector Memory</span>
        <div className="flex-1" />
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

        {/* Search bar */}
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

        {/* Results */}
        {hits.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs text-gray-600">{hits.length} result{hits.length !== 1 ? 's' : ''}</p>
            {hits.map((h) => (
              <div
                key={h.id}
                className="rounded-2xl border border-surface-border bg-surface-overlay p-4 animate-fade-in"
              >
                <div className="flex items-start justify-between gap-3 mb-2">
                  <p className="text-sm text-gray-200 leading-relaxed">{h.text}</p>
                  <DistanceBadge d={h.distance} />
                </div>
                {Object.keys(h.metadata).length > 0 && (
                  <pre className="mt-2 rounded-lg bg-surface p-2 font-mono text-[11px] text-gray-500 overflow-x-auto">
                    {JSON.stringify(h.metadata, null, 2)}
                  </pre>
                )}
                <p className="mt-1 font-mono text-[10px] text-gray-700 truncate">{h.id}</p>
              </div>
            ))}
          </div>
        )}

        {hits.length === 0 && !searchMut.isPending && query && searchMut.isSuccess && (
          <p className="text-center text-sm text-gray-600 py-8">No memories found for "{query}"</p>
        )}

        {!query && hits.length === 0 && !showAdd && (
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
    </div>
  )
}
