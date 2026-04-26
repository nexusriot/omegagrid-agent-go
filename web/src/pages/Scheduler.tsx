import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Clock, Plus, Trash2, Play, Pause, Loader2, X, ChevronDown, CheckCircle2,
  XCircle, Calendar,
} from 'lucide-react'
import { toast } from 'sonner'
import { fetchTasks, createTask, deleteTask, enableTask, disableTask, fetchSkills } from '../api/client'
import type { SchedulerTask, CreateTaskRequest } from '../api/types'

function cronFieldMatches(field: string, value: number, lo: number): boolean {
  for (const part of field.split(',')) {
    const trimmed = part.trim()
    let step = 1
    let base = trimmed
    const slashIdx = trimmed.indexOf('/')
    if (slashIdx >= 0) {
      step = parseInt(trimmed.slice(slashIdx + 1), 10)
      if (isNaN(step) || step <= 0) continue
      base = trimmed.slice(0, slashIdx)
    }
    if (base === '*') {
      if ((value - lo) % step === 0) return true
      continue
    }
    const dashIdx = base.indexOf('-')
    if (dashIdx >= 0) {
      const lo2 = parseInt(base.slice(0, dashIdx), 10)
      const hi2 = parseInt(base.slice(dashIdx + 1), 10)
      if (!isNaN(lo2) && !isNaN(hi2) && value >= lo2 && value <= hi2 && (value - lo2) % step === 0) return true
      continue
    }
    const n = parseInt(base, 10)
    if (!isNaN(n) && n === value && step === 1) return true
  }
  return false
}

function cronMatches(parts: string[], d: Date): boolean {
  return (
    cronFieldMatches(parts[0], d.getMinutes(), 0) &&
    cronFieldMatches(parts[1], d.getHours(), 0) &&
    cronFieldMatches(parts[2], d.getDate(), 1) &&
    cronFieldMatches(parts[3], d.getMonth() + 1, 1) &&
    cronFieldMatches(parts[4], d.getDay(), 0)
  )
}

function nextCronFires(expr: string, count: number): Date[] | null {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return null
  const results: Date[] = []
  const d = new Date()
  d.setSeconds(0, 0)
  d.setMinutes(d.getMinutes() + 1)
  let attempts = 0
  while (results.length < count && attempts < 60 * 24 * 400) {
    attempts++
    if (cronMatches(parts, d)) results.push(new Date(d))
    d.setMinutes(d.getMinutes() + 1)
  }
  return results.length === count ? results : null
}

function fmtTs(ts: number | null): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

function StatusBadge({ enabled }: { enabled: boolean }) {
  return enabled
    ? <span className="flex items-center gap-1 text-[11px] text-emerald-400"><CheckCircle2 size={10} /> enabled</span>
    : <span className="flex items-center gap-1 text-[11px] text-gray-600"><XCircle size={10} /> disabled</span>
}

