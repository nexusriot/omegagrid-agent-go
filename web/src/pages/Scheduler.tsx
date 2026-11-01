import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Clock, Plus, Trash2, Play, Pause, Loader2, CheckCircle2, XCircle, Calendar,
  Zap, History, Bot, RotateCcw, Globe, Send,
} from 'lucide-react'
import { toast } from 'sonner'
import { fetchTasks, deleteTask, enableTask, disableTask, runTask } from '../api/client'
import type { SchedulerTask } from '../api/types'
import TaskCreateModal, { CronPreview } from '../components/TaskCreateModal'
import TaskRunHistory, { resultPreview } from '../components/TaskRunHistory'
import { isValidTimezone } from '../lib/cron'

function fmtTs(ts: number | null): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

function StatusBadge({ enabled }: { enabled: boolean }) {
  return enabled
    ? <span className="flex items-center gap-1 text-[11px] text-emerald-400"><CheckCircle2 size={10} /> enabled</span>
    : <span className="flex items-center gap-1 text-[11px] text-gray-600"><XCircle size={10} /> disabled</span>
}

function isAgentTask(t: SchedulerTask): boolean {
  return !!t.prompt || t.skill === 'agent'
}

function TaskRow({ task }: { task: SchedulerTask }) {
  const qc = useQueryClient()
  const [showHistory, setShowHistory] = useState(false)
  const agent = isAgentTask(task)

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

  const runMut = useMutation({
    mutationFn: () => runTask(task.id),
    onSuccess: (run) => {
      const line = resultPreview(run.result)
      if (run.ok) toast.success(`${task.name}: run succeeded`, { description: line })
      else toast.error(`${task.name}: run failed`, { description: line })
    },
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['tasks'] })
      qc.invalidateQueries({ queryKey: ['task-runs', task.id] })
    },
  })

  return (
    <div className="rounded-2xl border border-surface-border bg-surface-overlay p-4 animate-fade-in">
      <div className="flex items-start justify-between gap-3 mb-3">
        <div className="min-w-0">
          <div className="font-medium text-sm text-gray-200">{task.name}</div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1">
            <code className="rounded bg-surface px-2 py-0.5 font-mono text-[11px] text-indigo-300">{task.cron_expr}</code>
            {task.timezone && (
              <span className="flex items-center gap-1 text-[11px] text-gray-500" title="Cron timezone">
                <Globe size={10} /> {task.timezone}
              </span>
            )}
            {agent
              ? <span className="flex items-center gap-1 rounded bg-accent/15 px-1.5 py-0.5 text-[10px] font-medium text-accent"><Bot size={10} /> agent</span>
              : <code className="font-mono text-[11px] text-gray-500">{task.skill}</code>}
            <StatusBadge enabled={task.enabled} />
            {task.one_shot && <span className="text-[10px] text-gray-500">one-shot</span>}
            {task.max_retries > 0 && (
              <span className="flex items-center gap-1 text-[10px] text-gray-500" title="Retries on failure">
                <RotateCcw size={9} /> {task.max_retries}× / {task.retry_delay_sec}s
              </span>
            )}
            {task.notify_telegram_chat_id != null && (
              <span className="flex items-center gap-1 text-[10px] text-gray-500" title="Telegram chat notified">
                <Send size={9} /> {task.notify_telegram_chat_id}
              </span>
            )}
          </div>
          {agent && task.prompt && (
            <p className="mt-1.5 line-clamp-2 text-[11px] italic text-gray-500" title={task.prompt}>“{task.prompt}”</p>
          )}
          {task.next_retry_at != null && (
            <p className="mt-1.5 flex items-center gap-1 text-[11px] text-amber-400">
              <RotateCcw size={10} /> retry {task.retry_attempt} pending at {fmtTs(task.next_retry_at)}
            </p>
          )}
          {task.enabled && isValidTimezone(task.timezone) && <CronPreview expr={task.cron_expr} tz={task.timezone} count={1} />}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <button
            onClick={() => runMut.mutate()}
            disabled={runMut.isPending}
            title="Run now"
            className="flex h-7 items-center gap-1 rounded-lg px-2 text-[11px] text-gray-500 hover:bg-surface-border hover:text-gray-300 transition-colors disabled:opacity-60"
          >
            {runMut.isPending ? <Loader2 size={12} className="animate-spin" /> : <Zap size={12} />}
            {runMut.isPending ? 'Running…' : 'Run now'}
          </button>
          <button
            onClick={() => setShowHistory((s) => !s)}
            title="Run history"
            className={`flex h-7 w-7 items-center justify-center rounded-lg transition-colors ${showHistory ? 'bg-accent/20 text-accent' : 'text-gray-500 hover:bg-surface-border hover:text-gray-300'}`}
          >
            <History size={13} />
          </button>
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

      {showHistory && <TaskRunHistory taskId={task.id} />}
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
              Create tasks to run skills or agent prompts on a cron schedule. Results can be sent to Telegram.
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

      {showCreate && <TaskCreateModal onClose={() => setShowCreate(false)} />}
    </div>
  )
}
