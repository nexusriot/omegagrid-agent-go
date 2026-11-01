import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2, X } from 'lucide-react'
import { toast } from 'sonner'
import { updateMemory } from '../api/client'
import type { MemoryHit, MemoryUpdateRequest } from '../api/types'
import { fmtDateTime, fmtRelativeFuture } from '../lib/time'

// Keys the server maintains itself; expires_at is edited through the expiry
// control instead of the JSON.
const READ_ONLY_KEYS = new Set(['hash', 'created_at', 'updated_at', 'namespace', 'expires_at'])

type Expiry = 'keep' | 'set' | 'never'

const inputBase = 'w-full rounded-xl border bg-surface px-3 py-2 text-sm text-gray-200 placeholder-gray-600 outline-none'
const inputCls = `${inputBase} border-surface-border focus:border-accent/50`
const invalidCls = `${inputBase} border-red-800/70 focus:border-red-700`

function editableMeta(meta: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(meta)) if (!READ_ONLY_KEYS.has(k)) out[k] = v
  return out
}

/** Builds the meta patch: removed keys → null, changed/new keys → string. */
function metaPatch(before: Record<string, unknown>, after: Record<string, unknown>): Record<string, string | null> {
  const patch: Record<string, string | null> = {}
  for (const k of Object.keys(before)) if (!(k in after)) patch[k] = null
  for (const [k, v] of Object.entries(after)) {
    if (READ_ONLY_KEYS.has(k)) continue
    const s = v === null ? null : typeof v === 'string' ? v : JSON.stringify(v)
    if (s === null) {
      if (k in before) patch[k] = null
    } else if (before[k] !== s) {
      patch[k] = s
    }
  }
  return patch
}

export default function MemoryEditModal({ hit, onClose, onSaved }: {
  hit: MemoryHit
  onClose: () => void
  onSaved: (hit: MemoryHit) => void
}) {
  const qc = useQueryClient()
  const original = editableMeta(hit.metadata)
  const [text, setText] = useState(hit.text)
  const [metaRaw, setMetaRaw] = useState(() => JSON.stringify(original, null, 2))
  const [expiry, setExpiry] = useState<Expiry>('keep')
  const [days, setDays] = useState(30)

  let metaErr = ''
  let parsedMeta: Record<string, unknown> = {}
  try {
    const v: unknown = JSON.parse(metaRaw.trim() || '{}')
    if (v === null || typeof v !== 'object' || Array.isArray(v)) metaErr = 'Metadata must be a JSON object'
    else parsedMeta = v as Record<string, unknown>
  } catch (e) {
    metaErr = (e as Error).message
  }

  const expiresAt = parseFloat(String(hit.metadata.expires_at ?? ''))
  const hasExpiry = !isNaN(expiresAt)

  const mut = useMutation({
    mutationFn: () => {
      const req: MemoryUpdateRequest = {}
      if (text.trim() !== hit.text) req.text = text.trim()
      const patch = metaPatch(original, parsedMeta)
      if (Object.keys(patch).length > 0) req.meta = patch
      if (expiry === 'set') req.ttl_seconds = days * 86400
      if (expiry === 'never') req.ttl_seconds = 0
      return updateMemory(hit.id, req)
    },
    onSuccess: (res) => {
      toast.success('Memory updated')
      qc.invalidateQueries({ queryKey: ['memories'] })
      qc.invalidateQueries({ queryKey: ['memory-namespaces'] })
      onSaved(res.hit)
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const canSave = text.trim() !== '' && !metaErr && (expiry !== 'set' || days > 0) && !mut.isPending

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
      <div className="w-full max-w-lg max-h-full overflow-y-auto rounded-2xl border border-surface-border bg-surface-raised p-6 shadow-2xl animate-slide-up">
        <div className="flex items-center justify-between mb-5">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-gray-200">Edit memory</h2>
            <p className="font-mono text-[10px] text-gray-600 truncate">{hit.id}</p>
          </div>
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300"><X size={16} /></button>
        </div>

        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs text-gray-500">Text</label>
            <textarea rows={5} value={text} onChange={(e) => setText(e.target.value)} className={`${inputCls} resize-y`} />
          </div>
          <div>
            <label className="mb-1 block text-xs text-gray-500">
              Metadata (JSON) <span className="text-gray-700">— hash, timestamps, namespace and expiry are managed separately</span>
            </label>
            <textarea
              rows={5}
              value={metaRaw}
              onChange={(e) => setMetaRaw(e.target.value)}
              spellCheck={false}
              className={`${metaErr ? invalidCls : inputCls} font-mono text-xs resize-y`}
            />
            {metaErr && <p className="mt-1 text-[10px] text-red-400">{metaErr}</p>}
          </div>
          <div>
            <label className="mb-1 block text-xs text-gray-500">
              Expiry{' '}
              <span className="text-gray-700">
                — {hasExpiry ? `currently ${fmtRelativeFuture(expiresAt)} (${fmtDateTime(expiresAt)})` : 'currently never'}
              </span>
            </label>
            <div className="flex flex-wrap items-center gap-3 text-xs text-gray-400">
              {(['keep', 'set', 'never'] as const).map((opt) => (
                <label key={opt} className="flex items-center gap-1.5 cursor-pointer">
                  <input type="radio" name="expiry" checked={expiry === opt} onChange={() => setExpiry(opt)} className="accent-indigo-500" />
                  {opt === 'keep' ? 'Keep' : opt === 'set' ? 'Expire in' : 'Never expire'}
                  {opt === 'set' && (
                    <>
                      <input
                        type="number" min={1}
                        value={days}
                        onChange={(e) => { setDays(Math.max(0, Number(e.target.value))); setExpiry('set') }}
                        className="w-16 rounded-lg border border-surface-border bg-surface px-2 py-1 text-xs text-gray-200 outline-none focus:border-accent/50"
                      />
                      days
                    </>
                  )}
                </label>
              ))}
            </div>
          </div>
        </div>

        <div className="mt-5 flex justify-end gap-2">
          <button onClick={onClose} className="rounded-xl px-4 py-2 text-sm text-gray-500 hover:text-gray-300 transition-colors">
            Cancel
          </button>
          <button
            onClick={() => mut.mutate()}
            disabled={!canSave}
            className="flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-sm font-semibold text-white hover:bg-accent-hover disabled:opacity-40 transition-colors"
          >
            {mut.isPending && <Loader2 size={13} className="animate-spin" />}
            Save
          </button>
        </div>
      </div>
    </div>
  )
}