function CreateModal({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const { data: skills = [] } = useQuery({ queryKey: ['skills'], queryFn: fetchSkills, staleTime: 30_000 })

  const [form, setForm] = useState<CreateTaskRequest>({
    name: '', cron_expr: '0 9 * * *', skill: '', args: {},
  })
  const [argsRaw, setArgsRaw] = useState('{}')
  const [nextRuns, setNextRuns] = useState<Date[]>(() => nextCronFires('0 9 * * *', 3) ?? [])

  useEffect(() => {
    setNextRuns(nextCronFires(form.cron_expr, 3) ?? [])
  }, [form.cron_expr])

  const mut = useMutation({
    mutationFn: () => {
      let args: Record<string, unknown> = {}
      try { args = JSON.parse(argsRaw) } catch { toast.error('Invalid JSON in args'); throw new Error('invalid json') }
      return createTask({ ...form, args })
    },
    onSuccess: () => {
      toast.success('Task created')
      qc.invalidateQueries({ queryKey: ['tasks'] })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const field = (key: keyof CreateTaskRequest) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setForm((f) => ({ ...f, [key]: e.target.value }))

  const inputCls = 'w-full rounded-xl border border-surface-border bg-surface px-3 py-2 text-sm text-gray-200 placeholder-gray-600 outline-none focus:border-accent/50'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
      <div className="w-full max-w-md rounded-2xl border border-surface-border bg-surface-raised p-6 shadow-2xl animate-slide-up">
        <div className="flex items-center justify-between mb-5">
          <h2 className="text-sm font-semibold text-gray-200">New scheduled task</h2>
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300"><X size={16} /></button>
        </div>

        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs text-gray-500">Name</label>
            <input value={form.name} onChange={field('name')} placeholder="my-task" className={inputCls} />
          </div>
          <div>
            <label className="mb-1 block text-xs text-gray-500">Cron expression</label>
            <input value={form.cron_expr} onChange={field('cron_expr')} placeholder="0 9 * * *" className={`${inputCls} font-mono`} />
            <p className="mt-1 text-[10px] text-gray-600">minute hour day month weekday</p>
            {nextRuns.length > 0 && (
              <div className="mt-1.5 space-y-0.5">
                {nextRuns.map((d, i) => (
                  <div key={i} className="flex items-center gap-1.5 text-[10px] text-emerald-500/70">
                    <span className="text-gray-600">{i === 0 ? 'Next:' : '     '}</span>
                    <span>{d.toLocaleString()}</span>
                  </div>
                ))}
              </div>
            )}
            {nextRuns.length === 0 && form.cron_expr.trim() !== '' && (
              <p className="mt-1 text-[10px] text-red-400/70">No fires found — check expression</p>
            )}
          </div>
          <div>
            <label className="mb-1 block text-xs text-gray-500">Skill</label>
            <div className="relative">
              <select
                value={form.skill}
                onChange={field('skill')}
                className={`${inputCls} appearance-none pr-8 cursor-pointer`}
              >
                <option value="">Choose a skill…</option>
                {skills.map((s) => (
                  <option key={s.name} value={s.name}>{s.name}</option>
                ))}
              </select>
              <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-600" />
            </div>
          </div>
          <div>
            <label className="mb-1 block text-xs text-gray-500">Args (JSON)</label>
            <textarea
              rows={3}
              value={argsRaw}
              onChange={(e) => setArgsRaw(e.target.value)}
              className={`${inputCls} font-mono text-xs resize-none`}
            />
          </div>
        </div>

        <div className="mt-5 flex justify-end gap-2">
          <button onClick={onClose} className="rounded-xl px-4 py-2 text-sm text-gray-500 hover:text-gray-300 transition-colors">
            Cancel
          </button>
          <button
            onClick={() => mut.mutate()}
            disabled={!form.name || !form.cron_expr || !form.skill || mut.isPending}
            className="flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-sm font-semibold text-white hover:bg-accent-hover disabled:opacity-40 transition-colors"
          >
            {mut.isPending && <Loader2 size={13} className="animate-spin" />}
            Create
          </button>
        </div>
      </div>
    </div>
  )
}

function TaskRow({ task }: { task: SchedulerTask }) {
  const qc = useQueryClient()

  const toggleMut = useMutation({
    mutationFn: () => (task.enabled ? disableTask(task.id) : enableTask(task.id)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tasks'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: () => deleteTask(task.id),
    onSuccess: () => { toast.success('Task deleted'); qc.invalidateQueries({ queryKey: ['tasks'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4 animate-fade-in">
      <div className="flex items-start justify-between gap-3 mb-3">
        <div>
          <div className="font-medium text-sm text-gray-200">{task.name}</div>
          <div className="flex items-center gap-3 mt-1">
            <code className="rounded bg-surface px-2 py-0.5 font-mono text-[11px] text-indigo-300">{task.cron_expr}</code>
            <code className="font-mono text-[11px] text-gray-500">{task.skill}</code>
            <StatusBadge enabled={task.enabled} />
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            onClick={() => toggleMut.mutate()}
            disabled={toggleMut.isPending}
            title={task.enabled ? 'Disable' : 'Enable'}
            className="flex h-7 w-7 items-center justify-center rounded-lg text-gray-500 hover:bg-surface-border hover:text-gray-300 transition-colors disabled:opacity-40"
          >
            {task.enabled ? <Pause size={13} /> : <Play size={13} />}
          </button>
          <button
            onClick={() => { if (confirm('Delete this task?')) deleteMut.mutate() }}
            disabled={deleteMut.isPending}
            title="Delete"
            className="flex h-7 w-7 items-center justify-center rounded-lg text-gray-600 hover:bg-red-950/40 hover:text-red-400 transition-colors disabled:opacity-40"
          >
            <Trash2 size={13} />
          </button>
        </div>
      </div>

      <div className="grid grid-cols-3 gap-3 text-[11px] text-gray-600">
        <div>
          <div className="text-[10px] uppercase tracking-widest mb-0.5">Last run</div>
          <div className="text-gray-400">{fmtTs(task.last_run_at)}</div>
        </div>
        <div>
          <div className="text-[10px] uppercase tracking-widest mb-0.5">Run count</div>
          <div className="text-gray-400">{task.run_count}</div>
        </div>
        <div>
          <div className="text-[10px] uppercase tracking-widest mb-0.5">Created</div>
          <div className="text-gray-400">{fmtTs(task.created_at)}</div>
        </div>
      </div>

      {task.last_result && (
        <div className="mt-3 rounded-lg bg-surface p-2 font-mono text-[10px] text-gray-500 max-h-20 overflow-y-auto">
          {task.last_result}
        </div>
      )}
    </div>
  )
}

export default function Scheduler() {
  const [showCreate, setShowCreate] = useState(false)
  const { data: tasks = [], isLoading, error } = useQuery({
    queryKey: ['tasks'],
    queryFn: fetchTasks,
    refetchInterval: 30_000,
  })

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-12 items-center gap-3 border-b border-surface-border px-5 shrink-0">
        <Clock size={16} className="text-accent" />
        <span className="text-sm font-semibold text-gray-300">Scheduler</span>
        <div className="flex-1" />
        {!isLoading && (
          <span className="text-xs text-gray-600">{tasks.length} task{tasks.length !== 1 ? 's' : ''}</span>
        )}
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-1.5 rounded-lg bg-accent/20 px-3 py-1.5 text-xs font-medium text-accent hover:bg-accent/30 transition-colors"
        >
          <Plus size={12} /> New task
        </button>
      </header>

      <div className="flex-1 overflow-y-auto p-5 space-y-3">
        {isLoading && (
          <div className="flex items-center justify-center py-20 text-gray-600">
            <Loader2 size={20} className="animate-spin" />
          </div>
        )}

        {error && (
          <div className="rounded-2xl border border-red-900/50 bg-red-950/20 p-4 text-sm text-red-400">
            Failed to load tasks: {(error as Error).message}
          </div>
        )}

        {tasks.map((t) => <TaskRow key={t.id} task={t} />)}

        {!isLoading && !error && tasks.length === 0 && (
          <div className="flex flex-col items-center justify-center py-20 gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-accent/10 text-accent">
              <Calendar size={22} />
            </div>
            <p className="text-sm text-gray-500">No scheduled tasks</p>
            <p className="text-xs text-gray-700 max-w-xs">
              Create tasks to run skills on a cron schedule. Results can be sent to Telegram.
            </p>
            <button
              onClick={() => setShowCreate(true)}
              className="mt-2 flex items-center gap-2 rounded-xl bg-accent px-4 py-2 text-xs font-semibold text-white hover:bg-accent-hover transition-colors"
            >
              <Plus size={13} /> Create first task
            </button>
          </div>
        )}
      </div>

      {showCreate && <CreateModal onClose={() => setShowCreate(false)} />}
    </div>
  )
}
