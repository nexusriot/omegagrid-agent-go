export interface Session {
  id: number
  created_at: number
  message_count?: number
}

export interface Message {
  id: number
  session_id: number
  ts: number
  role: 'user' | 'assistant' | 'tool' | 'raw_model_json'
  content: string
}

export interface SkillParam {
  type: string
  description: string
  required?: boolean
}

export interface Skill {
  name: string
  description: string
  parameters: Record<string, SkillParam>
  body?: string
}

export interface SchedulerTask {
  id: number
  name: string
  cron_expr: string
  skill: string
  args_json: string
  notify_telegram_chat_id: number | null
  enabled: boolean
  created_at: number
  last_run_at: number | null
  last_result: string | null
  run_count: number
}

export interface CreateTaskRequest {
  name: string
  cron_expr: string
  skill: string
  args: Record<string, unknown>
  notify_telegram_chat_id?: number | null
}

export interface MemoryHit {
  id: string
  text: string
  metadata: Record<string, unknown>
  distance: number
}

export interface MemoryAddResult {
  ok: boolean
  memory_id: string
  skipped: boolean
  reason?: string
}

export interface HealthStatus {
  ok: boolean
  provider: string
  chat_model: string
  chat_base: string
  skills_dir: string
  scheduler_db: string
  embed_model: string
  embed_ok: boolean
  embed_error?: string
}

export interface QueryRequest {
  query: string
  session_id?: number
  remember?: boolean
  max_steps?: number
}

export interface QueryResult {
  session_id: number
  answer: string
  meta: {
    steps: number
    model: string
    timings: Record<string, number>
  }
  memories?: MemoryHit[]
  debug_log?: string
}

export interface SkillAttachment {
  type: string
  filename: string
  mime_type: string
  base64: string
}

export interface SkillInvokeResult {
  name: string
  args: Record<string, unknown>
  result: unknown
  elapsed_s: number
  error: string | null
  attachments: SkillAttachment[] | null
}

// SSE event payloads
export interface ThinkingEvent  { step: number }
export interface ToolCallEvent  { step: number; tool: string; args: Record<string, unknown>; why: string }
export interface ToolResultEvent { step: number; tool: string; result: string; elapsed_s: number }
export interface FinalEvent     { session_id: number; answer: string; meta: QueryResult['meta'] }
export interface ErrorEvent     { message: string }

export type StreamEvent =
  | { event: 'thinking';    data: ThinkingEvent }
  | { event: 'tool_call';   data: ToolCallEvent }
  | { event: 'tool_result'; data: ToolResultEvent }
  | { event: 'final';       data: FinalEvent }
  | { event: 'error';       data: ErrorEvent }
