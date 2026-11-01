import { useMemo, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2, X, ChevronDown, Wrench, Bot } from 'lucide-react'
import { toast } from 'sonner'
import { createTask, fetchSkills } from '../api/client'
import type { CreateTaskRequest } from '../api/types'
import {
  nextCronFires, fmtZonedHM, isValidTimezone, browserTimezone, COMMON_TIMEZONES,
} from '../lib/cron'

type Mode = 'skill' | 'agent'

const inputBase = 'w-full rounded-xl border bg-surface px-3 py-2 text-sm text-gray-200 placeholder-gray-600 outline-none'
const inputCls = `${inputBase} border-surface-border focus:border-accent/50`
const invalidCls = `${inputBase} border-red-800/70 focus:border-red-700`
const labelCls = 'mb-1 block text-xs text-gray-500'

export function CronPreview({ expr, tz, count = 3 }: { expr: string; tz: string; count?: number }) {
  const nextRuns = useMemo(() => nextCronFires(expr, count, tz) ?? [], [expr, tz, count])
  if (expr.trim() === '') return null
  if (nextRuns.length === 0) {
    return <p className="mt-1 text-[10px] text-red-400/70">No fires found — check expression{tz ? ' and timezone' : ''}</p>
  }
  return (
    <div className="mt-1.5 space-y-0.5">
      {nextRuns.map((d, i) => (
        <div key={i} className="flex items-center gap-1.5 text-[10px] text-emerald-500/70">
          <span className="text-gray-600">{i === 0 ? 'Next:' : '     '}</span>
          <span>
            {d.toLocaleString()}{' '}
            <span className="text-gray-600">({fmtZonedHM(d, tz)} {tz || 'UTC'})</span>
          </span>
        </div>
      ))}
    </div>
  )
}

