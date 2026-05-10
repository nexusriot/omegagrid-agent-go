import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Wrench, Search, ChevronDown, ChevronRight, Loader2, FileCode, Play, Copy, Check } from 'lucide-react'
import { fetchSkills, invokeSkill } from '../api/client'
import type { Skill, SkillInvokeResult } from '../api/types'

function ParamBadge({ required }: { required?: boolean }) {
  return required
    ? <span className="rounded bg-red-950/60 px-1.5 py-0.5 text-[10px] text-red-400">required</span>
    : <span className="rounded bg-surface px-1.5 py-0.5 text-[10px] text-gray-600">optional</span>
}

// SkillForm generates input fields from the skill's parameter schema.
function SkillForm({ params, values, onChange }: {
  params: Skill['parameters']
  values: Record<string, string>
  onChange: (k: string, v: string) => void
}) {
  const entries = Object.entries(params ?? {})
  if (entries.length === 0) {
    return <p className="text-xs text-gray-600 italic">No parameters</p>
  }
  return (
    <div className="space-y-2">
      {entries.map(([name, p]) => (
        <div key={name}>
          <div className="flex items-center gap-2 mb-1">
            <label className="font-mono text-[11px] text-indigo-300">{name}</label>
            <span className="text-[10px] text-gray-600 font-mono">{p.type}</span>
            <ParamBadge required={p.required} />
          </div>
          <input
            value={values[name] ?? ''}
            onChange={e => onChange(name, e.target.value)}
            placeholder={p.description}
            className="w-full rounded-lg border border-surface-border bg-surface px-2.5 py-1.5 text-xs text-gray-200 placeholder-gray-600 outline-none focus:border-accent/50 font-mono"
          />
        </div>
      ))}
    </div>
  )
}

// ResultPanel renders invoke output with tabs for Pretty / Raw / Timing.
function ResultPanel({ result }: { result: SkillInvokeResult }) {
  const [tab, setTab] = useState<'pretty' | 'raw' | 'timing'>('pretty')
  const [copied, setCopied] = useState(false)

  const copyResult = () => {
    navigator.clipboard.writeText(JSON.stringify(result.result, null, 2))
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  const tabs: { id: typeof tab; label: string }[] = [
    { id: 'pretty', label: 'Pretty' },
    { id: 'raw', label: 'Raw' },
    { id: 'timing', label: `${result.elapsed_s.toFixed(3)}s` },
  ]

  return (
    <div className="mt-3 rounded-lg border border-surface-border bg-surface overflow-hidden">
      {/* Tab bar */}
      <div className="flex items-center border-b border-surface-border px-2">
        {tabs.map(t => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`px-3 py-1.5 text-[11px] font-mono transition-colors ${
              tab === t.id
                ? 'text-accent border-b border-accent'
                : 'text-gray-600 hover:text-gray-400'
            }`}
          >
            {t.label}
          </button>
        ))}
        <div className="flex-1" />
        <button onClick={copyResult} className="p-1 text-gray-600 hover:text-gray-300 transition-colors">
          {copied ? <Check size={12} className="text-green-400" /> : <Copy size={12} />}
        </button>
      </div>

      <div className="p-3 max-h-64 overflow-y-auto">
        {result.error && (
          <p className="text-xs text-red-400 font-mono">{result.error}</p>
        )}

        {tab === 'pretty' && !result.error && (
          <>
            {result.attachments && result.attachments.length > 0 && (
              <div className="mb-2 space-y-2">
                {result.attachments.map((a, i) => (
                  a.mime_type.startsWith('image/') ? (
                    <img
                      key={i}
                      src={`data:${a.mime_type};base64,${a.base64}`}
                      alt={a.filename}
                      className="max-w-full rounded border border-surface-border"
                    />
                  ) : (
                    <p key={i} className="text-xs text-gray-400">{a.filename}</p>
                  )
                ))}
              </div>
            )}
            <pre className="text-[11px] text-gray-300 whitespace-pre-wrap font-mono">
              {JSON.stringify(result.result, null, 2)}
            </pre>
          </>
        )}

        {tab === 'raw' && (
          <pre className="text-[11px] text-gray-400 whitespace-pre-wrap font-mono">
            {JSON.stringify(result, null, 2)}
          </pre>
        )}

        {tab === 'timing' && (
          <div className="text-[11px] text-gray-400 font-mono space-y-1">
            <div>elapsed: <span className="text-accent">{result.elapsed_s.toFixed(4)}s</span></div>
            <div>skill: <span className="text-indigo-300">{result.name}</span></div>
          </div>
        )}
      </div>
    </div>
  )
}

