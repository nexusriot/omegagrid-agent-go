import type {
  Session, Message, Skill, SchedulerTask,
  CreateTaskRequest, MemoryHit, MemoryAddResult,
  HealthStatus, QueryResult, QueryRequest,
  Invocation, InvocationListResult, InvocationFilter, ReplayResult,
} from './types'

const BASE = import.meta.env.VITE_API_BASE ?? ''

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    ...init,
  })
  if (!res.ok) {
    const msg = await res.text().catch(() => res.statusText)
    throw new Error(`${res.status}: ${msg}`)
  }
  return res.json() as Promise<T>
}

export const fetchHealth = () => json<HealthStatus>('/health')

export const fetchSessions = (limit = 50) =>
  json<{ sessions: Session[] }>(`/api/sessions?limit=${limit}`).then(r => r.sessions)

export const createSession = () =>
  json<{ session_id: number }>('/api/sessions/new', { method: 'POST' }).then(r => r.session_id)

export const fetchMessages = (sessionId: number, limit = 200) =>
  json<{ messages: Message[] }>(`/api/sessions/${sessionId}/messages?limit=${limit}`).then(r => r.messages)

export const query = (req: QueryRequest) =>
  json<QueryResult>('/api/query', { method: 'POST', body: JSON.stringify(req) })

export const searchMemory = (q: string, k = 10) =>
  json<{ hits: MemoryHit[] | null }>('/api/memory/search', {
    method: 'POST',
    body: JSON.stringify({ query: q, k }),
  }).then(r => r.hits ?? [])

export const addMemory = (text: string, meta: Record<string, unknown> = {}) =>
  json<MemoryAddResult>('/api/memory/add', {
    method: 'POST',
    body: JSON.stringify({ text, meta }),
  })

export const fetchSkills = () =>
  json<{ skills: Skill[] }>('/api/skills').then(r => r.skills)

export const fetchTasks = () =>
  json<SchedulerTask[]>('/api/scheduler/tasks').then(r => r ?? [])

export const createTask = (req: CreateTaskRequest) =>
  json<SchedulerTask>('/api/scheduler/tasks', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const deleteTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}`, { method: 'DELETE' })

export const enableTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}/enable`, { method: 'POST' })

export const disableTask = (id: number) =>
  json<{ ok: boolean }>(`/api/scheduler/tasks/${id}/disable`, { method: 'POST' })

export const fetchInvocations = (f: InvocationFilter = {}) => {
  const p = new URLSearchParams()
  if (f.skill)         p.set('skill', f.skill)
  if (f.session_id)    p.set('session_id', String(f.session_id))
  if (f.only_errors)   p.set('only_errors', 'true')
  if (f.limit)         p.set('limit', String(f.limit))
  if (f.offset)        p.set('offset', String(f.offset))
  const qs = p.toString()
  return json<InvocationListResult>(`/api/invocations${qs ? '?' + qs : ''}`)
}

export const fetchInvocation = (id: number) =>
  json<Invocation>(`/api/invocations/${id}`)

export const replayInvocation = (id: number) =>
  json<ReplayResult>(`/api/invocations/${id}/replay`, { method: 'POST' })

export const invokeSkill = (name: string, args: Record<string, unknown>) =>
  json<import('./types').SkillInvokeResult>(`/api/skills/${encodeURIComponent(name)}/invoke`, {
    method: 'POST',
    body: JSON.stringify({ args }),
  })