export default function TaskCreateModal({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const { data: skills = [] } = useQuery({ queryKey: ['skills'], queryFn: fetchSkills, staleTime: 30_000 })

  const [mode, setMode] = useState<Mode>('skill')
  const [name, setName] = useState('')
  const [cronExpr, setCronExpr] = useState('0 9 * * *')
  const [skill, setSkill] = useState('')
  const [argsRaw, setArgsRaw] = useState('{}')
  const [prompt, setPrompt] = useState('')
  const [timezone, setTimezone] = useState('')
  const [oneShot, setOneShot] = useState(false)
  const [chatId, setChatId] = useState('')
  const [maxRetries, setMaxRetries] = useState(0)
  const [retryDelay, setRetryDelay] = useState(60)

  const tz = timezone.trim()
  const tzValid = isValidTimezone(tz)
  const zones = useMemo(() => {
    const own = browserTimezone()
    return own && !COMMON_TIMEZONES.includes(own) ? [own, ...COMMON_TIMEZONES] : COMMON_TIMEZONES
  }, [])
  const chatIdValid = chatId.trim() === '' || /^-?\d+$/.test(chatId.trim())

  const mut = useMutation({
    mutationFn: () => {
      const req: CreateTaskRequest = {
        name: name.trim(),
        cron_expr: cronExpr.trim(),
        timezone: tz,
        one_shot: oneShot,
        max_retries: maxRetries,
        retry_delay_sec: retryDelay,
        notify_telegram_chat_id: chatId.trim() ? Number(chatId.trim()) : null,
      }
      if (mode === 'agent') {
        req.prompt = prompt.trim()
      } else {
        let args: Record<string, unknown>
        try {
          args = JSON.parse(argsRaw || '{}')
        } catch {
          throw new Error('Invalid JSON in args')
        }
        req.skill = skill
        req.args = args
      }
      return createTask(req)
    },
    onSuccess: () => {
      toast.success('Task created')
      qc.invalidateQueries({ queryKey: ['tasks'] })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const canSubmit = name.trim() && cronExpr.trim() && tzValid && chatIdValid &&
    (mode === 'agent' ? prompt.trim() : skill) && !mut.isPending

  const modeBtn = (m: Mode, label: string, Icon: typeof Bot) => (
    <button
      type="button"
      onClick={() => setMode(m)}
      className={[
        'flex flex-1 items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
        mode === m ? 'bg-accent/20 text-accent' : 'text-gray-500 hover:text-gray-300',
      ].join(' ')}
    >
      <Icon size={12} /> {label}
    </button>
  )

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm p-4 sm:pt-10">
      <div className="w-full max-w-md max-h-full overflow-y-auto rounded-2xl border border-surface-border bg-surface-raised p-6 shadow-2xl animate-slide-up">
        <div className="flex items-center justify-between mb-5">
          <h2 className="text-sm font-semibold text-gray-200">New scheduled task</h2>
          <button onClick={onClose} className="text-gray-600 hover:text-gray-300"><X size={16} /></button>
        </div>

        <div className="space-y-3">
          <div>
            <label className={labelCls}>Name</label>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="my-task" className={inputCls} />
          </div>
          <div className="grid grid-cols-5 gap-2">
            <div className="col-span-3">
              <label className={labelCls}>Cron expression</label>
              <input value={cronExpr} onChange={(e) => setCronExpr(e.target.value)} placeholder="0 9 * * *" className={`${inputCls} font-mono`} />
            </div>
            <div className="col-span-2">
              <label className={labelCls}>Timezone</label>
              <input
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
                placeholder="UTC"
                list="task-timezones"
                className={tzValid ? inputCls : invalidCls}
              />
              <datalist id="task-timezones">
                {zones.map((z) => <option key={z} value={z} />)}
              </datalist>
            </div>
          </div>
          <div>
            <p className="text-[10px] text-gray-600">
              minute hour day month weekday — evaluated in {tz && tzValid ? tz : 'UTC'}
              {!tzValid && <span className="text-red-400/70"> (unknown timezone)</span>}
            </p>
            {tzValid && <CronPreview expr={cronExpr} tz={tz} />}
          </div>

          <div className="flex gap-1 rounded-xl border border-surface-border bg-surface p-1">
            {modeBtn('skill', 'Run a skill', Wrench)}
            {modeBtn('agent', 'Ask the agent', Bot)}
          </div>

          {mode === 'skill' ? (
            <>
              <div>
                <label className={labelCls}>Skill</label>
                <div className="relative">
                  <select
                    value={skill}
                    onChange={(e) => setSkill(e.target.value)}
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
                <label className={labelCls}>Args (JSON)</label>
                <textarea
                  rows={3}
                  value={argsRaw}
                  onChange={(e) => setArgsRaw(e.target.value)}
                  className={`${inputCls} font-mono text-xs resize-none`}
                />
              </div>
            </>
          ) : (
            <div>
              <label className={labelCls}>Prompt</label>
              <textarea
                rows={4}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder="Summarise today's headlines about…"
                className={`${inputCls} text-xs resize-y`}
              />
              <p className="mt-1 text-[10px] text-gray-600">The agent answers this on every fire, with all tools available.</p>
            </div>
          )}

          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className={labelCls}>Max retries (0-10)</label>
              <input
                type="number" min={0} max={10}
                value={maxRetries}
                onChange={(e) => setMaxRetries(Math.min(10, Math.max(0, parseInt(e.target.value, 10) || 0)))}
                className={inputCls}
              />
            </div>
            <div>
              <label className={labelCls}>Retry delay (s)</label>
              <input
                type="number" min={0} max={86400}
                value={retryDelay}
                disabled={maxRetries === 0}
                onChange={(e) => setRetryDelay(Math.min(86400, Math.max(0, parseInt(e.target.value, 10) || 0)))}
                className={`${inputCls} disabled:opacity-40`}
              />
            </div>
          </div>
          <div>
            <label className={labelCls}>Telegram chat id (optional)</label>
            <input
              value={chatId}
              onChange={(e) => setChatId(e.target.value)}
              placeholder="e.g. 123456789"
              inputMode="numeric"
              className={`${chatIdValid ? inputCls : invalidCls} font-mono`}
            />
          </div>
          <label className="flex items-center gap-2 text-xs text-gray-400 cursor-pointer">
            <input type="checkbox" checked={oneShot} onChange={(e) => setOneShot(e.target.checked)} className="accent-indigo-500" />
            One-shot — disable after the first run
          </label>
        </div>

        <div className="mt-5 flex justify-end gap-2">
          <button onClick={onClose} className="rounded-xl px-4 py-2 text-sm text-gray-500 hover:text-gray-300 transition-colors">
            Cancel
          </button>
          <button
            onClick={() => mut.mutate()}
            disabled={!canSubmit}
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
