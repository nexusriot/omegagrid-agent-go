package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScheduleTaskSkill is the Go-side replacement for the Python schedule_task
// skill.  It is registered directly into the agent's tool table by the gateway
// so that the agent can create / list / delete cron tasks against the Go
// scheduler store.
type ScheduleTaskSkill struct {
	Store *Store
}

// SkillSchema returns the JSON schema entry the agent loop merges with the
// sidecar's skill list when assembling the system prompt.
func (s *ScheduleTaskSkill) SkillSchema() map[string]any {
	return map[string]any{
		"name": "schedule_task",
		"description": "Manage scheduled tasks. Actions: 'create' a new task " +
			"(runs a skill on cron schedule, optionally notifies Telegram), 'list' all scheduled tasks, " +
			"'delete' a task by id, 'delete_all' to remove every scheduled task (no task_id needed), " +
			"'enable'/'disable' a task by id, 'history' of a task's runs by id. " +
			"cron_expr is evaluated in UTC unless timezone is given (an IANA name such as 'Europe/Berlin'); " +
			"prefer passing the user's timezone over converting times yourself. " +
			"A task runs either a skill (skill + args) or, for open-ended jobs ('every morning summarise the news'), " +
			"an agent prompt (prompt, no skill). " +
			"For one-time reminders ('tell me at 3pm') set one_shot=true and skill='reminder' " +
			"with args={\"message\": \"...\"}; the task auto-disables after it fires. " +
			"max_retries re-runs a failed run (retry_delay_sec apart, doubling).",
		"parameters": map[string]any{
			"action":                  map[string]any{"type": "string", "description": "Action: create, list, history, delete, delete_all, enable, disable", "required": true},
			"name":                    map[string]any{"type": "string", "description": "Task name (for create)", "required": false},
			"cron_expr":               map[string]any{"type": "string", "description": "Cron expression, e.g. '*/5 * * * *' (for create)", "required": false},
			"timezone":                map[string]any{"type": "string", "description": "IANA time zone the cron is in, e.g. 'Europe/Berlin' (default UTC)", "required": false},
			"skill":                   map[string]any{"type": "string", "description": "Skill to run, e.g. 'ping_check', 'weather', 'reminder' (for create; omit when using prompt)", "required": false},
			"prompt":                  map[string]any{"type": "string", "description": "Agent prompt to answer on each run instead of calling one skill (for create)", "required": false},
			"max_retries":             map[string]any{"type": "number", "description": "Re-run a failed run up to N times, 0-10 (for create)", "required": false},
			"retry_delay_sec":         map[string]any{"type": "number", "description": "Seconds before the first retry, doubling after (default 60)", "required": false},
			"args":                    map[string]any{"type": "object", "description": "Arguments for the skill (for create)", "required": false},
			"one_shot":                map[string]any{"type": "boolean", "description": "Run once then auto-disable (for create). Use for one-time reminders.", "required": false},
			"notify_telegram_chat_id": map[string]any{"type": "number", "description": "Telegram chat ID to send results to (for create). Use the current chat_id if user asks for Telegram notifications.", "required": false},
			"task_id":                 map[string]any{"type": "number", "description": "Task ID (for delete/enable/disable/history)", "required": false},
		},
	}
}

// Execute implements the agent's skill calling convention.  args mirrors what
// the LLM emits, so we accept loose types and coerce.
func (s *ScheduleTaskSkill) Execute(args map[string]any) any {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	switch action {
	case "create":
		return s.create(args)
	case "list":
		return s.list()
	case "delete":
		return s.delete(args)
	case "delete_all", "deleteall", "delete-all":
		return s.deleteAll()
	case "enable":
		return s.setEnabled(args, true)
	case "disable":
		return s.setEnabled(args, false)
	case "history", "runs":
		return s.history(args)
	default:
		return map[string]any{"error": fmt.Sprintf("Unknown action: %s. Use: create, list, history, delete, delete_all, enable, disable", action)}
	}
}

func (s *ScheduleTaskSkill) create(args map[string]any) any {
	innerArgs, err := taskArgs(args["args"])
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	spec := TaskSpec{
		Name:          asString(args["name"]),
		CronExpr:      asString(args["cron_expr"]),
		Skill:         asString(args["skill"]),
		Prompt:        strings.TrimSpace(asString(args["prompt"])),
		Args:          innerArgs,
		OneShot:       asBool(args["one_shot"]),
		Timezone:      strings.TrimSpace(asString(args["timezone"])),
		MaxRetries:    int(asInt64(args["max_retries"])),
		RetryDelaySec: int(asInt64(args["retry_delay_sec"])),
	}
	if v, ok := args["notify_telegram_chat_id"]; ok && v != nil {
		if n := asInt64(v); n != 0 {
			spec.NotifyTelegramChatID = &n
		}
	}
	// Validate up front: Matches() returns false for malformed expressions, so
	// an unvalidated bad cron_expr would create a task that silently never fires.
	if err := ValidateSpec(spec); err != nil {
		return map[string]any{"error": err.Error()}
	}
	if spec.Name == "" {
		what := spec.Skill
		if spec.Prompt != "" {
			what = AgentSkill
		}
		spec.Name = fmt.Sprintf("%s (%s)", what, spec.CronExpr)
	}
	task, err := s.Store.CreateTask(spec)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"created": true, "task": task}
}

func (s *ScheduleTaskSkill) history(args map[string]any) any {
	id := asInt64(args["task_id"])
	if id == 0 {
		return map[string]any{"error": "task_id is required"}
	}
	runs, err := s.Store.ListRuns(id, int(asInt64(args["limit"])))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"task_id": id, "count": len(runs), "runs": runs}
}

func (s *ScheduleTaskSkill) list() any {
	tasks, err := s.Store.ListAll()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"count": len(tasks), "tasks": tasks}
}

func (s *ScheduleTaskSkill) delete(args map[string]any) any {
	id := asInt64(args["task_id"])
	if id == 0 {
		return map[string]any{"error": "task_id is required"}
	}
	ok, err := s.Store.Delete(id)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Task %d not found", id)}
	}
	return map[string]any{"deleted": true, "task_id": id}
}

func (s *ScheduleTaskSkill) deleteAll() any {
	n, err := s.Store.DeleteAll()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"deleted_all": true, "deleted_count": n}
}

func (s *ScheduleTaskSkill) setEnabled(args map[string]any, enabled bool) any {
	id := asInt64(args["task_id"])
	if id == 0 {
		return map[string]any{"error": "task_id is required"}
	}
	ok, err := s.Store.SetEnabled(id, enabled)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Task %d not found", id)}
	}
	return map[string]any{"ok": true, "task_id": id, "enabled": enabled}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// asBool coerces the loose types the LLM emits for boolean parameters.
func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}

// taskArgs reads the inner skill args. Models often send them as a JSON
// string ("{\"message\":\"x\"}") rather than an object; that used to be
// replaced by {} silently, so a reminder fired with no message. Anything
// that is neither an object nor a JSON-object string is an error.
func taskArgs(v any) (map[string]any, error) {
	switch a := v.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return a, nil
	case string:
		if strings.TrimSpace(a) == "" {
			return map[string]any{}, nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(a), &m); err != nil || m == nil {
			return nil, fmt.Errorf("args must be a JSON object, got the string %q", a)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("args must be a JSON object, got %T", v)
	}
}
