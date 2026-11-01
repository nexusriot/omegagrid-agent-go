import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, X, Save, Trash2, CheckCircle2, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { fetchSkillSource, validateSkillSource, saveSkillSource, deleteSkill } from '../api/client'
import { useDebounced } from '../lib/useDebounced'
import type { SkillValidateResult } from '../api/types'

const TEMPLATE = `---
name: my_skill
description: One sentence telling the agent when to use this skill.
parameters:
  query:
    type: string
    description: What to look up
    required: true
---
Instructions for the agent go here. Describe step by step how to handle
the request, what to return, and any constraints.
`

function ValidationPane({ data, error, pending }: {
  data: SkillValidateResult | undefined
  error: Error | null
  pending: boolean
}) {

  return (
    <div className="flex h-full flex-col text-xs">
      <div className="mb-2 flex items-center gap-2 text-[10px] uppercase tracking-widest text-gray-600">
        Validation
        {pending && <Loader2 size={10} className="animate-spin" />}
      </div>
      {error && <p className="text-red-400">{error.message}</p>}
      {data && !data.ok && (
        <div className="flex items-start gap-2 rounded-lg border border-red-900/50 bg-red-950/20 p-2.5 text-red-400">
          <XCircle size={13} className="mt-px shrink-0" />
          <span className="font-mono text-[11px] break-words">{data.error}</span>
        </div>
      )}
      {data?.ok && data.skill && (
        <div className="space-y-3">
          <div className="flex items-start gap-2 rounded-lg border border-emerald-900/50 bg-emerald-950/20 p-2.5">
            <CheckCircle2 size={13} className="mt-px shrink-0 text-emerald-400" />
            <div className="min-w-0">
              <div className="font-mono text-sm font-semibold text-indigo-300">{data.skill.name}</div>
              <div className="text-gray-400">{data.skill.description}</div>
            </div>
          </div>
          {Object.keys(data.skill.parameters ?? {}).length > 0 ? (
            <table className="w-full text-left">
              <thead>
                <tr className="text-[10px] uppercase tracking-widest text-gray-600">
                  <th className="pb-1 pr-2 font-normal">Param</th>
                  <th className="pb-1 pr-2 font-normal">Type</th>
                  <th className="pb-1 pr-2 font-normal">Req</th>
                  <th className="pb-1 font-normal">Description</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(data.skill.parameters).map(([n, p]) => (
                  <tr key={n} className="border-t border-surface-border align-top">
                    <td className="py-1 pr-2 font-mono text-indigo-300">{n}</td>
                    <td className="py-1 pr-2 font-mono text-gray-500">{p.type || '—'}</td>
                    <td className="py-1 pr-2">{p.required ? <span className="text-red-400">yes</span> : <span className="text-gray-600">no</span>}</td>
                    <td className="py-1 text-gray-500">{p.description}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className="text-gray-600">No parameters</p>
          )}
        </div>
      )}
    </div>
  )
}

/** Editor for a markdown skill file; `name` undefined creates a new skill. */
export default function SkillEditor({ name, onClose }: { name?: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [content, setContent] = useState<string | null>(name ? null : TEMPLATE)

  const source = useQuery({
    queryKey: ['skill-source', name],
    queryFn: () => fetchSkillSource(name!),
    enabled: !!name,
    staleTime: 0,
  })
  useEffect(() => {
    if (source.data !== undefined && content === null) setContent(source.data)
  }, [source.data, content])

  const debounced = useDebounced(content ?? '', 400)
  const validation = useQuery({
    queryKey: ['skill-validate', debounced],
    queryFn: ({ signal }) => validateSkillSource(debounced, signal),
    enabled: content !== null,
    staleTime: Infinity,
    placeholderData: (prev) => prev,
  })
  const settled = content !== null && debounced === content && !validation.isPlaceholderData
  const valid = settled && validation.data?.ok === true

  const saveMut = useMutation({
    mutationFn: () => saveSkillSource(content ?? ''),
    onSuccess: (res) => {
      toast.success(res.created ? `Skill ${res.skill.name} created` : `Skill ${res.skill.name} saved`)
      qc.invalidateQueries({ queryKey: ['skills'] })
      qc.invalidateQueries({ queryKey: ['skill-source'] })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const delMut = useMutation({
    mutationFn: () => deleteSkill(name!),
    onSuccess: () => {
      toast.success(`Skill ${name} deleted`)
      qc.invalidateQueries({ queryKey: ['skills'] })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
      <div className="flex h-full max-h-[48rem] w-full max-w-5xl flex-col rounded-2xl border border-surface-border bg-surface-raised shadow-2xl animate-slide-up">
        <div className="flex items-center justify-between border-b border-surface-border px-5 py-3">
          <h2 className="text-sm font-semibold text-gray-200">
            {name ? <>Edit skill <code className="font-mono text-indigo-300">{name}</code></> : 'New markdown skill'}
          </h2>
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300"><X size={16} /></button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          <div className="flex min-h-0 flex-1 flex-col p-4">
            {source.isLoading && (
              <div className="flex flex-1 items-center justify-center text-gray-600"><Loader2 size={18} className="animate-spin" /></div>
            )}
            {source.error && (
              <p className="text-sm text-red-400">Failed to load source: {(source.error as Error).message}</p>
            )}
            {content !== null && (
              <textarea
                value={content}
                onChange={(e) => setContent(e.target.value)}
                spellCheck={false}
                className="min-h-[16rem] flex-1 resize-none rounded-xl border border-surface-border bg-surface p-3 font-mono text-xs leading-relaxed text-gray-200 outline-none focus:border-accent/50"
              />
            )}
          </div>
          <div className="min-h-0 overflow-y-auto border-t border-surface-border p-4 md:w-80 md:border-l md:border-t-0">
            {content !== null && (
              <ValidationPane
                data={validation.data}
                error={validation.error}
                pending={validation.isFetching || !settled}
              />
            )}
          </div>
        </div>

        <div className="flex items-center gap-2 border-t border-surface-border px-5 py-3">
          {name && (
            <button
              onClick={() => { if (confirm(`Delete skill "${name}"? This removes its file.`)) delMut.mutate() }}
              disabled={delMut.isPending}
              className="flex items-center gap-1.5 rounded-xl px-3 py-2 text-xs font-medium text-red-400 hover:bg-red-950/40 transition-colors disabled:opacity-40"
            >
              {delMut.isPending ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
              Delete
            </button>
          )}
          <div className="flex-1" />
          <button onClick={onClose} className="rounded-xl px-4 py-2 text-sm text-gray-500 hover:text-gray-300 transition-colors">
            Cancel
          </button>
          <button
            onClick={() => saveMut.mutate()}
            disabled={!valid || saveMut.isPending}
            title={valid ? 'Save skill' : 'Fix validation errors first'}
            className="flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-sm font-semibold text-white hover:bg-accent-hover disabled:opacity-40 transition-colors"
          >
            {saveMut.isPending ? <Loader2 size={13} className="animate-spin" /> : <Save size={13} />}
            Save
          </button>
        </div>
      </div>
    </div>
  )
}
