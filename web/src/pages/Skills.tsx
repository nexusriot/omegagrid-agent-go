import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Wrench, Search, ChevronDown, ChevronRight, Loader2, FileCode } from 'lucide-react'
import { fetchSkills } from '../api/client'
import type { Skill } from '../api/types'

function ParamBadge({ required }: { required?: boolean }) {
  return required
    ? <span className="rounded bg-red-950/60 px-1.5 py-0.5 text-[10px] text-red-400">required</span>
    : <span className="rounded bg-surface px-1.5 py-0.5 text-[10px] text-gray-600">optional</span>
}

function SkillCard({ skill }: { skill: Skill }) {
  const [open, setOpen] = useState(false)
  const params = Object.entries(skill.parameters ?? {})

  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay overflow-hidden animate-fade-in">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-white/5 transition-colors"
      >
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent/15 text-accent">
          <Wrench size={14} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="font-mono text-sm font-semibold text-indigo-300">{skill.name}</div>
          <div className="truncate text-xs text-gray-500">{skill.description}</div>
        </div>
        <span className="shrink-0 text-[10px] text-gray-600">{params.length} param{params.length !== 1 ? 's' : ''}</span>
        {open
          ? <ChevronDown size={14} className="shrink-0 text-gray-600" />
          : <ChevronRight size={14} className="shrink-0 text-gray-600" />
        }
      </button>

      {open && (
        <div className="border-t border-surface-border px-4 py-3 space-y-3 text-xs">
          {/* Description */}
          <p className="text-gray-400 leading-relaxed">{skill.description}</p>

          {/* Parameters */}
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

          {/* Markdown body */}
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

  const filtered = skills.filter((s) =>
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
        {/* Search */}
        <div className="relative mb-4">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-600" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
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
          {filtered.map((s) => <SkillCard key={s.name} skill={s} />)}
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