function SkillCard({ skill }: { skill: Skill }) {
  const [open, setOpen] = useState(false)
  const [playOpen, setPlayOpen] = useState(false)
  const [formValues, setFormValues] = useState<Record<string, string>>({})
  const [invokeResult, setInvokeResult] = useState<SkillInvokeResult | null>(null)
  const [invoking, setInvoking] = useState(false)
  const params = Object.entries(skill.parameters ?? {})

  const handleInvoke = async () => {
    setInvoking(true)
    setInvokeResult(null)
    try {
      const args: Record<string, unknown> = {}
      for (const [k, v] of Object.entries(formValues)) {
        if (v !== '') args[k] = v
      }
      const res = await invokeSkill(skill.name, args)
      setInvokeResult(res)
    } catch (e) {
      setInvokeResult({
        name: skill.name,
        args: formValues,
        result: null,
        elapsed_s: 0,
        error: (e as Error).message,
        attachments: null,
      })
    } finally {
      setInvoking(false)
    }
  }

  const copyToolCall = () => {
    const args: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(formValues)) {
      if (v !== '') args[k] = v
    }
    navigator.clipboard.writeText(JSON.stringify({
      type: 'tool_call',
      tool: skill.name,
      args,
      why: 'manual invoke',
    }, null, 2))
  }

  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay overflow-hidden animate-fade-in">
      {/* Header row */}
      <div className="flex items-center">
        <button
          onClick={() => setOpen(o => !o)}
          className="flex flex-1 items-center gap-3 px-4 py-3 text-left hover:bg-white/5 transition-colors"
        >
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent/15 text-accent">
            <Wrench size={14} />
          </div>
          <div className="flex-1 min-w-0">
            <div className="font-mono text-sm font-semibold text-indigo-300">{skill.name}</div>
            <div className="truncate text-xs text-gray-500">{skill.description}</div>
          </div>
          <span className="shrink-0 text-[10px] text-gray-600">{params.length} param{params.length !== 1 ? 's' : ''}</span>
          {open ? <ChevronDown size={14} className="shrink-0 text-gray-600" /> : <ChevronRight size={14} className="shrink-0 text-gray-600" />}
        </button>

        {/* Run button */}
        <button
          onClick={() => { setPlayOpen(p => !p); setOpen(true) }}
          title="Open skill playground"
          className={`shrink-0 mx-2 flex h-7 w-7 items-center justify-center rounded-lg transition-colors ${
            playOpen ? 'bg-accent text-white' : 'bg-accent/10 text-accent hover:bg-accent/20'
          }`}
        >
          <Play size={12} />
        </button>
      </div>

      {/* Schema detail */}
      {open && (
        <div className="border-t border-surface-border px-4 py-3 space-y-3 text-xs">
          <p className="text-gray-400 leading-relaxed">{skill.description}</p>

          {params.length > 0 && (
            <div>
              <div className="mb-2 text-[10px] uppercase tracking-widest text-gray-600">Parameters</div>
              <div className="space-y-2">
                {params.map(([name, p]) => (
                  <div key={name} className="rounded-lg border border-surface-border bg-surface p-2.5">
                    <div className="flex items-center gap-2 mb-1">
                      <code className="font-mono text-indigo-300 text-[12px]">{name}</code>
                      <span className="rounded bg-surface-overlay px-1.5 py-0.5 text-[10px] text-gray-500 font-mono">{p.type}</span>
                      <ParamBadge required={p.required} />
                    </div>
                    <p className="text-gray-500">{p.description}</p>
                  </div>
                ))}
              </div>
            </div>
          )}

          {skill.body && (
            <div>
              <div className="mb-1 text-[10px] uppercase tracking-widest text-gray-600">Body</div>
              <pre className="overflow-x-auto rounded-lg bg-surface p-3 font-mono text-[11px] text-gray-400 max-h-48">
                {skill.body}
              </pre>
            </div>
          )}
        </div>
      )}

      {/* Playground panel */}
      {playOpen && (
        <div className="border-t border-surface-border bg-surface px-4 py-4 space-y-3">
          <div className="flex items-center gap-2 mb-1">
            <Play size={12} className="text-accent" />
            <span className="text-[10px] uppercase tracking-widest text-gray-500">Playground</span>
            <button
              onClick={copyToolCall}
              title="Copy as tool_call JSON"
              className="ml-auto flex items-center gap-1 text-[10px] text-gray-600 hover:text-gray-300 transition-colors"
            >
              <Copy size={10} /> tool_call
            </button>
          </div>

          <SkillForm
            params={skill.parameters}
            values={formValues}
            onChange={(k, v) => setFormValues(prev => ({ ...prev, [k]: v }))}
          />

          <button
            onClick={handleInvoke}
            disabled={invoking}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-accent/20 hover:bg-accent/30 disabled:opacity-50 px-3 py-2 text-xs text-accent font-medium transition-colors"
          >
            {invoking
              ? <><Loader2 size={12} className="animate-spin" /> Running…</>
              : <><Play size={12} /> Invoke</>
            }
          </button>

          {invokeResult && <ResultPanel result={invokeResult} />}
        </div>
      )}
    </div>
  )
}

export default function Skills() {
  const [search, setSearch] = useState('')
  const { data: skills = [], isLoading, error } = useQuery({
    queryKey: ['skills'],
    queryFn: fetchSkills,
    staleTime: 30_000,
  })

  const filtered = skills.filter(s =>
    !search || s.name.includes(search) || s.description.toLowerCase().includes(search.toLowerCase()),
  )

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <Wrench size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Skills</span>
        <div className="flex-1" />
        {!isLoading && (
          <span className="text-xs text-gray-600">{skills.length} registered</span>
        )}
      </header>

      <div className="flex-1 overflow-y-auto p-5">
        <div className="relative mb-4">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-600" />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Filter skills…"
            className="w-full rounded-xl border border-surface-border bg-surface-overlay py-2 pl-9 pr-3 text-sm text-gray-200 placeholder-gray-600 outline-none focus:border-accent/50"
          />
        </div>

        {isLoading && (
          <div className="flex items-center justify-center py-20 text-gray-600">
            <Loader2 size={20} className="animate-spin" />
          </div>
        )}

        {error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            Failed to load skills: {(error as Error).message}
          </div>
        )}

        <div className="space-y-2">
          {filtered.map(s => <SkillCard key={s.name} skill={s} />)}
        </div>

        {!isLoading && !error && filtered.length === 0 && (
          <div className="flex flex-col items-center justify-center py-20 gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-accent/10 text-accent">
              <FileCode size={22} />
            </div>
            <p className="text-sm text-gray-500">
              {search ? `No skills matching "${search}"` : 'No skills registered'}
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
